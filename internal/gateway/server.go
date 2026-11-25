package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bberka/acpx/internal/backend"
	"github.com/bberka/acpx/internal/backend/acpbackend"
	"github.com/bberka/acpx/internal/backend/httpbackend"
	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/descriptors"
	"github.com/bberka/acpx/internal/logbuf"
	"github.com/bberka/acpx/internal/pool"
	"github.com/bberka/acpx/internal/router"
	"github.com/bberka/acpx/internal/secrets"
)

const Version = "0.1.0"

// Server is the local OpenAI & Anthropic compatible HTTP gateway.
type Server struct {
	mu       sync.RWMutex
	dataDir  string
	cfg      *config.Config
	secrets  *secrets.Store
	registry *descriptors.Registry
	router   *router.Router
	pool     *pool.Manager
	httpBE   *httpbackend.Client
	acpBE    *acpbackend.Manager
	logs     *logbuf.Buffer

	srv      *http.Server
	listener net.Listener
	running  bool
	listenOn string
}

func New(dataDir string, cfg *config.Config, sec *secrets.Store, reg *descriptors.Registry, logs *logbuf.Buffer) *Server {
	rtr := router.New(cfg)
	pm := pool.NewManager(cfg.Accounts)
	acpMgr := acpbackend.NewManager(dataDir, reg, logs, cfg.ACP.ToolActivity)
	s := &Server{
		dataDir:  dataDir,
		cfg:      cfg,
		secrets:  sec,
		registry: reg,
		router:   rtr,
		pool:     pm,
		httpBE:   httpbackend.New(),
		acpBE:    acpMgr,
		logs:     logs,
	}
	if logs != nil {
		logs.RegisterSecret(cfg.Server.LocalKey)
	}
	pm.SetAutoDisableHook(func(accountID, reason string) {
		s.mu.Lock()
		for i := range s.cfg.Accounts {
			if s.cfg.Accounts[i].ID == accountID {
				s.cfg.Accounts[i].Enabled = false
				s.cfg.Accounts[i].DisabledReason = reason
				break
			}
		}
		_ = config.Save(s.dataDir, s.cfg)
		s.mu.Unlock()
		if s.logs != nil {
			s.logs.Warn("health", fmt.Sprintf("account %s auto-disabled: %s", accountID, reason))
		}
	})
	return s
}

func (s *Server) Config() *config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

func (s *Server) Pool() *pool.Manager         { return s.pool }
func (s *Server) Router() *router.Router      { return s.router }
func (s *Server) Secrets() *secrets.Store     { return s.secrets }
func (s *Server) Registry() *descriptors.Registry { return s.registry }
func (s *Server) ACP() *acpbackend.Manager    { return s.acpBE }
func (s *Server) HTTPBackend() *httpbackend.Client { return s.httpBE }
func (s *Server) Logs() *logbuf.Buffer        { return s.logs }
func (s *Server) DataDir() string             { return s.dataDir }

func (s *Server) ReloadConfig(cfg *config.Config) error {
	s.mu.Lock()
	s.cfg = cfg
	if s.logs != nil {
		s.logs.RegisterSecret(cfg.Server.LocalKey)
	}
	s.mu.Unlock()

	s.router.UpdateConfig(cfg)
	s.pool.SyncAccounts(cfg.Accounts)
	s.acpBE.SetToolActivity(cfg.ACP.ToolActivity)
	return config.Save(s.dataDir, cfg)
}

func (s *Server) Addr() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.listenOn != "" {
		return s.listenOn
	}
	return fmt.Sprintf("%s:%d", s.cfg.Server.Bind, s.cfg.Server.Port)
}

func (s *Server) Running() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /admin/state", s.withAuth(s.handleAdminState))
	mux.HandleFunc("GET /v1/models", s.withAuth(s.handleListModels))
	mux.HandleFunc("POST /v1/chat/completions", s.withAuth(s.handleChatCompletions))
	mux.HandleFunc("POST /v1/messages", s.withAuth(s.handleAnthropicMessages))
	return mux
}

func (s *Server) Start() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	cfg := s.cfg
	if !cfg.IsLoopbackBind() && !cfg.Server.AllowRemote {
		s.mu.Unlock()
		return fmt.Errorf("refusing to bind to non-loopback address %q without --allow-remote", cfg.Server.Bind)
	}
	addr := net.JoinHostPort(cfg.Server.Bind, fmt.Sprintf("%d", cfg.Server.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	srv := &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}
	s.listener = ln
	s.srv = srv
	s.running = true
	s.listenOn = ln.Addr().String()
	s.mu.Unlock()

	if !cfg.IsLoopbackBind() && s.logs != nil {
		s.logs.Warn("system", fmt.Sprintf("WARNING: gateway bound to non-loopback address %s", ln.Addr().String()))
	} else if s.logs != nil {
		s.logs.Info("system", fmt.Sprintf("gateway listening on http://%s/v1", ln.Addr().String()))
	}

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			if s.logs != nil {
				s.logs.Error("system", "http server error", err)
			}
		}
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	srv := s.srv
	s.running = false
	s.mu.Unlock()
	s.acpBE.Close()
	if srv != nil {
		return srv.Shutdown(ctx)
	}
	return nil
}

// TestAccount runs a live connectivity test against an API-key or ACP account and returns discovered models.
func (s *Server) TestAccount(ctx context.Context, acct config.AccountConfig) ([]string, error) {
	if acct.Kind == config.KindACP {
		return s.acpBE.TestAccount(ctx, acct)
	}
	key, _ := s.secrets.Get(acct.ID)
	return s.httpBE.TestAccount(ctx, acct, key)
}

func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.RLock()
		expected := s.cfg.Server.LocalKey
		s.mu.RUnlock()

		var token string
		if auth := r.Header.Get("Authorization"); auth != "" {
			if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
				token = strings.TrimSpace(auth[7:])
			}
		}
		if token == "" {
			token = strings.TrimSpace(r.Header.Get("x-api-key"))
		}
		if expected == "" || subtle.ConstantTimeCompare([]byte(token), []byte(expected)) != 1 {
			writeOpenAIError(w, http.StatusUnauthorized, "invalid_api_key", "Invalid or missing local acpx API key.")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": Version,
	})
}

func (s *Server) handleAdminState(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	cfg := s.cfg
	listenOn := s.listenOn
	running := s.running
	s.mu.RUnlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"running":         running,
		"address":         listenOn,
		"bind":            cfg.Server.Bind,
		"port":            cfg.Server.Port,
		"secrets_backend": s.secrets.Backend(),
		"accounts":        s.pool.Snapshots(),
		"banners":         s.pool.Banners(),
		"models_count":    len(s.router.ListModels(false)),
	})
}

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	models := s.router.ListModels(false)
	now := time.Now().Unix()
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		owner := m.Backend
		if m.AccountName != "" {
			owner = m.Backend + "/" + m.AccountName
		}
		data = append(data, map[string]any{
			"id":       m.ID,
			"object":   "model",
			"created":  now,
			"owned_by": owner,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}

type openAIChatRequest struct {
	Model       string            `json:"model"`
	Messages    []openAIMessage   `json:"messages"`
	Stream      bool              `json:"stream"`
	Temperature *float64          `json:"temperature,omitempty"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	MaxCompTok  int               `json:"max_completion_tokens,omitempty"`
	User        string            `json:"user,omitempty"`
	Tools       []json.RawMessage `json:"tools,omitempty"`
	Functions   []json.RawMessage `json:"functions,omitempty"`
}

type openAIMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func extractMessageContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var sb strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				sb.WriteString(p.Text)
			}
		}
		return sb.String()
	}
	return string(raw)
}

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "failed to read request body")
		return
	}
	var req openAIChatRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON request body: "+err.Error())
		return
	}

	msgs := make([]backend.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, backend.Message{
			Role:    m.Role,
			Content: extractMessageContent(m.Content),
		})
	}
	maxTok := req.MaxTokens
	if maxTok == 0 {
		maxTok = req.MaxCompTok
	}
	sessionKey := strings.TrimSpace(r.Header.Get("X-Acpx-Session"))
	if sessionKey == "" {
		sessionKey = strings.TrimSpace(req.User)
	}

	chatReq := backend.ChatRequest{
		Model:         req.Model,
		Messages:      msgs,
		Stream:        req.Stream,
		Temperature:   req.Temperature,
		MaxTokens:     maxTok,
		SessionKey:    sessionKey,
		Cwd:           strings.TrimSpace(r.Header.Get("X-Acpx-Cwd")),
		HasTools:      len(req.Tools) > 0 || len(req.Functions) > 0,
		RawOpenAIBody: rawBody,
	}

	plan, err := s.router.Resolve(req.Model)
	if err != nil {
		s.logRequest(r, req.Model, "", http.StatusNotFound, start, err)
		writeOpenAIError(w, http.StatusNotFound, "model_not_found", err.Error())
		return
	}
	chatReq.UpstreamModel = plan.UpstreamModel

	if req.Stream {
		s.executeOpenAIStream(w, r, plan, chatReq, start)
		return
	}

	resp, acct, status, execErr := s.executeNonStream(r.Context(), plan, chatReq)
	if execErr != nil {
		acctName := ""
		if acct != nil {
			acctName = acct.Backend + "/" + acct.Name
		}
		s.logRequest(r, req.Model, acctName, status, start, execErr)
		writeOpenAIError(w, status, "backend_error", execErr.Error())
		return
	}

	acctName := acct.Backend + "/" + acct.Name
	w.Header().Set("X-Acpx-Account", acctName)
	if resp.ToolsIgnored {
		w.Header().Set("X-Acpx-Tools-Ignored", "true")
	}
	if resp.SessionID != "" {
		w.Header().Set("X-Acpx-Session-Id", resp.SessionID)
	}

	id := resp.ID
	if id == "" {
		id = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	out := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   req.Model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": resp.Content,
				},
				"finish_reason": resp.FinishReason,
			},
		},
		"usage": resp.Usage,
	}
	s.logRequest(r, req.Model, acctName, http.StatusOK, start, nil)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) executeNonStream(ctx context.Context, plan *router.Plan, req backend.ChatRequest) (*backend.ChatResponse, *config.AccountConfig, int, error) {
	tried := make(map[string]bool)
	var lastAcct *config.AccountConfig
	var lastErr error

	for attempt := 0; attempt < max(len(plan.CandidateIDs), 1); attempt++ {
		lease, err := s.pool.Acquire(pool.SelectRequest{
			RuleKey:                  plan.Rule.Pattern,
			Strategy:                 plan.Rule.Strategy,
			CandidateIDs:             plan.CandidateIDs,
			SessionKey:               req.SessionKey,
			SubscriptionWarningAcked: plan.Rule.SubscriptionWarningAcked,
			ExcludeIDs:               tried,
		})
		if err != nil {
			if lastErr != nil {
				return nil, lastAcct, httpStatusFromError(lastErr), lastErr
			}
			return nil, nil, httpStatusFromPoolError(err), err
		}

		acct := lease.Account
		lastAcct = &acct
		tried[acct.ID] = true

		var resp *backend.ChatResponse
		var callErr error
		if acct.Kind == config.KindACP {
			resp, callErr = s.acpBE.Complete(ctx, acct, req)
		} else {
			apiKey, _ := s.secrets.Get(acct.ID)
			resp, callErr = s.httpBE.Complete(ctx, acct, apiKey, req)
		}
		lease.Release()

		if callErr == nil {
			s.pool.RecordSuccess(acct.ID)
			return resp, &acct, http.StatusOK, nil
		}

		lastErr = callErr
		allowFailover := s.pool.RecordError(acct.ID, callErr, plan.Rule)
		if !allowFailover {
			return nil, &acct, httpStatusFromError(callErr), callErr
		}
	}
	if lastErr == nil {
		lastErr = pool.ErrNoAvailableAccount
	}
	return nil, lastAcct, httpStatusFromError(lastErr), lastErr
}

func (s *Server) executeOpenAIStream(w http.ResponseWriter, r *http.Request, plan *router.Plan, req backend.ChatRequest, start time.Time) {
	tried := make(map[string]bool)
	var lastErr error
	var lastAcct string

	for attempt := 0; attempt < max(len(plan.CandidateIDs), 1); attempt++ {
		lease, err := s.pool.Acquire(pool.SelectRequest{
			RuleKey:                  plan.Rule.Pattern,
			Strategy:                 plan.Rule.Strategy,
			CandidateIDs:             plan.CandidateIDs,
			SessionKey:               req.SessionKey,
			SubscriptionWarningAcked: plan.Rule.SubscriptionWarningAcked,
			ExcludeIDs:               tried,
		})
		if err != nil {
			if lastErr != nil {
				s.logRequest(r, req.Model, lastAcct, httpStatusFromError(lastErr), start, lastErr)
				writeOpenAIError(w, httpStatusFromError(lastErr), "backend_error", lastErr.Error())
				return
			}
			s.logRequest(r, req.Model, "", httpStatusFromPoolError(err), start, err)
			writeOpenAIError(w, httpStatusFromPoolError(err), "pool_error", err.Error())
			return
		}

		acct := lease.Account
		acctName := acct.Backend + "/" + acct.Name
		lastAcct = acctName
		tried[acct.ID] = true

		flusher, ok := w.(http.Flusher)
		if !ok {
			lease.Release()
			writeOpenAIError(w, http.StatusInternalServerError, "streaming_unsupported", "streaming unsupported")
			return
		}

		headersSent := false
		compID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
		created := time.Now().Unix()

		sendHeaders := func(toolsIgnored bool, sessionID string) {
			if headersSent {
				return
			}
			headersSent = true
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Acpx-Account", acctName)
			if toolsIgnored {
				w.Header().Set("X-Acpx-Tools-Ignored", "true")
			}
			if sessionID != "" {
				w.Header().Set("X-Acpx-Session-Id", sessionID)
			}
			w.WriteHeader(http.StatusOK)
			// Initial role chunk
			writeSSE(w, map[string]any{
				"id":      compID,
				"object":  "chat.completion.chunk",
				"created": created,
				"model":   req.Model,
				"choices": []map[string]any{
					{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil},
				},
			})
			flusher.Flush()
		}

		var finishedSent bool
		cb := func(ch backend.StreamChunk) error {
			sendHeaders(ch.ToolsIgnored, ch.SessionID)
			delta := map[string]any{}
			if ch.Delta != "" {
				delta["content"] = ch.Delta
			}
			if ch.Reasoning != "" {
				delta["reasoning_content"] = ch.Reasoning
			}
			var fr any = nil
			if ch.FinishReason != "" {
				fr = ch.FinishReason
				finishedSent = true
			}
			payload := map[string]any{
				"id":      compID,
				"object":  "chat.completion.chunk",
				"created": created,
				"model":   req.Model,
				"choices": []map[string]any{
					{"index": 0, "delta": delta, "finish_reason": fr},
				},
			}
			if ch.Usage != nil {
				payload["usage"] = ch.Usage
			}
			writeSSE(w, payload)
			flusher.Flush()
			return nil
		}

		var streamErr error
		if acct.Kind == config.KindACP {
			streamErr = s.acpBE.Stream(r.Context(), acct, req, cb)
		} else {
			apiKey, _ := s.secrets.Get(acct.ID)
			streamErr = s.httpBE.Stream(r.Context(), acct, apiKey, req, cb)
		}
		lease.Release()

		if streamErr == nil {
			s.pool.RecordSuccess(acct.ID)
			if !headersSent {
				sendHeaders(acct.Kind == config.KindACP && req.HasTools, "")
			}
			if !finishedSent {
				writeSSE(w, map[string]any{
					"id":      compID,
					"object":  "chat.completion.chunk",
					"created": created,
					"model":   req.Model,
					"choices": []map[string]any{
						{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"},
					},
				})
			}
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			flusher.Flush()
			s.logRequest(r, req.Model, acctName, http.StatusOK, start, nil)
			return
		}

		lastErr = streamErr
		allowFailover := s.pool.RecordError(acct.ID, streamErr, plan.Rule)
		if headersSent {
			// Cannot retry after streaming started; emit error comment/event and close.
			s.logRequest(r, req.Model, acctName, httpStatusFromError(streamErr), start, streamErr)
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}
		if !allowFailover {
			s.logRequest(r, req.Model, acctName, httpStatusFromError(streamErr), start, streamErr)
			writeOpenAIError(w, httpStatusFromError(streamErr), "backend_error", streamErr.Error())
			return
		}
	}

	if lastErr == nil {
		lastErr = pool.ErrNoAvailableAccount
	}
	s.logRequest(r, req.Model, lastAcct, httpStatusFromError(lastErr), start, lastErr)
	writeOpenAIError(w, httpStatusFromError(lastErr), "backend_error", lastErr.Error())
}

func (s *Server) logRequest(r *http.Request, model, acct string, status int, start time.Time, err error) {
	if s.logs == nil {
		return
	}
	lvl := logbuf.LevelInfo
	errStr := ""
	if err != nil {
		lvl = logbuf.LevelError
		errStr = err.Error()
	} else if status >= 400 {
		lvl = logbuf.LevelWarn
	}
	s.logs.Add(logbuf.Entry{
		Time:       time.Now(),
		Level:      lvl,
		Kind:       "request",
		Method:     r.Method,
		Path:       r.URL.Path,
		Model:      model,
		Account:    acct,
		Status:     status,
		DurationMs: time.Since(start).Milliseconds(),
		Error:      errStr,
	})
}

func httpStatusFromPoolError(err error) int {
	if errors.Is(err, pool.ErrStrategyOptInNeeded) {
		return http.StatusForbidden
	}
	return http.StatusServiceUnavailable
}

func httpStatusFromError(err error) int {
	if be, ok := backend.AsBackendError(err); ok {
		if be.StatusCode >= 400 {
			return be.StatusCode
		}
		switch be.Kind {
		case backend.ErrKindRateLimit:
			return http.StatusTooManyRequests
		case backend.ErrKindAuth, backend.ErrKindRestricted:
			return http.StatusUnauthorized
		case backend.ErrKindClient:
			return http.StatusBadRequest
		}
	}
	return http.StatusBadGateway
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeSSE(w io.Writer, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
}

func writeOpenAIError(w http.ResponseWriter, status int, errType, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    errType,
		},
	})
}
