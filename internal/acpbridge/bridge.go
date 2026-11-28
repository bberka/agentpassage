package acpbridge

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/gateway"
	"github.com/bberka/agentpassage/internal/router"
	"github.com/bberka/agentpassage/internal/skills"
)

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type bridgeSession struct {
	id         string
	cwd        string
	model      string
	effort     string
	mode       string
	hostMode   bool // mode was set by the host (set_mode / set_config_option)
	title      string
	updatedAt  time.Time
	mcpServers []any
	history    []backend.Message

	promptMu sync.Mutex
	cancel   context.CancelFunc

	cmdMu    sync.Mutex
	commands []map[string]any // the agent's own slash commands
	cmdModel int              // counts model switches, to drop a left model's list
	prepared chan struct{}    // closed when the upstream session is ready; guarded by sessMu
}

type persistedSession struct {
	ID        string            `json:"id"`
	Cwd       string            `json:"cwd,omitempty"`
	Model     string            `json:"model,omitempty"`
	Effort    string            `json:"effort,omitempty"`
	Mode      string            `json:"mode,omitempty"`
	HostMode  bool              `json:"host_mode,omitempty"`
	Title     string            `json:"title,omitempty"`
	UpdatedAt time.Time         `json:"updated_at"`
	History   []backend.Message `json:"history,omitempty"`
}

// Server exposes apx itself as an ACP JSON-RPC 2.0 stdio server (`apx acp`).
// Any ACP host (T3 Code, Zed, Delta, JetBrains, etc.) can register `apx acp` and
// immediately select pooled rotated models or account-prefixed models with full
// native ACP passthrough (thoughts, tools, usage, images, permissions, cancel, live reload).
type Server struct {
	gw         *gateway.Server
	writeMu    sync.Mutex
	sessMu     sync.Mutex
	nextID     atomic.Int64
	nextCallID atomic.Int64
	sess       map[string]*bridgeSession

	pendingMu sync.Mutex
	pending   map[string]chan rpcMsg
}

func New(gw *gateway.Server) *Server {
	return &Server{
		gw:      gw,
		sess:    make(map[string]*bridgeSession),
		pending: make(map[string]chan rpcMsg),
	}
}

func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	serveCtx, serveCancel := context.WithCancel(ctx)
	defer serveCancel()

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024) // prompts carry base64 images, audio and files

	write := func(m rpcMsg) {
		raw, err := json.Marshal(m)
		if err != nil {
			return
		}
		s.writeMu.Lock()
		defer s.writeMu.Unlock()
		_, _ = w.Write(append(raw, '\n'))
	}

	replyOK := func(id json.RawMessage, res any) {
		raw, _ := json.Marshal(res)
		write(rpcMsg{JSONRPC: "2.0", ID: id, Result: raw})
	}
	replyErr := func(id json.RawMessage, code int, msg string) {
		write(rpcMsg{JSONRPC: "2.0", ID: id, Error: &rpcErr{Code: code, Message: msg}})
	}
	notify := func(method string, params any) {
		raw, _ := json.Marshal(params)
		write(rpcMsg{JSONRPC: "2.0", Method: method, Params: raw})
	}

	notifyOptions := func(sessionID string, configOpts []map[string]any) {
		notify("session/update", map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "config_option_update",
				"configOptions": configOpts,
			},
		})
	}

	callHost := func(callCtx context.Context, method string, params any) (json.RawMessage, error) {
		callID := fmt.Sprintf("apx-req-%d", s.nextCallID.Add(1))
		idRaw, _ := json.Marshal(callID)
		pRaw, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		ch := make(chan rpcMsg, 1)
		s.pendingMu.Lock()
		s.pending[callID] = ch
		s.pendingMu.Unlock()

		write(rpcMsg{JSONRPC: "2.0", ID: idRaw, Method: method, Params: pRaw})

		select {
		case <-callCtx.Done():
			s.pendingMu.Lock()
			delete(s.pending, callID)
			s.pendingMu.Unlock()
			return nil, callCtx.Err()
		case <-serveCtx.Done():
			s.pendingMu.Lock()
			delete(s.pending, callID)
			s.pendingMu.Unlock()
			return nil, serveCtx.Err()
		case resp, ok := <-ch:
			if !ok {
				return nil, errors.New("host connection closed")
			}
			if resp.Error != nil {
				return nil, fmt.Errorf("host RPC error %d: %s", resp.Error.Code, resp.Error.Message)
			}
			return resp.Result, nil
		}
	}

	// Background config.toml watcher: reloads config and pushes config_option_update to active sessions.
	if dataDir := s.gw.DataDir(); dataDir != "" {
		go s.watchConfig(serveCtx, dataDir, notify)
	}

	var promptWG, prepareWG sync.WaitGroup
	prepare := func(bs *bridgeSession) {
		s.prepareSession(serveCtx, &prepareWG, bs, notify)
	}

	for scanner.Scan() {
		if serveCtx.Err() != nil {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg rpcMsg
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}

		// Dispatch host responses to our outgoing requests (e.g. session/request_permission).
		if msg.Method == "" && len(msg.ID) > 0 {
			key := strings.Trim(strings.TrimSpace(string(msg.ID)), `"`)
			s.pendingMu.Lock()
			ch, ok := s.pending[key]
			delete(s.pending, key)
			s.pendingMu.Unlock()
			if ok {
				ch <- msg
			}
			continue
		}

		switch msg.Method {
		case "initialize":
			replyOK(msg.ID, map[string]any{
				"protocolVersion": 1,
				"agentInfo": map[string]string{
					"name":    "apx ACP Gateway",
					"version": gateway.Version,
				},
				"agentCapabilities": map[string]any{
					"loadSession": true,
					"sessionCapabilities": map[string]any{
						"load":   map[string]any{},
						"list":   map[string]any{},
						"fork":   map[string]any{},
						"resume": map[string]any{},
					},
					"promptCapabilities": map[string]any{
						"image":           true,
						"audio":           true,
						"embeddedContext": true,
					},
				},
				"authMethods": []any{},
			})

		case "session/new":
			var p struct {
				Cwd        string `json:"cwd"`
				MCPServers []any  `json:"mcpServers"`
			}
			_ = json.Unmarshal(msg.Params, &p)

			defaultMode := s.defaultMode()
			configOpts, availModels, defaultModel, defaultEffort := s.buildModelOptions("", "", defaultMode)

			sid := s.newSessionID()
			bs := &bridgeSession{
				id:         sid,
				cwd:        p.Cwd,
				model:      defaultModel,
				effort:     defaultEffort,
				mode:       defaultMode,
				updatedAt:  time.Now(),
				mcpServers: p.MCPServers,
			}
			s.sessMu.Lock()
			s.sess[sid] = bs
			s.sessMu.Unlock()
			s.saveSession(bs)

			replyOK(msg.ID, s.sessionReply(sid, defaultModel, configOpts, availModels, defaultMode))
			sendSkills(notify, sid, s.gw.UserSkills(p.Cwd))
			prepare(bs)
			if s.hostTools() {
				s.gw.AnnounceNewTools(p.Cwd)
			}

		case "session/load", "session/resume":
			var p struct {
				SessionID  string `json:"sessionId"`
				Cwd        string `json:"cwd"`
				MCPServers []any  `json:"mcpServers"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			bs, ok := s.loadOrGetSession(p.SessionID)
			if !ok {
				replyErr(msg.ID, -32602, "session not found: "+p.SessionID)
				continue
			}
			s.sessMu.Lock()
			if p.Cwd != "" {
				bs.cwd = p.Cwd
			}
			if len(p.MCPServers) > 0 {
				bs.mcpServers = p.MCPServers
			}
			if !s.exposeModes() && !bs.hostMode {
				// Sessions saved before modes were hidden defaulted to "auto",
				// which would override the account's permission_mode.
				bs.mode = s.defaultMode()
			}
			curModel, curEffort, curMode := bs.model, bs.effort, bs.mode
			hist := append([]backend.Message(nil), bs.history...)
			s.sessMu.Unlock()

			configOpts, availModels, resolvedModel, resolvedEffort := s.buildModelOptions(curModel, curEffort, curMode)
			s.sessMu.Lock()
			bs.model = resolvedModel
			bs.effort = resolvedEffort
			s.sessMu.Unlock()

			// Replay persisted turns as session/update chunks per ACP loadSession spec.
			for _, m := range hist {
				updType := "user_message_chunk"
				if m.Role == "assistant" {
					updType = "agent_message_chunk"
				}
				notify("session/update", map[string]any{
					"sessionId": bs.id,
					"update": map[string]any{
						"sessionUpdate": updType,
						"content": map[string]any{
							"type": "text",
							"text": m.Content,
						},
					},
				})
			}

			replyOK(msg.ID, s.sessionReply(bs.id, resolvedModel, configOpts, availModels, normalizeBridgeMode(curMode)))
			sendSkills(notify, bs.id, s.gw.UserSkills(bs.cwd))
			prepare(bs)

		case "session/list":
			list := s.listPersistedSessions()
			replyOK(msg.ID, map[string]any{
				"sessions": list,
			})

		case "session/fork":
			var p struct {
				SessionID  string `json:"sessionId"`
				Cwd        string `json:"cwd"`
				MCPServers []any  `json:"mcpServers"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			orig, ok := s.loadOrGetSession(p.SessionID)
			if !ok {
				replyErr(msg.ID, -32602, "session not found: "+p.SessionID)
				continue
			}
			s.sessMu.Lock()
			forkCwd := orig.cwd
			if p.Cwd != "" {
				forkCwd = p.Cwd
			}
			forkMCP := orig.mcpServers
			if len(p.MCPServers) > 0 {
				forkMCP = p.MCPServers
			}
			forkModel, forkEffort, forkMode, forkTitle := orig.model, orig.effort, orig.mode, orig.title
			forkHostMode := orig.hostMode
			if !s.exposeModes() && !forkHostMode {
				forkMode = s.defaultMode()
			}
			forkHist := append([]backend.Message(nil), orig.history...)
			s.sessMu.Unlock()

			newSID := s.newSessionID()
			configOpts, availModels, resolvedModel, resolvedEffort := s.buildModelOptions(forkModel, forkEffort, forkMode)
			forked := &bridgeSession{
				id:         newSID,
				cwd:        forkCwd,
				model:      resolvedModel,
				effort:     resolvedEffort,
				mode:       normalizeBridgeMode(forkMode),
				hostMode:   forkHostMode,
				title:      forkTitle,
				updatedAt:  time.Now(),
				mcpServers: forkMCP,
				history:    forkHist,
			}
			s.sessMu.Lock()
			s.sess[newSID] = forked
			s.sessMu.Unlock()
			s.saveSession(forked)

			replyOK(msg.ID, s.sessionReply(newSID, resolvedModel, configOpts, availModels, forked.mode))
			sendSkills(notify, newSID, s.gw.UserSkills(forkCwd))
			prepare(forked)

		case "session/set_config_option":
			var p struct {
				SessionID string `json:"sessionId"`
				ConfigID  string `json:"configId"`
				Value     string `json:"value"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			curModel := ""
			curEffort := ""
			curMode := s.defaultMode()
			var switched *bridgeSession
			s.sessMu.Lock()
			if bs, ok := s.sess[p.SessionID]; ok {
				before := bs.model
				switch strings.ToLower(p.ConfigID) {
				case "thought_level", "reasoning_effort", "effort", "thinking":
					bs.effort = router.NormalizeEffort(p.Value)
				case "mode":
					bs.mode = normalizeBridgeMode(p.Value)
					bs.hostMode = true
				default: // "model" or empty
					bs.model = p.Value
				}
				curModel = bs.model
				curEffort = bs.effort
				curMode = bs.mode
				if bs.model != before {
					switched = bs
				}
			} else if p.ConfigID == "model" || p.ConfigID == "" {
				curModel = p.Value
			} else if strings.EqualFold(p.ConfigID, "mode") {
				curMode = normalizeBridgeMode(p.Value)
			}
			s.sessMu.Unlock()
			configOpts := s.refreshOptions(p.SessionID, curModel, curEffort, curMode)
			replyOK(msg.ID, map[string]any{
				"configOptions": configOpts,
			})
			if switched != nil {
				prepare(switched)
			}

		case "session/set_mode":
			var p struct {
				SessionID string `json:"sessionId"`
				ModeID    string `json:"modeId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			curModel := ""
			curEffort := ""
			curMode := normalizeBridgeMode(p.ModeID)
			s.sessMu.Lock()
			if bs, ok := s.sess[p.SessionID]; ok {
				bs.mode = curMode
				bs.hostMode = true
				curModel = bs.model
				curEffort = bs.effort
			}
			s.sessMu.Unlock()
			configOpts := s.refreshOptions(p.SessionID, curModel, curEffort, curMode)
			notifyOptions(p.SessionID, configOpts)
			replyOK(msg.ID, map[string]any{})

		case "session/set_model":
			var p struct {
				SessionID string `json:"sessionId"`
				ModelID   string `json:"modelId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			curModel := p.ModelID
			curEffort := ""
			curMode := s.defaultMode()
			var switched *bridgeSession
			s.sessMu.Lock()
			if bs, ok := s.sess[p.SessionID]; ok {
				if bs.model != p.ModelID {
					switched = bs
				}
				bs.model = p.ModelID
				curModel = bs.model
				curEffort = bs.effort
				curMode = bs.mode
			}
			s.sessMu.Unlock()
			configOpts := s.refreshOptions(p.SessionID, curModel, curEffort, curMode)
			notifyOptions(p.SessionID, configOpts)
			replyOK(msg.ID, map[string]any{})
			if switched != nil {
				prepare(switched)
			}

		case "session/prompt":
			var p struct {
				SessionID string `json:"sessionId"`
				Prompt    []struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					Data     string `json:"data"`
					MimeType string `json:"mimeType"`
					URI      string `json:"uri"`
					Name     string `json:"name"`
					Resource *struct {
						URI      string `json:"uri"`
						Text     string `json:"text"`
						Blob     string `json:"blob"`
						MimeType string `json:"mimeType"`
					} `json:"resource,omitempty"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(msg.Params, &p)

			s.sessMu.Lock()
			bs, ok := s.sess[p.SessionID]
			s.sessMu.Unlock()
			if !ok {
				replyErr(msg.ID, -32602, "unknown sessionId: "+p.SessionID)
				continue
			}

			var promptSB strings.Builder
			var atts []backend.Attachment
			for _, b := range p.Prompt {
				switch {
				case (b.Type == "image" || b.Type == "audio") && b.Data != "":
					atts = append(atts, backend.Attachment{Kind: b.Type, Data: b.Data, MimeType: b.MimeType, URI: b.URI})
				case b.Type == "resource" && b.Resource != nil && (b.Resource.Text != "" || b.Resource.Blob != ""):
					r := b.Resource
					a := backend.Attachment{Kind: backend.AttachFile, Text: r.Text, Data: r.Blob, MimeType: r.MimeType, URI: r.URI, Name: path.Base(r.URI)}
					if strings.HasPrefix(r.MimeType, "image/") && r.Blob != "" {
						a.Kind = backend.AttachImage
					}
					atts = append(atts, a)
				case b.Type == "resource_link" && b.URI != "":
					atts = append(atts, backend.Attachment{Kind: backend.AttachLink, URI: b.URI, Name: b.Name, MimeType: b.MimeType})
				case b.Text != "":
					if promptSB.Len() > 0 {
						promptSB.WriteString("\n")
					}
					promptSB.WriteString(b.Text)
				}
			}
			userText := promptSB.String()
			if note := skills.Instructions(userText, s.gw.UserSkills(bs.cwd)); note != "" {
				userText += "\n\n" + note
			}

			s.sessMu.Lock()
			if bs.model == "" {
				s.sessMu.Unlock()
				replyErr(msg.ID, -32602, "no model configured in apx; add an account first")
				continue
			}
			bs.history = append(bs.history, backend.Message{Role: "user", Content: userText, Attachments: atts})
			histSnap := append([]backend.Message(nil), bs.history...)
			reqModel := bs.model
			reqEffort := bs.effort
			reqMode := bs.mode
			reqHostMode := bs.hostMode
			reqCwd := bs.cwd
			reqMCP := bs.mcpServers
			prepared := bs.prepared
			s.sessMu.Unlock()
			onCommands := s.commandReceiver(bs, notify)

			msgID := append(json.RawMessage(nil), msg.ID...)
			promptCtx, promptCancel := context.WithCancel(serveCtx)
			bs.promptMu.Lock()
			if bs.cancel != nil {
				bs.cancel()
			}
			bs.cancel = promptCancel
			bs.promptMu.Unlock()

			// Only override the account's configured permission_mode when the host
			// explicitly set a mode (or when [acp] expose_modes is on). Otherwise
			// let each account's permission_mode (default: auto-approve) decide, while
			// still supplying permHandler in case an account is set to "ask".
			permMode := ""
			upstreamACPMode := ""
			if reqHostMode || s.exposeModes() {
				permMode = bridgeModeToPermission(reqMode)
				upstreamACPMode = reqMode
				if normalizeBridgeMode(reqMode) == "default" {
					permMode = config.PermAsk
				}
			}
			var permHandler func(context.Context, backend.PermissionRequest) backend.PermissionDecision
			if permMode == "" || permMode == config.PermAsk {
				permHandler = func(pCtx context.Context, req backend.PermissionRequest) backend.PermissionDecision {
					opts := make([]map[string]string, 0, len(req.Options))
					for _, o := range req.Options {
						opts = append(opts, map[string]string{
							"optionId": o.ID,
							"name":     o.Name,
							"kind":     o.Kind,
						})
					}
					toolCall := make(map[string]any)
					if len(req.RawToolCall) > 0 {
						_ = json.Unmarshal(req.RawToolCall, &toolCall)
					}
					if id, _ := toolCall["toolCallId"].(string); id == "" {
						if req.ToolCallID != "" {
							toolCall["toolCallId"] = req.ToolCallID
						} else if req.ID != "" {
							toolCall["toolCallId"] = "apx-call-" + req.ID
						} else {
							toolCall["toolCallId"] = fmt.Sprintf("apx-call-%d", s.nextCallID.Add(1))
						}
					}
					if _, ok := toolCall["title"]; !ok && req.Title != "" {
						toolCall["title"] = req.Title
					}
					if _, ok := toolCall["kind"]; !ok && req.Kind != "" {
						toolCall["kind"] = req.Kind
					}
					if _, ok := toolCall["rawInput"]; !ok && req.Arguments != "" && json.Valid([]byte(req.Arguments)) {
						toolCall["rawInput"] = json.RawMessage(req.Arguments)
					}
					resRaw, err := callHost(pCtx, "session/request_permission", map[string]any{
						"sessionId": bs.id,
						"toolCall":  toolCall,
						"options":   opts,
					})
					if err != nil {
						if logs := s.gw.Logs(); logs != nil {
							logs.Warn("acp", fmt.Sprintf("host session/request_permission error (session=%s toolCallId=%v): %v", bs.id, toolCall["toolCallId"], err))
						}
						return backend.PermissionDecision{Approved: false}
					}
					var out struct {
						Outcome struct {
							Outcome  string `json:"outcome"`
							OptionID string `json:"optionId"`
						} `json:"outcome"`
					}
					if json.Unmarshal(resRaw, &out) == nil && out.Outcome.Outcome == "selected" {
						return backend.PermissionDecision{Approved: true, OptionID: out.Outcome.OptionID}
					}
					if logs := s.gw.Logs(); logs != nil {
						logs.Warn("acp", fmt.Sprintf("host session/request_permission not selected (session=%s toolCallId=%v resp=%s)", bs.id, toolCall["toolCallId"], string(resRaw)))
					}
					return backend.PermissionDecision{Approved: false}
				}
			}

			promptWG.Add(1)
			go func() {
				defer promptWG.Done()
				defer func() {
					promptCancel()
					bs.promptMu.Lock()
					bs.cancel = nil
					bs.promptMu.Unlock()
				}()

				if prepared != nil {
					select {
					case <-prepared:
					case <-promptCtx.Done():
					}
				}

				var replySB strings.Builder
				_, err := s.gw.StreamDirect(promptCtx, backend.ChatRequest{
					Model:             reqModel,
					ReasoningEffort:   reqEffort,
					PermissionMode:    permMode,
					ACPMode:           upstreamACPMode,
					PermissionHandler: permHandler,
					MCPServers:        reqMCP,
					HostTools:         s.hostTools(),
					OnCommands:        onCommands,
					Messages:          histSnap,
					Stream:            true,
					SessionKey:        bs.id,
					Cwd:               reqCwd,
				}, func(ch backend.StreamChunk) error {
					if len(ch.RawACPUpdate) > 0 {
						notify("session/update", map[string]any{
							"sessionId": bs.id,
							"update":    ch.RawACPUpdate,
						})
						return nil
					}
					if ch.Reasoning != "" {
						notify("session/update", map[string]any{
							"sessionId": bs.id,
							"update": map[string]any{
								"sessionUpdate": "agent_thought_chunk",
								"content": map[string]any{
									"type": "text",
									"text": ch.Reasoning,
								},
							},
						})
					}
					if ch.Delta != "" {
						replySB.WriteString(ch.Delta)
						notify("session/update", map[string]any{
							"sessionId": bs.id,
							"update": map[string]any{
								"sessionUpdate": "agent_message_chunk",
								"content": map[string]any{
									"type": "text",
									"text": ch.Delta,
								},
							},
						})
					}
					return nil
				})
				if err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(promptCtx.Err(), context.Canceled) {
						replyOK(msgID, map[string]any{"stopReason": "cancelled"})
						return
					}
					replyErr(msgID, -32000, err.Error())
					return
				}

				s.sessMu.Lock()
				bs.history = append(bs.history, backend.Message{Role: "assistant", Content: replySB.String()})
				if bs.title == "" && userText != "" {
					t := strings.TrimSpace(strings.SplitN(userText, "\n", 2)[0])
					if len(t) > 64 {
						t = t[:64] + "..."
					}
					bs.title = t
				}
				bs.updatedAt = time.Now()
				s.sessMu.Unlock()
				s.saveSession(bs)
				replyOK(msgID, map[string]any{"stopReason": "end_turn"})
			}()

		case "session/cancel":
			var p struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			s.sessMu.Lock()
			bs, ok := s.sess[p.SessionID]
			s.sessMu.Unlock()
			if ok {
				bs.promptMu.Lock()
				if bs.cancel != nil {
					bs.cancel()
				}
				bs.promptMu.Unlock()
			}

		default:
			if len(msg.ID) > 0 {
				replyErr(msg.ID, -32601, "method not found: "+msg.Method)
			}
		}
	}

	// Wait for any in-flight prompts to complete before closing stdout.
	promptWG.Wait()
	serveCancel()
	prepareWG.Wait()
	return scanner.Err()
}

func (s *Server) watchConfig(ctx context.Context, dataDir string, notify func(string, any)) {
	cfgPath := config.ConfigPath(dataDir)
	var lastMod time.Time
	if st, err := os.Stat(cfgPath); err == nil {
		lastMod = st.ModTime()
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			st, err := os.Stat(cfgPath)
			if err != nil {
				continue
			}
			if !st.ModTime().After(lastMod) {
				continue
			}
			lastMod = st.ModTime()
			newCfg, _, err := config.Load(dataDir)
			if err != nil || newCfg == nil {
				continue
			}
			if err := s.gw.ReloadConfig(newCfg); err != nil {
				continue
			}
			s.sessMu.Lock()
			sessions := make([]*bridgeSession, 0, len(s.sess))
			for _, bs := range s.sess {
				sessions = append(sessions, bs)
			}
			s.sessMu.Unlock()
			for _, bs := range sessions {
				s.sessMu.Lock()
				curM, curE, curMode := bs.model, bs.effort, bs.mode
				s.sessMu.Unlock()
				opts, _, _, resolvedEffort := s.buildModelOptions(curM, curE, curMode)
				s.sessMu.Lock()
				bs.effort = resolvedEffort
				s.sessMu.Unlock()
				notify("session/update", map[string]any{
					"sessionId": bs.id,
					"update": map[string]any{
						"sessionUpdate": "config_option_update",
						"configOptions": opts,
					},
				})
			}
		}
	}
}

func (s *Server) buildModelOptions(selectedModel, selectedEffort, selectedMode string) ([]map[string]any, []map[string]string, string, string) {
	models := s.gw.Router().ListModels(false)
	opts := make([]map[string]string, 0, len(models)+1)
	availModels := make([]map[string]string, 0, len(models)+1)
	defaultModel := ""
	hasSelected := false
	var selectedEfforts []string
	for _, m := range models {
		if defaultModel == "" {
			defaultModel = m.ID
		}
		if m.ID == selectedModel {
			hasSelected = true
			selectedEfforts = m.Efforts
		}
		label := router.Label(m)
		opts = append(opts, map[string]string{"value": m.ID, "name": label})
		availModels = append(availModels, map[string]string{"modelId": m.ID, "name": label})
	}
	cur := selectedModel
	if cur == "" {
		cur = defaultModel
		for _, m := range models {
			if m.ID == cur {
				selectedEfforts = m.Efforts
				break
			}
		}
	} else if !hasSelected {
		opts = append(opts, map[string]string{"value": cur, "name": cur})
		availModels = append(availModels, map[string]string{"modelId": cur, "name": cur})
	}

	configOpts := []map[string]any{
		{
			"id":           "model",
			"name":         "Model",
			"category":     "model",
			"type":         "select",
			"currentValue": cur,
			"options":      opts,
		},
	}

	// Expose ACP v1 `thought_level` configOption when the selected model has effort levels.
	// Omit synthetic "default" so the host dropdown shows only real thinking levels.
	resolvedEffort := router.NormalizeEffort(selectedEffort)
	if len(selectedEfforts) > 0 {
		effortOpts := make([]map[string]string, 0, len(selectedEfforts))
		effortValid := false
		for _, eff := range selectedEfforts {
			if eff == resolvedEffort {
				effortValid = true
			}
			effortOpts = append(effortOpts, map[string]string{
				"value": eff,
				"name":  formatEffortLabel(eff),
			})
		}
		if !effortValid {
			resolvedEffort = pickDefaultEffort(selectedEfforts)
		}
		configOpts = append(configOpts, map[string]any{
			"id":           "thought_level",
			"name":         "Thinking Effort",
			"category":     "thought_level",
			"type":         "select",
			"currentValue": resolvedEffort,
			"options":      effortOpts,
		})
	} else {
		resolvedEffort = ""
	}

	// The ACP v1 `mode` configOption is only shown when [acp] expose_modes is on.
	if s.exposeModes() {
		configOpts = append(configOpts, map[string]any{
			"id":           "mode",
			"name":         "Mode",
			"category":     "mode",
			"type":         "select",
			"currentValue": normalizeBridgeMode(selectedMode),
			"options":      bridgeModeOptions(),
		})
	}

	return configOpts, availModels, cur, resolvedEffort
}

// refreshOptions rebuilds a session's config options and stores the effort they
// resolve to.
func (s *Server) refreshOptions(sessionID, model, effort, mode string) []map[string]any {
	configOpts, _, _, resolvedEffort := s.buildModelOptions(model, effort, mode)
	s.sessMu.Lock()
	defer s.sessMu.Unlock()
	if bs, ok := s.sess[sessionID]; ok {
		bs.effort = resolvedEffort
	}
	return configOpts
}

// exposeModes reports whether apx shows its own Mode menu to the ACP host.
func (s *Server) exposeModes() bool {
	if s.gw == nil {
		return false
	}
	cfg := s.gw.Config()
	return cfg != nil && cfg.ACP.ExposeModes
}

func (s *Server) hostTools() bool {
	if s.gw == nil {
		return false
	}
	cfg := s.gw.Config()
	return cfg != nil && !cfg.ACP.DisableHostMCP
}

// defaultMode is the mode a session starts in. With modes hidden it is
// "default", which is not sent to the agent: until the host sets a mode, each
// account's permission_mode decides how permission requests are answered.
func (s *Server) defaultMode() string {
	if s.exposeModes() {
		return "auto"
	}
	return "default"
}

// sessionReply is the result of session/new, load, resume and fork.
func (s *Server) sessionReply(sessionID, model string, configOpts []map[string]any, availModels []map[string]string, mode string) map[string]any {
	return s.withModes(map[string]any{
		"sessionId":     sessionID,
		"configOptions": configOpts,
		"models": map[string]any{
			"currentModelId":  model,
			"availableModels": availModels,
		},
	}, mode)
}

// withModes adds the ACP `modes` block to a session reply when modes are shown.
func (s *Server) withModes(reply map[string]any, current string) map[string]any {
	if s.exposeModes() {
		reply["modes"] = map[string]any{
			"currentModeId":  current,
			"availableModes": bridgeAvailableModes(),
		}
	}
	return reply
}

func pickDefaultEffort(efforts []string) string {
	for _, pref := range []string{"medium", "high"} {
		for _, e := range efforts {
			if e == pref {
				return e
			}
		}
	}
	if len(efforts) > 0 {
		return efforts[0]
	}
	return ""
}

func normalizeBridgeMode(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "default", "approval-required", "manual":
		return "default"
	case "acceptedits", "accept-edits", "auto-accept-edits":
		return "acceptEdits"
	case "bypasspermissions", "bypass", "full-access", "full_access":
		return "bypassPermissions"
	case "plan":
		return "plan"
	case "auto", "":
		return "auto"
	default:
		return strings.TrimSpace(raw)
	}
}

func bridgeModeToPermission(mode string) string {
	switch normalizeBridgeMode(mode) {
	case "bypassPermissions", "auto", "acceptEdits":
		return config.PermAutoApprove
	case "plan":
		return config.PermDeny
	default:
		return ""
	}
}

func bridgeAvailableModes() []map[string]string {
	return []map[string]string{
		{"id": "default", "name": "Manual", "description": "Forward permission prompts to host"},
		{"id": "acceptEdits", "name": "Accept edits", "description": "Auto-approve file edits"},
		{"id": "auto", "name": "Auto", "description": "Auto-approve tool permissions"},
		{"id": "bypassPermissions", "name": "Full access", "description": "Auto-approve all permissions"},
		{"id": "plan", "name": "Plan", "description": "Read-only planning mode"},
	}
}

func bridgeModeOptions() []map[string]string {
	return []map[string]string{
		{"value": "default", "name": "Manual"},
		{"value": "acceptEdits", "name": "Accept edits"},
		{"value": "auto", "name": "Auto"},
		{"value": "bypassPermissions", "name": "Full access"},
		{"value": "plan", "name": "Plan"},
	}
}

func (s *Server) newSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err == nil {
		return fmt.Sprintf("apx-sess-%s-%d", hex.EncodeToString(b[:]), s.nextID.Add(1))
	}
	return fmt.Sprintf("apx-sess-%d", s.nextID.Add(1))
}

func (s *Server) sessionsDir() string {
	if s.gw == nil || s.gw.DataDir() == "" {
		return ""
	}
	return filepath.Join(s.gw.DataDir(), "sessions")
}

func (s *Server) saveSession(bs *bridgeSession) {
	dir := s.sessionsDir()
	if dir == "" || bs == nil || bs.id == "" {
		return
	}
	_ = os.MkdirAll(dir, 0o700)
	s.sessMu.Lock()
	ps := persistedSession{
		ID:        bs.id,
		Cwd:       bs.cwd,
		Model:     bs.model,
		Effort:    bs.effort,
		Mode:      bs.mode,
		HostMode:  bs.hostMode,
		Title:     bs.title,
		UpdatedAt: bs.updatedAt,
		History:   append([]backend.Message(nil), bs.history...),
	}
	s.sessMu.Unlock()
	if ps.UpdatedAt.IsZero() {
		ps.UpdatedAt = time.Now()
	}
	raw, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		return
	}
	safe := filepath.Base(bs.id)
	target := filepath.Join(dir, safe+".json")
	tmp := target + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, target)
	}
}

func (s *Server) loadOrGetSession(sid string) (*bridgeSession, bool) {
	if sid == "" {
		return nil, false
	}
	s.sessMu.Lock()
	if bs, ok := s.sess[sid]; ok {
		s.sessMu.Unlock()
		return bs, true
	}
	s.sessMu.Unlock()

	dir := s.sessionsDir()
	if dir == "" {
		return nil, false
	}
	raw, err := os.ReadFile(filepath.Join(dir, filepath.Base(sid)+".json"))
	if err != nil {
		return nil, false
	}
	var ps persistedSession
	if json.Unmarshal(raw, &ps) != nil || ps.ID == "" {
		return nil, false
	}
	bs := &bridgeSession{
		id:        ps.ID,
		cwd:       ps.Cwd,
		model:     ps.Model,
		effort:    ps.Effort,
		mode:      ps.Mode,
		hostMode:  ps.HostMode,
		title:     ps.Title,
		updatedAt: ps.UpdatedAt,
		history:   ps.History,
	}
	s.sessMu.Lock()
	s.sess[bs.id] = bs
	s.sessMu.Unlock()
	return bs, true
}

func (s *Server) listPersistedSessions() []map[string]any {
	byID := make(map[string]persistedSession)
	if dir := s.sessionsDir(); dir != "" {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
					continue
				}
				raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
				if err != nil {
					continue
				}
				var ps persistedSession
				if json.Unmarshal(raw, &ps) == nil && ps.ID != "" {
					byID[ps.ID] = ps
				}
			}
		}
	}
	s.sessMu.Lock()
	for _, bs := range s.sess {
		byID[bs.id] = persistedSession{
			ID:        bs.id,
			Cwd:       bs.cwd,
			Model:     bs.model,
			Title:     bs.title,
			UpdatedAt: bs.updatedAt,
		}
	}
	s.sessMu.Unlock()

	items := make([]persistedSession, 0, len(byID))
	for _, ps := range byID {
		items = append(items, ps)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	out := make([]map[string]any, 0, len(items))
	for _, ps := range items {
		out = append(out, map[string]any{
			"sessionId": ps.ID,
			"cwd":       ps.Cwd,
			"title":     ps.Title,
			"updatedAt": ps.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return out
}

func formatEffortLabel(eff string) string {
	switch eff {
	case "minimal":
		return "Minimal"
	case "low":
		return "Low"
	case "medium":
		return "Medium"
	case "high":
		return "High"
	case "xhigh":
		return "Extra High"
	case "max":
		return "Max"
	case "ultra":
		return "Ultra"
	default:
		return eff
	}
}
