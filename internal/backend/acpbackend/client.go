package acpbackend

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	"github.com/bberka/agentpassage/internal/hostmcp"
	"github.com/bberka/agentpassage/internal/logbuf"
	"github.com/bberka/agentpassage/internal/sharing"
	"github.com/bberka/agentpassage/internal/skills"
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
	starting     sync.Map // account ID -> *sync.Mutex held while its process starts
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
	cmd.Env = backend.SanitizeChildEnv(os.Environ(), extraEnv, acct)
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
	models, labels, _, err := m.TestAccountDiscovery(ctx, acct)
	return models, labels, err
}

// TestAccountDiscovery launches the ACP worker, runs initialize + session/new, and returns
// discovered model IDs, upstream human labels, and ACP thought_level/effort options.
func (m *Manager) TestAccountDiscovery(ctx context.Context, acct config.AccountConfig) ([]string, map[string]string, map[string][]string, error) {
	w, err := m.getOrStartWorker(ctx, acct, false)
	if err != nil {
		return nil, nil, nil, err
	}
	cwd := acct.DefaultCwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	testCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	sess, err := w.newSession(testCtx, cwd, nil)
	if err != nil {
		return nil, nil, nil, err
	}
	var efforts map[string][]string
	if len(sess.availableEfforts) > 0 {
		efforts = map[string][]string{
			"*": append([]string(nil), sess.availableEfforts...),
		}
		if sess.currentModel != "" && sess.currentModel != "default" {
			efforts[sess.currentModel] = append([]string(nil), sess.availableEfforts...)
		}
	}
	return sess.availableModels, sess.modelLabels, efforts, nil
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

func (m *Manager) Stream(ctx context.Context, acct config.AccountConfig, req backend.ChatRequest, cb backend.StreamCallback) (err error) {
	w, sess, reused, err := m.openSession(ctx, acct, req)
	if err != nil {
		return err
	}
	defer func() {
		// A failed turn may be missing from the session, so a later request
		// replays the full history into a fresh one.
		w.releaseSession(req, sess, err == nil)
		if err == nil {
			backend.RecordAnswer(req.SessionKey, acct.ID)
		}
	}()

	promptText := backend.FormatPrompt(req.Messages, reused)

	m.mu.Lock()
	toolMode := m.toolActivity
	m.mu.Unlock()

	var cbErr error
	var cbMu sync.Mutex
	var quotaReply string
	unregister := w.registerStream(sess.id, func(update json.RawMessage) {
		delta, thought, passthrough := extractUpdateParts(update, toolMode)
		if delta == "" && thought == "" && !passthrough {
			return
		}
		cbMu.Lock()
		defer cbMu.Unlock()
		if acct.Backend == "agy" && isAgyQuotaReply(delta) {
			quotaReply = strings.TrimSpace(delta)
			return
		}
		if cbErr != nil {
			return
		}
		chunk := backend.StreamChunk{
			Delta:        delta,
			Reasoning:    thought,
			ToolsIgnored: req.HasTools,
			SessionID:    sess.id,
		}
		if passthrough {
			chunk.RawACPUpdate = append(json.RawMessage(nil), update...)
		}
		if err := cb(chunk); err != nil {
			cbErr = err
		}
	})
	defer unregister()

	atts := backend.PromptAttachments(req.Messages, reused)
	blocks := make([]promptBlock, 0, 1+len(atts))
	if promptText != "" || len(atts) == 0 {
		blocks = append(blocks, promptBlock{Type: "text", Text: promptText})
	}
	blocks = append(blocks, attachmentBlocks(ctx, atts, w.promptCaps)...)

	resRaw, err := w.call(ctx, "session/prompt", sessionPromptParams{
		SessionID: sess.id,
		Prompt:    blocks,
	})
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			_ = w.notify("session/cancel", sessionCancelParams{SessionID: sess.id})
			return ctx.Err()
		}
		return backend.ClassifyAgentError(err)
	}

	cbMu.Lock()
	firstErr := cbErr
	quotaMsg := quotaReply
	cbMu.Unlock()
	if quotaMsg != "" {
		return backend.ClassifyAgentError(errors.New(quotaMsg))
	}
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
	var used *backend.Usage
	if u := res.Usage; u != nil && u.InputTokens+u.OutputTokens+u.CachedReadTokens+u.CachedWriteTokens > 0 {
		prompt := u.InputTokens + u.CachedReadTokens + u.CachedWriteTokens
		used = &backend.Usage{
			PromptTokens:     prompt,
			CompletionTokens: u.OutputTokens,
			TotalTokens:      prompt + u.OutputTokens,
			CacheReadTokens:  u.CachedReadTokens,
			CacheWriteTokens: u.CachedWriteTokens,
		}
	}
	return cb(backend.StreamChunk{
		Usage:        used,
		FinishReason: finish,
		ToolsIgnored: req.HasTools,
		SessionID:    sess.id,
	})
}

// openSession returns the account's agent process and the session req runs in.
// reused says the session already holds the earlier turns.
func (m *Manager) openSession(ctx context.Context, acct config.AccountConfig, req backend.ChatRequest) (*worker, *acpSession, bool, error) {
	if acct.Backend == "pi" && !acct.UseHostProfile {
		m.dropWorkerUnless(acct.ID, req.HostTools)
	}
	w, err := m.getOrStartWorker(ctx, acct, req.HostTools)
	if err != nil {
		return nil, nil, false, err
	}

	cwd := strings.TrimSpace(req.Cwd)
	if cwd == "" {
		cwd = strings.TrimSpace(acct.DefaultCwd)
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	if backend.AnsweredElsewhere(req.SessionKey, acct.ID) {
		w.forgetSession(req.SessionKey)
	}
	mcpServers := req.MCPServers
	if !acct.UseHostProfile {
		// Each request, so skills and MCP servers added later are picked up.
		if dir, err := config.ProfileDir(m.dataDir, acct.Backend, acct.Name); err == nil {
			m.shareSkills(acct, dir)
		}
		if req.HostTools {
			mcpServers = m.withHostMCP(mcpServers, acct.Backend, cwd, w.mcpCaps)
		}
	}
	sess, reused, err := w.acquireSession(ctx, req, cwd, mcpServers)
	if err != nil {
		return nil, nil, false, err
	}
	w.listenCommands(sess.id, req.OnCommands)
	return w, sess, reused, nil
}

// withHostMCP adds the MCP servers configured in the user's own copy of the
// agent. A server the host sent wins over one with the same name.
func (m *Manager) withHostMCP(sent []any, agent, cwd string, caps hostmcp.Capabilities) []any {
	home, err := os.UserHomeDir()
	if err != nil {
		return sent
	}
	servers, err := hostmcp.Load(home, agent, cwd)
	if err != nil && m.logs != nil {
		m.logs.Warn("acp", err.Error())
	}
	return hostmcp.Merge(sent, hostmcp.Keep(servers, m.choices().Allows), caps)
}

// choices reads which tools the user shares. When that can't be read, no MCP
// server is allowed.
func (m *Manager) choices() sharing.Choices {
	c, err := sharing.Load(m.dataDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) && m.logs != nil {
		m.logs.Warn("acp", err.Error())
	}
	return c
}

// shareSkills links the user's skills for this agent into its profile, which
// hides the folders the agent would normally read them from.
func (m *Manager) shareSkills(acct config.AccountConfig, profileDir string) {
	home, err := os.UserHomeDir()
	if err == nil {
		err = skills.ShareWithProfile(acct.Backend, home, profileDir, m.choices().SkillsOff)
	}
	if err != nil && m.logs != nil {
		m.logs.Warn("acp", fmt.Sprintf("share skills with %s/%s: %v", acct.Backend, acct.Name, err))
	}
}

// getOrStartWorker returns the account's running agent process, starting one if
// needed. hostTools says whether a new process may use the user's MCP servers.
func (m *Manager) getOrStartWorker(ctx context.Context, acct config.AccountConfig, hostTools bool) (*worker, error) {
	// One start at a time per account: a second process would replace the first
	// and end its sessions.
	startLock, _ := m.starting.LoadOrStore(acct.ID, &sync.Mutex{})
	startLock.(*sync.Mutex).Lock()
	defer startLock.(*sync.Mutex).Unlock()

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
		if acct.Backend == "pi" {
			env, err := piMCPEnv(profileDir, acct.CustomEnv["PI_ACP_PI_COMMAND"], hostTools, m.choices().Allows)
			if err != nil {
				return nil, err
			}
			extraEnv = append(extraEnv, env...)
		}
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("ACP backend %q has no command configured", acct.Backend)
	}

	w, err := startWorker(ctx, acct, argv, extraEnv, m)
	if err != nil {
		return nil, err
	}
	w.hostTools = hostTools

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
	id               string
	cwd              string
	modelConfigID    string
	effortConfigID   string
	modeConfigID     string
	useSetModel      bool
	currentModel     string
	currentEffort    string
	currentMode      string
	availableModels  []string
	availableEfforts []string
	hasDefaultEffort bool
	modelLabels      map[string]string

	// Guarded by worker.sessMu.
	history  string // sum of the user turns sent, see backend.ContinueSession
	busy     bool   // a turn is running
	host     bool   // belongs to a local ACP host, which keeps it open
	fresh    bool   // no turn has run in it yet
	lastUsed time.Time
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

	sessMu          sync.Mutex
	sessions        map[string]*acpSession
	permByID        map[string]string            // upstream ACP sessionId -> per-request permissionMode override
	permHandlerByID map[string]PermissionHandler // upstream ACP sessionId -> per-request interactive permission handler

	hostTools  bool                 // set once at start
	promptCaps promptCapabilities   // set once at initialize
	mcpCaps    hostmcp.Capabilities // set once at initialize
	canClose   bool                 // set once at initialize

	cmdMu     sync.Mutex
	commands  map[string]json.RawMessage       // upstream sessionId -> latest command list
	listeners map[string]func(json.RawMessage) // upstream sessionId -> command receiver

	closed atomic.Bool
	doneCh chan struct{}
}

func startWorker(ctx context.Context, acct config.AccountConfig, argv []string, extraEnv []string, mgr *Manager) (*worker, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = backend.SanitizeChildEnv(os.Environ(), extraEnv, acct)

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
		mgr:             mgr,
		acct:            acct,
		cmd:             cmd,
		stdin:           stdin,
		pending:         make(map[string]chan rpcMessage),
		streams:         make(map[string]func(json.RawMessage)),
		sessions:        make(map[string]*acpSession),
		permByID:        make(map[string]string),
		permHandlerByID: make(map[string]PermissionHandler),
		commands:        make(map[string]json.RawMessage),
		listeners:       make(map[string]func(json.RawMessage)),
		doneCh:          make(chan struct{}),
	}

	go w.readStdout(stdout)
	go w.readStderr(stderr)
	go func() {
		_ = cmd.Wait()
		w.markClosed()
	}()

	initCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	initRaw, err := w.call(initCtx, "initialize", initializeParams{
		ProtocolVersion: 1,
		ClientInfo: clientInfo{
			Name:    "apx",
			Version: "0.1.0",
		},
		ClientCapabilities: map[string]any{},
	})
	if err != nil {
		w.close()
		return nil, backend.ClassifyAgentError(fmt.Errorf("ACP initialize failed: %w", err))
	}

	var initRes initializeResult
	_ = json.Unmarshal(initRaw, &initRes)
	w.promptCaps = initRes.AgentCapabilities.PromptCapabilities
	w.mcpCaps = initRes.AgentCapabilities.MCPCapabilities
	closeCap := string(initRes.AgentCapabilities.Session.Close)
	w.canClose = closeCap != "" && closeCap != "null" && closeCap != "false"

	if mgr.logs != nil {
		c := w.promptCaps
		mgr.logs.Info("acp", fmt.Sprintf("started ACP worker for %s (%s; accepts image=%v audio=%v files=%v)", acct.Name, argv[0], c.Image, c.Audio, c.EmbeddedContext))
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
				w.noteCommands(p.SessionID, p.Update)
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
		if id := permissionOption(opts, approve); id != "" {
			return permissionOutcome{Outcome: "selected", OptionID: id}
		}
		return permissionOutcome{Outcome: "cancelled"}
	}

	permMode := acct.PermissionMode
	var sessPermHandler PermissionHandler
	w.sessMu.Lock()
	if override := w.permByID[p.SessionID]; override != "" {
		permMode = override
	}
	sessPermHandler = w.permHandlerByID[p.SessionID]
	w.sessMu.Unlock()

	var outcome permissionOutcome
	switch permMode {
	case config.PermAutoApprove:
		outcome = pickOption(true)
	case config.PermAsk:
		handler := sessPermHandler
		if handler == nil {
			w.mgr.mu.Lock()
			handler = w.mgr.permHandler
			w.mgr.mu.Unlock()
		}
		if handler != nil {
			var tc permissionToolCallMeta
			_ = json.Unmarshal(p.ToolCall, &tc)
			args := string(tc.Arguments)
			if args == "" {
				args = string(tc.RawInput)
			}
			req := backend.PermissionRequest{
				ID:          normalizeRPCID(msg.ID),
				AccountID:   acct.ID,
				AccountName: acct.Name,
				SessionID:   p.SessionID,
				ToolCallID:  tc.ToolCallID,
				Kind:        tc.Kind,
				Title:       tc.Title,
				ToolName:    tc.Name,
				Arguments:   args,
				RawToolCall: append(json.RawMessage(nil), p.ToolCall...),
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

func (w *worker) newSession(ctx context.Context, cwd string, mcpServers []any) (*acpSession, error) {
	if mcpServers == nil {
		mcpServers = []any{}
	}
	resRaw, err := w.call(ctx, "session/new", sessionNewParams{
		Cwd:        cwd,
		MCPServers: mcpServers,
	})
	if err != nil {
		return nil, backend.ClassifyAgentError(err)
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
			continue
		}
		if isThoughtLevelOption(opt) {
			applyEffortConfigOption(s, opt)
			continue
		}
		if strings.EqualFold(opt.Category, "mode") || strings.EqualFold(opt.ID, "mode") {
			s.modeConfigID = opt.ID
			s.currentMode = strings.TrimSpace(opt.CurrentValue)
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

func isThoughtLevelOption(opt configOption) bool {
	cat := strings.ToLower(strings.TrimSpace(opt.Category))
	id := strings.ToLower(strings.TrimSpace(opt.ID))
	return cat == "thought_level" || id == "thought_level" || id == "effort" || id == "reasoning_effort"
}

func applyEffortConfigOption(s *acpSession, opt configOption) {
	s.effortConfigID = opt.ID
	s.currentEffort = strings.TrimSpace(opt.CurrentValue)
	s.availableEfforts = nil
	s.hasDefaultEffort = false
	seen := make(map[string]bool)
	var collect func(opts []configOptionValue)
	collect = func(opts []configOptionValue) {
		for _, v := range opts {
			if len(v.Options) > 0 {
				collect(v.Options)
			}
			val := strings.ToLower(strings.TrimSpace(v.Value))
			if val == "" {
				val = strings.ToLower(strings.TrimSpace(v.ID))
			}
			if val == "" {
				continue
			}
			if val == "default" {
				s.hasDefaultEffort = true
				continue
			}
			if val == "auto" || val == "none" || val == "off" {
				continue
			}
			if !seen[val] {
				seen[val] = true
				s.availableEfforts = append(s.availableEfforts, val)
			}
		}
	}
	collect(opt.Options)
}

// sessionIdleLimit is how long an API conversation may sit unused before its
// upstream session is closed.
const sessionIdleLimit = time.Hour

// forgetSession drops a conversation's upstream session.
func (w *worker) forgetSession(sessionKey string) {
	w.sessMu.Lock()
	sess := w.sessions[sessionKey]
	if sess != nil && sess.fresh {
		sess = nil // it has missed nothing
	} else {
		delete(w.sessions, sessionKey)
	}
	idle := sess != nil && !sess.busy
	w.sessMu.Unlock()
	if idle {
		w.closeSession(sess)
	}
}

// releaseSession ends a turn. The session stays only if the turn succeeded and
// it is still the one kept for the conversation.
func (w *worker) releaseSession(req backend.ChatRequest, sess *acpSession, ok bool) {
	w.sessMu.Lock()
	sess.busy = false
	kept := req.SessionKey != "" && w.sessions[req.SessionKey] == sess
	if kept && ok {
		sess.fresh = false
		sess.history, _ = backend.ContinueSession(sess.history, req)
		sess.lastUsed = time.Now()
	} else if kept {
		delete(w.sessions, req.SessionKey)
	}
	w.sessMu.Unlock()
	if !kept || !ok {
		w.closeSession(sess)
	}
}

// closeSession frees a session apx will not use again.
func (w *worker) closeSession(sess *acpSession) {
	w.sessMu.Lock()
	delete(w.permByID, sess.id)
	delete(w.permHandlerByID, sess.id)
	w.sessMu.Unlock()
	w.forgetCommands(sess.id)
	if w.canClose && w.alive() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = w.call(ctx, "session/close", map[string]string{"sessionId": sess.id})
		}()
	}
}

// takeSession returns the session to continue req in, or nil when it needs a
// new one. keep says whether that new one may be stored for the conversation.
func (w *worker) takeSession(req backend.ChatRequest, cwd string) (sess *acpSession, keep bool) {
	var closing []*acpSession
	w.sessMu.Lock()
	for key, s := range w.sessions {
		if key != req.SessionKey && !s.busy && !s.host && time.Since(s.lastUsed) > sessionIdleLimit {
			delete(w.sessions, key)
			closing = append(closing, s)
		}
	}
	keep = req.SessionKey != ""
	if s, ok := w.sessions[req.SessionKey]; ok && keep {
		_, continues := backend.ContinueSession(s.history, req)
		switch {
		case s.busy:
			// Another turn holds it; two prompts can't share a session.
			keep = false
		case continues && s.cwd == cwd:
			s.busy, sess = true, s
		default:
			delete(w.sessions, req.SessionKey)
			closing = append(closing, s)
		}
	}
	w.sessMu.Unlock()
	for _, s := range closing {
		w.closeSession(s)
	}
	return sess, keep
}

func (w *worker) acquireSession(ctx context.Context, req backend.ChatRequest, cwd string, mcpServers []any) (*acpSession, bool, error) {
	upstreamModel, reasoningEffort, acpMode := req.UpstreamModel, req.ReasoningEffort, req.ACPMode
	sess, keep := w.takeSession(req, cwd)
	reused := sess != nil && !sess.fresh

	if sess == nil {
		var err error
		sess, err = w.newSession(ctx, cwd, mcpServers)
		if err != nil {
			return nil, false, err
		}
		w.sessMu.Lock()
		sess.busy, sess.fresh, sess.host, sess.lastUsed = true, true, req.HostTools, time.Now()
		if keep {
			w.sessions[req.SessionKey] = sess
		}
		w.sessMu.Unlock()
	}

	w.sessMu.Lock()
	if req.PermissionMode != "" {
		w.permByID[sess.id] = req.PermissionMode
	}
	if req.PermissionHandler != nil {
		w.permHandlerByID[sess.id] = req.PermissionHandler
	}
	w.sessMu.Unlock()

	if acpMode != "" && !strings.EqualFold(acpMode, sess.currentMode) {
		if sess.modeConfigID != "" {
			if _, err := w.call(ctx, "session/set_config_option", setConfigOptionParams{
				SessionID: sess.id,
				ConfigID:  sess.modeConfigID,
				Value:     acpMode,
			}); err == nil {
				sess.currentMode = acpMode
			}
		} else {
			if _, err := w.call(ctx, "session/set_mode", map[string]string{
				"sessionId": sess.id,
				"modeId":    acpMode,
			}); err == nil {
				sess.currentMode = acpMode
			}
		}
	}

	if upstreamModel != "" && upstreamModel != "default" && upstreamModel != sess.currentModel {
		if sess.modelConfigID != "" {
			if resRaw, err := w.call(ctx, "session/set_config_option", setConfigOptionParams{
				SessionID: sess.id,
				ConfigID:  sess.modelConfigID,
				Value:     upstreamModel,
			}); err == nil {
				sess.currentModel = upstreamModel
				var cfgRes struct {
					ConfigOptions []configOption `json:"configOptions"`
				}
				if json.Unmarshal(resRaw, &cfgRes) == nil && len(cfgRes.ConfigOptions) > 0 {
					for _, opt := range cfgRes.ConfigOptions {
						if isThoughtLevelOption(opt) {
							applyEffortConfigOption(sess, opt)
						}
					}
				}
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

	if sess.effortConfigID != "" {
		targetEffort := strings.ToLower(strings.TrimSpace(reasoningEffort))
		if targetEffort == "" && sess.hasDefaultEffort && sess.currentEffort != "" && !strings.EqualFold(sess.currentEffort, "default") {
			targetEffort = "default"
		}
		if targetEffort != "" && !strings.EqualFold(targetEffort, strings.TrimSpace(sess.currentEffort)) {
			if _, err := w.call(ctx, "session/set_config_option", setConfigOptionParams{
				SessionID: sess.id,
				ConfigID:  sess.effortConfigID,
				Value:     targetEffort,
			}); err == nil {
				sess.currentEffort = targetEffort
			}
		}
	}
	return sess, reused, nil
}

func normalizeRPCID(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	return strings.Trim(s, `"`)
}

func extractUpdateParts(raw json.RawMessage, toolMode string) (delta, thought string, passthrough bool) {
	var u struct {
		SessionUpdate string          `json:"sessionUpdate"`
		Kind          string          `json:"kind"`
		Title         string          `json:"title"`
		Status        string          `json:"status"`
		Content       json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return "", "", false
	}
	kind := u.SessionUpdate
	if kind == "" {
		kind = u.Kind
	}

	switch kind {
	case "agent_message_chunk":
		return extractTextFromContent(u.Content), "", false
	case "agent_thought_chunk":
		return "", extractTextFromContent(u.Content), true
	case "tool_call":
		if toolMode == config.ToolActivityText && u.Title != "" {
			return fmt.Sprintf("\n[tool: %s]\n", u.Title), "", true
		}
		return "", "", true
	case "tool_call_update":
		if toolMode == config.ToolActivityText && u.Title != "" && u.Status != "" {
			return fmt.Sprintf("\n[tool %s: %s]\n", u.Status, u.Title), "", true
		}
		return "", "", true
	case "plan", "usage_update":
		return "", "", true
	}
	return "", "", false
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

// isAgyQuotaReply reports whether an agent message chunk is agy's quota notice.
// agy sends it as reply text and ends the turn normally, not as an RPC error.
func isAgyQuotaReply(delta string) bool {
	t := strings.TrimSpace(delta)
	return strings.HasPrefix(t, "⚠") && len(t) < 400 && !strings.Contains(t, "\n") &&
		strings.Contains(strings.ToLower(t), "quota reached")
}
