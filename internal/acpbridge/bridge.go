package acpbridge

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bberka/acpx/internal/backend"
	"github.com/bberka/acpx/internal/gateway"
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
	id      string
	cwd     string
	model   string
	history []backend.Message
}

// Server exposes acpx itself as an ACP JSON-RPC 2.0 stdio server (`acpx acp`).
// Any ACP host (T3 Code, Zed, JetBrains, etc.) can register `acpx acp` and
// immediately select pooled rotated models or account-prefixed models.
type Server struct {
	gw      *gateway.Server
	writeMu sync.Mutex
	sessMu  sync.Mutex
	nextID  atomic.Int64
	sess    map[string]*bridgeSession
}

func New(gw *gateway.Server) *Server {
	return &Server{
		gw:   gw,
		sess: make(map[string]*bridgeSession),
	}
}

func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)

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

	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg rpcMsg
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}

		switch msg.Method {
		case "initialize":
			replyOK(msg.ID, map[string]any{
				"protocolVersion": 1,
				"agentInfo": map[string]string{
					"name":    "acpx ACP Gateway",
					"version": gateway.Version,
				},
				"agentCapabilities": map[string]any{
					"loadSession": false,
					"promptCapabilities": map[string]any{
						"image":           false,
						"audio":           false,
						"embeddedContext": true,
					},
				},
				"authMethods": []any{},
			})

		case "session/new":
			var p struct {
				Cwd string `json:"cwd"`
			}
			_ = json.Unmarshal(msg.Params, &p)

			configOpts, availModels, defaultModel := s.buildModelOptions("")

			sid := fmt.Sprintf("acpx-sess-%d", s.nextID.Add(1))
			s.sessMu.Lock()
			s.sess[sid] = &bridgeSession{
				id:    sid,
				cwd:   p.Cwd,
				model: defaultModel,
			}
			s.sessMu.Unlock()

			replyOK(msg.ID, map[string]any{
				"sessionId":     sid,
				"configOptions": configOpts,
				"models": map[string]any{
					"currentModelId":  defaultModel,
					"availableModels": availModels,
				},
			})

		case "session/set_config_option":
			var p struct {
				SessionID string `json:"sessionId"`
				ConfigID  string `json:"configId"`
				Value     string `json:"value"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			curModel := p.Value
			s.sessMu.Lock()
			if bs, ok := s.sess[p.SessionID]; ok {
				if p.ConfigID == "model" || p.ConfigID == "" {
					bs.model = p.Value
				}
				curModel = bs.model
			}
			s.sessMu.Unlock()
			configOpts, _, _ := s.buildModelOptions(curModel)
			replyOK(msg.ID, map[string]any{
				"configOptions": configOpts,
			})

		case "session/set_model":
			var p struct {
				SessionID string `json:"sessionId"`
				ModelID   string `json:"modelId"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			s.sessMu.Lock()
			if bs, ok := s.sess[p.SessionID]; ok {
				bs.model = p.ModelID
			}
			s.sessMu.Unlock()
			replyOK(msg.ID, map[string]any{})

		case "session/prompt":
			var p struct {
				SessionID string `json:"sessionId"`
				Prompt    []struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					Resource *struct {
						URI  string `json:"uri"`
						Text string `json:"text"`
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
			for _, b := range p.Prompt {
				if b.Text != "" {
					if promptSB.Len() > 0 {
						promptSB.WriteString("\n")
					}
					promptSB.WriteString(b.Text)
				} else if b.Resource != nil && b.Resource.Text != "" {
					if promptSB.Len() > 0 {
						promptSB.WriteString("\n")
					}
					if b.Resource.URI != "" {
						promptSB.WriteString("[" + b.Resource.URI + "]\n")
					}
					promptSB.WriteString(b.Resource.Text)
				}
			}
			userText := promptSB.String()
			bs.history = append(bs.history, backend.Message{Role: "user", Content: userText})

			if bs.model == "" {
				replyErr(msg.ID, -32602, "no model configured in acpx; add an account first")
				continue
			}

			var replySB strings.Builder
			_, err := s.gw.StreamDirect(ctx, backend.ChatRequest{
				Model:      bs.model,
				Messages:   append([]backend.Message(nil), bs.history...),
				Stream:     true,
				SessionKey: bs.id,
				Cwd:        bs.cwd,
			}, func(ch backend.StreamChunk) error {
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
				replyErr(msg.ID, -32000, err.Error())
				continue
			}

			bs.history = append(bs.history, backend.Message{Role: "assistant", Content: replySB.String()})
			replyOK(msg.ID, map[string]any{"stopReason": "end_turn"})

		case "session/cancel":
			// No-op ack for notification
		default:
			if len(msg.ID) > 0 {
				replyErr(msg.ID, -32601, "method not found: "+msg.Method)
			}
		}
	}
	return scanner.Err()
}

func (s *Server) buildModelOptions(selected string) ([]map[string]any, []map[string]string, string) {
	models := s.gw.Router().ListModels(false)
	opts := make([]map[string]string, 0, len(models)+1)
	availModels := make([]map[string]string, 0, len(models)+1)
	defaultModel := ""
	hasSelected := false
	for _, m := range models {
		if defaultModel == "" {
			defaultModel = m.ID
		}
		if m.ID == selected {
			hasSelected = true
		}
		label := m.ID
		if m.DisplayName != "" {
			label = m.DisplayName
		}
		opts = append(opts, map[string]string{"value": m.ID, "name": label})
		availModels = append(availModels, map[string]string{"modelId": m.ID, "name": label})
	}
	cur := selected
	if cur == "" {
		cur = defaultModel
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
	return configOpts, availModels, cur
}
