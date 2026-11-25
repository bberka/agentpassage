package acpbackend

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/logbuf"
)

type PermissionHandler func(ctx context.Context, req backend.PermissionRequest) backend.PermissionDecision

// Manager manages isolated ACP child processes per account.
type Manager struct {
	mu           sync.Mutex
	dataDir      string
	registry     *descriptors.Registry
	logs         *logbuf.Buffer
	toolActivity string
	permHandler  PermissionHandler
	workers      map[string]*worker
}

func NewManager(dataDir string, reg *descriptors.Registry, logs *logbuf.Buffer, toolActivity string) *Manager {
	if toolActivity == "" {
		toolActivity = config.ToolActivityDrop
	}
	return &Manager{
		dataDir:      dataDir,
		registry:     reg,
		logs:         logs,
		toolActivity: toolActivity,
		workers:      make(map[string]*worker),
	}
}

func (m *Manager) SetToolActivity(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mode == config.ToolActivityText || mode == config.ToolActivityDrop {
		m.toolActivity = mode
	}
}

func (m *Manager) SetPermissionHandler(fn PermissionHandler) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.permHandler = fn
}

func (m *Manager) SetRegistry(reg *descriptors.Registry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registry = reg
}

func (m *Manager) Close() {
	m.mu.Lock()
	ws := make([]*worker, 0, len(m.workers))
	for _, w := range m.workers {
		ws = append(ws, w)
	}
	m.workers = make(map[string]*worker)
	m.mu.Unlock()

	for _, w := range ws {
		w.close()
	}
}

func (m *Manager) StopAccount(accountID string) {
	m.mu.Lock()
	w, ok := m.workers[accountID]
	delete(m.workers, accountID)
	m.mu.Unlock()
	if ok && w != nil {
		w.close()
	}
}

// LoginCommand returns the exec.Cmd to run an account's interactive login in its isolated profile dir (or host profile if configured).
func (m *Manager) LoginCommand(acct config.AccountConfig) (*exec.Cmd, string, error) {
	desc, ok := m.registry.GetACP(acct.Backend)
	if !ok {
		return nil, "", fmt.Errorf("unknown ACP backend %q", acct.Backend)
	}
	profileDir := ""
	var extraEnv []string
	if acct.UseHostProfile {
		for k, v := range acct.CustomEnv {
			extraEnv = append(extraEnv, k+"="+v)
		}
	} else {
		var err error
		profileDir, err = config.EnsureProfileDir(m.dataDir, acct.Backend, acct.Name)
		if err != nil {
			return nil, "", err
		}
		extraEnv = desc.ExpandEnv(profileDir, acct.CustomEnv)
	}
	loginArgs := desc.ExpandLogin(profileDir, acct.CustomLogin)
	if len(loginArgs) == 0 {
		return nil, profileDir, fmt.Errorf("backend %q has no login command configured", acct.Backend)
	}
	cmd := exec.Command(loginArgs[0], loginArgs[1:]...)
	cmd.Env = sanitizeChildEnv(os.Environ(), extraEnv, acct)
	return cmd, profileDir, nil
}

// TestAccount launches the ACP worker, runs initialize + session/new, and returns discovered models.
func (m *Manager) TestAccount(ctx context.Context, acct config.AccountConfig) ([]string, error) {
	models, _, err := m.TestAccountWithLabels(ctx, acct)
	return models, err
}

// TestAccountWithLabels launches the ACP worker, runs initialize + session/new, and returns
// both discovered model IDs and upstream human labels (e.g. "fable" -> "Fable 5.2").
func (m *Manager) TestAccountWithLabels(ctx context.Context, acct config.AccountConfig) ([]string, map[string]string, error) {
	w, err := m.getOrStartWorker(ctx, acct)
	if err != nil {
		return nil, nil, err
	}
	cwd := acct.DefaultCwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	testCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	sess, err := w.newSession(testCtx, cwd)
	if err != nil {
		return nil, nil, err
	}
	return sess.availableModels, sess.modelLabels, nil
}

func (m *Manager) Complete(ctx context.Context, acct config.AccountConfig, req backend.ChatRequest) (*backend.ChatResponse, error) {
	var sb strings.Builder
	var sessionID string
	var finishReason string
	err := m.Stream(ctx, acct, req, func(ch backend.StreamChunk) error {
		if ch.Delta != "" {
			sb.WriteString(ch.Delta)
		}
		if ch.SessionID != "" {
			sessionID = ch.SessionID
		}
		if ch.FinishReason != "" {
			finishReason = ch.FinishReason
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if finishReason == "" {
		finishReason = "stop"
	}
	return &backend.ChatResponse{
		ID:           "acp_" + fmt.Sprintf("%d", time.Now().UnixNano()),
		Model:        req.Model,
		Content:      sb.String(),
		FinishReason: finishReason,
		ToolsIgnored: req.HasTools,
		SessionID:    sessionID,
	}, nil
}

func (m *Manager) Stream(ctx context.Context, acct config.AccountConfig, req backend.ChatRequest, cb backend.StreamCallback) error {
	w, err := m.getOrStartWorker(ctx, acct)
	if err != nil {
		return err
	}

	cwd := strings.TrimSpace(req.Cwd)
	if cwd == "" {
		cwd = strings.TrimSpace(acct.DefaultCwd)
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	sess, reused, err := w.acquireSession(ctx, req.SessionKey, cwd, req.UpstreamModel)
	if err != nil {
		return err
	}

	promptText := formatPrompt(req.Messages, reused)

	m.mu.Lock()
	toolMode := m.toolActivity
	m.mu.Unlock()

	var cbErr error
	var cbMu sync.Mutex
	unregister := w.registerStream(sess.id, func(update json.RawMessage) {
		delta := extractUpdateDelta(update, toolMode)
		if delta == "" {
			return
		}
		cbMu.Lock()
		defer cbMu.Unlock()
		if cbErr != nil {
			return
		}
		if err := cb(backend.StreamChunk{
			Delta:        delta,
			ToolsIgnored: req.HasTools,
			SessionID:    sess.id,
		}); err != nil {
			cbErr = err
		}
	})
	defer unregister()

	resRaw, err := w.call(ctx, "session/prompt", sessionPromptParams{
		SessionID: sess.id,
		Prompt: []promptBlock{
			{Type: "text", Text: promptText},
		},
	})
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			_ = w.notify("session/cancel", sessionCancelParams{SessionID: sess.id})
			return ctx.Err()
		}
		return classifyACPError(err)
	}

	cbMu.Lock()
	firstErr := cbErr
	cbMu.Unlock()
	if firstErr != nil {
		return firstErr
	}

	var res sessionPromptResult
	_ = json.Unmarshal(resRaw, &res)
	finish := "stop"
	switch strings.ToLower(res.StopReason) {
	case "max_tokens", "length":
		finish = "length"
	case "cancelled":
		finish = "stop"
	}
	return cb(backend.StreamChunk{
		FinishReason: finish,
		ToolsIgnored: req.HasTools,
		SessionID:    sess.id,
	})
}

func (m *Manager) getOrStartWorker(ctx context.Context, acct config.AccountConfig) (*worker, error) {
	m.mu.Lock()
	if existing, ok := m.workers[acct.ID]; ok && existing.alive() {
		existing.setAccount(acct)
		m.mu.Unlock()
		return existing, nil
	}
	desc, ok := m.registry.GetACP(acct.Backend)
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("unknown ACP backend %q", acct.Backend)
	}

	var profileDir string
	var argv []string
	var extraEnv []string
	if acct.UseHostProfile {
		if len(acct.CustomCommand) > 0 {
			argv = append([]string(nil), acct.CustomCommand...)
		} else {
			argv = append([]string(nil), desc.Command...)
		}
		for k, v := range acct.CustomEnv {
			extraEnv = append(extraEnv, k+"="+v)
		}
	} else {
		var err error
		profileDir, err = config.EnsureProfileDir(m.dataDir, acct.Backend, acct.Name)
		if err != nil {
			return nil, err
		}
		// Ensure standard subdirs used by profile flags/env exist.
		_ = os.MkdirAll(filepath.Join(profileDir, ".gemini"), 0o700)
		_ = os.MkdirAll(filepath.Join(profileDir, ".config"), 0o700)

		argv = desc.ExpandCommand(profileDir, acct.CustomCommand)
		extraEnv = desc.ExpandEnv(profileDir, acct.CustomEnv)
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("ACP backend %q has no command configured", acct.Backend)
	}

	w, err := startWorker(ctx, acct, argv, extraEnv, m)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	if old, ok := m.workers[acct.ID]; ok && old != w {
		old.close()
	}
	m.workers[acct.ID] = w
	m.mu.Unlock()
	return w, nil
}

// --- ACP stdio worker ---

type acpSession struct {
	id              string
	cwd             string
	modelConfigID   string
	useSetModel     bool
	currentModel    string
	availableModels []string
	modelLabels     map[string]string
}

type worker struct {
	mgr     *Manager
	acctMu  sync.RWMutex
	acct    config.AccountConfig
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeMu sync.Mutex

	nextID atomic.Int64

	pendingMu sync.Mutex
	pending   map[string]chan rpcMessage

	streamMu sync.RWMutex
	streams  map[string]func(json.RawMessage)

	sessMu   sync.Mutex
	sessions map[string]*acpSession

	closed atomic.Bool
	doneCh chan struct{}
}

func startWorker(ctx context.Context, acct config.AccountConfig, argv []string, extraEnv []string, mgr *Manager) (*worker, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = sanitizeChildEnv(os.Environ(), extraEnv, acct)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		return nil, &backend.BackendError{
			Kind:    backend.ErrKindTransient,
			Message: fmt.Sprintf("failed to launch %s: %v", argv[0], err),
			Cause:   err,
		}
	}

	w := &worker{
		mgr:      mgr,
		acct:     acct,
		cmd:      cmd,
		stdin:    stdin,
		pending:  make(map[string]chan rpcMessage),
		streams:  make(map[string]func(json.RawMessage)),
		sessions: make(map[string]*acpSession),
		doneCh:   make(chan struct{}),
	}

	go w.readStdout(stdout)
	go w.readStderr(stderr)
	go func() {
		_ = cmd.Wait()
		w.markClosed()
	}()

	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, err = w.call(initCtx, "initialize", initializeParams{
		ProtocolVersion: 1,
		ClientInfo: clientInfo{
			Name:    "apx",
			Version: "0.1.0",
		},
		ClientCapabilities: map[string]any{},
	})
	if err != nil {
		w.close()
		return nil, classifyACPError(fmt.Errorf("ACP initialize failed: %w", err))
	}

	if mgr.logs != nil {
		mgr.logs.Info("acp", fmt.Sprintf("started ACP worker for %s (%s)", acct.Name, argv[0]))
	}
	return w, nil
}

func (w *worker) setAccount(acct config.AccountConfig) {
	w.acctMu.Lock()
	defer w.acctMu.Unlock()
	w.acct = acct
}

func (w *worker) account() config.AccountConfig {
	w.acctMu.RLock()
	defer w.acctMu.RUnlock()
	return w.acct
}

func (w *worker) alive() bool {
	return !w.closed.Load()
}

func (w *worker) markClosed() {
	if w.closed.Swap(true) {
		return
	}
	close(w.doneCh)
	w.pendingMu.Lock()
	for id, ch := range w.pending {
		delete(w.pending, id)
		close(ch)
	}
	w.pendingMu.Unlock()
}

func (w *worker) close() {
	if w.closed.Load() {
		return
	}
	_ = w.stdin.Close()
	if w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
	}
	w.markClosed()
}

func (w *worker) writeJSON(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	if w.closed.Load() {
		return errors.New("ACP worker closed")
	}
	_, err = w.stdin.Write(raw)
	return err
}

func (w *worker) notify(method string, params any) error {
	pRaw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return w.writeJSON(rpcMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  pRaw,
	})
}

func (w *worker) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := fmt.Sprintf("%d", w.nextID.Add(1))
	idRaw := json.RawMessage(id)
	pRaw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}

	ch := make(chan rpcMessage, 1)
	w.pendingMu.Lock()
	if w.closed.Load() {
		w.pendingMu.Unlock()
		return nil, errors.New("ACP worker exited")
	}
	w.pending[id] = ch
	w.pendingMu.Unlock()

	if err := w.writeJSON(rpcMessage{
		JSONRPC: "2.0",
		ID:      idRaw,
		Method:  method,
		Params:  pRaw,
	}); err != nil {
		w.pendingMu.Lock()
		delete(w.pending, id)
		w.pendingMu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		w.pendingMu.Lock()
		delete(w.pending, id)
		w.pendingMu.Unlock()
		return nil, ctx.Err()
	case <-w.doneCh:
		return nil, errors.New("ACP worker exited unexpectedly")
	case resp, ok := <-ch:
		if !ok {
			return nil, errors.New("ACP worker closed")
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("RPC error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (w *worker) registerStream(sessionID string, fn func(json.RawMessage)) func() {
	w.streamMu.Lock()
	w.streams[sessionID] = fn
	w.streamMu.Unlock()
	return func() {
		w.streamMu.Lock()
		delete(w.streams, sessionID)
		w.streamMu.Unlock()
	}
}

func (w *worker) readStdout(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg rpcMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}

		// Response to one of our outgoing calls.
		if msg.Method == "" && len(msg.ID) > 0 {
			key := normalizeRPCID(msg.ID)
			w.pendingMu.Lock()
			ch, ok := w.pending[key]
			delete(w.pending, key)
			w.pendingMu.Unlock()
			if ok {
				ch <- msg
			}
			continue
		}

		// Notification or server->client request.
		switch msg.Method {
		case "session/update":
			var p sessionUpdateParams
			if err := json.Unmarshal(msg.Params, &p); err == nil {
				w.streamMu.RLock()
				fn := w.streams[p.SessionID]
				w.streamMu.RUnlock()
				if fn != nil {
					fn(p.Update)
				}
			}

		case "session/request_permission":
			go w.handlePermission(msg)

		default:
			// If it's an unknown request with an ID, reply with method not found so agent doesn't hang.
			if len(msg.ID) > 0 {
				_ = w.writeJSON(rpcMessage{
					JSONRPC: "2.0",
					ID:      msg.ID,
					Error:   &rpcError{Code: -32601, Message: "method not supported by apx"},
				})
			}
		}
	}
	w.markClosed()
}

func (w *worker) readStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if w.mgr.logs != nil {
			w.mgr.logs.Add(logbuf.Entry{
				Level:   logbuf.LevelInfo,
				Kind:    "acp",
				Account: w.account().Name,
				Message: line,
			})
		}
	}
}

func (w *worker) handlePermission(msg rpcMessage) {
	var p permissionParams
	_ = json.Unmarshal(msg.Params, &p)
	acct := w.account()

	opts := make([]backend.PermissionOption, 0, len(p.Options))
	for _, o := range p.Options {
		id := o.OptionID
		if id == "" {
			id = o.ID
		}
		opts = append(opts, backend.PermissionOption{
			ID:   id,
			Name: o.Name,
			Kind: o.Kind,
		})
	}

	pickOption := func(approve bool) permissionOutcome {
		for _, o := range opts {
			lowerID := strings.ToLower(o.ID)
			lowerKind := strings.ToLower(o.Kind)
			lowerName := strings.ToLower(o.Name)
			if approve {
				if strings.Contains(lowerKind, "allow") || strings.Contains(lowerID, "allow") || strings.Contains(lowerName, "allow") {
					return permissionOutcome{Outcome: "selected", OptionID: o.ID}
				}
			} else {
				if strings.Contains(lowerKind, "reject") || strings.Contains(lowerID, "reject") || strings.Contains(lowerID, "deny") || strings.Contains(lowerName, "reject") || strings.Contains(lowerName, "deny") {
					return permissionOutcome{Outcome: "selected", OptionID: o.ID}
				}
			}
		}
		if approve && len(opts) > 0 {
			return permissionOutcome{Outcome: "selected", OptionID: opts[0].ID}
		}
		return permissionOutcome{Outcome: "cancelled"}
	}

	var outcome permissionOutcome
	switch acct.PermissionMode {
	case config.PermAutoApprove:
		outcome = pickOption(true)
	case config.PermAsk:
		w.mgr.mu.Lock()
		handler := w.mgr.permHandler
		w.mgr.mu.Unlock()
		if handler != nil {
			args := string(p.ToolCall.Arguments)
			if args == "" {
				args = string(p.ToolCall.RawInput)
			}
			req := backend.PermissionRequest{
				ID:          normalizeRPCID(msg.ID),
				AccountID:   acct.ID,
				AccountName: acct.Name,
				SessionID:   p.SessionID,
				Title:       p.ToolCall.Title,
				ToolName:    p.ToolCall.Name,
				Arguments:   args,
				Options:     opts,
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			dec := handler(ctx, req)
			cancel()
			if dec.OptionID != "" {
				outcome = permissionOutcome{Outcome: "selected", OptionID: dec.OptionID}
			} else {
				outcome = pickOption(dec.Approved)
			}
		} else {
			outcome = pickOption(false)
		}
	default: // PermDeny
		outcome = pickOption(false)
	}

	resBytes, _ := json.Marshal(permissionResult{Outcome: outcome})
	_ = w.writeJSON(rpcMessage{
		JSONRPC: "2.0",
		ID:      msg.ID,
		Result:  resBytes,
	})
}

func (w *worker) newSession(ctx context.Context, cwd string) (*acpSession, error) {
	resRaw, err := w.call(ctx, "session/new", sessionNewParams{
		Cwd:        cwd,
		MCPServers: []any{},
	})
	if err != nil {
		return nil, classifyACPError(err)
	}
	var res sessionNewResult
	if err := json.Unmarshal(resRaw, &res); err != nil {
		return nil, fmt.Errorf("decode session/new response: %w", err)
	}
	if res.SessionID == "" {
		return nil, fmt.Errorf("ACP agent returned empty sessionId")
	}

	s := &acpSession{
		id:          res.SessionID,
		cwd:         cwd,
		modelLabels: make(map[string]string),
	}
	seenModels := make(map[string]bool)
	addModel := func(id, name string) {
		id = strings.TrimSpace(id)
		if id != "" && !seenModels[id] {
			seenModels[id] = true
			s.availableModels = append(s.availableModels, id)
		}
		name = strings.TrimSpace(name)
		if id != "" && name != "" && name != id && !strings.HasPrefix(strings.ToLower(name), "default (") {
			s.modelLabels[id] = name
		}
	}
	var collectOptionValues func(opts []configOptionValue)
	collectOptionValues = func(opts []configOptionValue) {
		for _, v := range opts {
			if len(v.Options) > 0 {
				collectOptionValues(v.Options)
			}
			if v.Value != "" {
				addModel(v.Value, v.Name)
			} else if v.ID != "" {
				addModel(v.ID, v.Name)
			}
		}
	}

	for _, opt := range res.ConfigOptions {
		if opt.Category == "model" || opt.ID == "model" {
			s.modelConfigID = opt.ID
			s.currentModel = opt.CurrentValue
			collectOptionValues(opt.Options)
			break
		}
	}
	if res.Models != nil {
		if s.modelConfigID == "" {
			s.useSetModel = true
		}
		if s.currentModel == "" {
			s.currentModel = res.Models.CurrentModelID
		}
		for _, m := range res.Models.AvailableModels {
			if m.ModelID != "" {
				addModel(m.ModelID, m.Name)
			} else if m.ID != "" {
				addModel(m.ID, m.Name)
			}
		}
	}
	return s, nil
}

func (w *worker) acquireSession(ctx context.Context, sessionKey, cwd, upstreamModel string) (*acpSession, bool, error) {
	w.sessMu.Lock()
	var sess *acpSession
	reused := false
	if sessionKey != "" {
		if existing, ok := w.sessions[sessionKey]; ok && existing.cwd == cwd {
			sess = existing
			reused = true
		}
	}
	w.sessMu.Unlock()

	if sess == nil {
		var err error
		sess, err = w.newSession(ctx, cwd)
		if err != nil {
			return nil, false, err
		}
		if sessionKey != "" {
			w.sessMu.Lock()
			w.sessions[sessionKey] = sess
			w.sessMu.Unlock()
		}
	}

	if upstreamModel != "" && upstreamModel != "default" && upstreamModel != sess.currentModel {
		if sess.modelConfigID != "" {
			if _, err := w.call(ctx, "session/set_config_option", setConfigOptionParams{
				SessionID: sess.id,
				ConfigID:  sess.modelConfigID,
				Value:     upstreamModel,
			}); err == nil {
				sess.currentModel = upstreamModel
			}
		} else if sess.useSetModel {
			if _, err := w.call(ctx, "session/set_model", setModelParams{
				SessionID: sess.id,
				ModelID:   upstreamModel,
			}); err == nil {
				sess.currentModel = upstreamModel
			}
		}
	}
	return sess, reused, nil
}

func normalizeRPCID(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	return strings.Trim(s, `"`)
}

func formatPrompt(messages []backend.Message, reusedSession bool) string {
	if len(messages) == 0 {
		return ""
	}
	if reusedSession {
		// Session already has earlier turns; send the most recent user message.
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				return messages[i].Content
			}
		}
		return messages[len(messages)-1].Content
	}
	if len(messages) == 1 {
		return messages[0].Content
	}
	var sb strings.Builder
	for _, m := range messages {
		switch m.Role {
		case "system":
			sb.WriteString("[System]\n")
			sb.WriteString(m.Content)
			sb.WriteString("\n\n")
		case "assistant":
			sb.WriteString("[Assistant]\n")
			sb.WriteString(m.Content)
			sb.WriteString("\n\n")
		default:
			sb.WriteString("[User]\n")
			sb.WriteString(m.Content)
			sb.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(sb.String())
}

func extractUpdateDelta(raw json.RawMessage, toolMode string) string {
	var u struct {
		SessionUpdate string          `json:"sessionUpdate"`
		Kind          string          `json:"kind"`
		Title         string          `json:"title"`
		Status        string          `json:"status"`
		Content       json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return ""
	}
	kind := u.SessionUpdate
	if kind == "" {
		kind = u.Kind
	}

	switch kind {
	case "agent_message_chunk":
		return extractTextFromContent(u.Content)
	case "tool_call":
		if toolMode == config.ToolActivityText && u.Title != "" {
			return fmt.Sprintf("\n[tool: %s]\n", u.Title)
		}
	case "tool_call_update":
		if toolMode == config.ToolActivityText && u.Title != "" && u.Status != "" {
			return fmt.Sprintf("\n[tool %s: %s]\n", u.Status, u.Title)
		}
	}
	return ""
}

func extractTextFromContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Official ACP v1: content: { "type": "text", "text": "..." }
	var single struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &single); err == nil {
		if single.Text != "" {
			return single.Text
		}
		if len(single.Content) > 0 {
			var sb strings.Builder
			for _, b := range single.Content {
				if b.Text != "" {
					sb.WriteString(b.Text)
				}
			}
			return sb.String()
		}
	}
	// Array of content blocks.
	var arr []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil {
		var sb strings.Builder
		for _, b := range arr {
			if b.Text != "" {
				sb.WriteString(b.Text)
			}
		}
		return sb.String()
	}
	return ""
}

func classifyACPError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := backend.AsBackendError(err); ok {
		return err
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "restricted") || strings.Contains(lower, "suspended") || strings.Contains(lower, "banned"):
		return &backend.BackendError{Kind: backend.ErrKindRestricted, StatusCode: 403, Message: msg, Cause: err}
	case strings.Contains(lower, "429") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "rate_limit") || strings.Contains(lower, "too many requests") || strings.Contains(lower, "quota") || strings.Contains(lower, "usage limit") || strings.Contains(lower, "resource_exhausted"):
		return &backend.BackendError{
			Kind:       backend.ErrKindRateLimit,
			StatusCode: 429,
			Message:    msg,
			RetryAfter: backend.ParseResetDurationSeconds(msg),
			Cause:      err,
		}
	case strings.Contains(lower, "401") || strings.Contains(lower, "403") || strings.Contains(lower, "unauthenticated") || strings.Contains(lower, "not logged in") || strings.Contains(lower, "login required") || strings.Contains(lower, "auth"):
		return &backend.BackendError{Kind: backend.ErrKindAuth, StatusCode: 401, Message: msg, Cause: err}
	default:
		return &backend.BackendError{Kind: backend.ErrKindTransient, StatusCode: 502, Message: msg, Cause: err}
	}
}

// sanitizeChildEnv strips apx gateway loopback URLs/keys from inherited environment variables
// (preventing recursive loops when a CLI like pi, opencode, codex, or claude is both connected
// to apx and launched by apx as an upstream ACP backend), deduplicates overridden keys,
// and tags the subprocess with APX_IN_CHILD metadata.
func sanitizeChildEnv(baseEnv, extraEnv []string, acct config.AccountConfig) []string {
	overrides := make(map[string]string, len(extraEnv)+3)
	for _, kv := range extraEnv {
		if k, v, ok := strings.Cut(kv, "="); ok {
			overrides[k] = v
		}
	}
	overrides["APX_IN_CHILD"] = "1"
	overrides["APX_CHILD_BACKEND"] = acct.Backend
	overrides["APX_CHILD_ACCOUNT"] = acct.ID

	out := make([]string, 0, len(baseEnv)+len(overrides))
	for _, kv := range baseEnv {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, replaced := overrides[k]; replaced {
			continue
		}
		// Strip inherited apx gateway env vars so child ACP agents never call back into apx.
		switch k {
		case "OPENAI_BASE_URL", "ANTHROPIC_BASE_URL":
			lower := strings.ToLower(v)
			if strings.Contains(lower, "127.0.0.1") || strings.Contains(lower, "localhost") || strings.Contains(lower, "[::1]") {
				continue
			}
		case "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "APX_API_KEY", "ACPX_API_KEY":
			trimmed := strings.TrimSpace(v)
			if strings.HasPrefix(trimmed, "apx_") || strings.HasPrefix(trimmed, "acpx_") {
				continue
			}
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}
