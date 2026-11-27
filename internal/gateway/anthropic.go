package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/pool"
	"github.com/bberka/agentpassage/internal/privacy"
	"github.com/bberka/agentpassage/internal/quota"
	"github.com/bberka/agentpassage/internal/router"
)

type anthropicRequest struct {
	Model        string            `json:"model"`
	System       json.RawMessage   `json:"system,omitempty"`
	Messages     []openAIMessage   `json:"messages"`
	MaxTokens    int               `json:"max_tokens"`
	Temperature  *float64          `json:"temperature,omitempty"`
	Stream       bool              `json:"stream"`
	Tools        []json.RawMessage `json:"tools,omitempty"`
	Thinking     *struct {
		Type         string `json:"type"`
		BudgetTokens int    `json:"budget_tokens"`
	} `json:"thinking,omitempty"`
	OutputConfig *struct {
		Effort string `json:"effort"`
	} `json:"output_config,omitempty"`
	Metadata struct {
		UserID string `json:"user_id"`
	} `json:"metadata,omitempty"`
}

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, maxChatBodyBytes))
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "failed to read request body")
		return
	}
	var req anthropicRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON: "+err.Error())
		return
	}

	var msgs []backend.Message
	if len(req.System) > 0 {
		if sys := extractMessageContent(req.System); sys != "" {
			msgs = append(msgs, backend.Message{Role: "system", Content: sys})
		}
	}
	for _, m := range req.Messages {
		text, atts := parseContent(m.Content, "")
		msgs = append(msgs, backend.Message{
			Role:        m.Role,
			Content:     text,
			Attachments: atts,
		})
	}

	sessionKey := headerFirst(r, "X-Apx-Session", "X-Acpx-Session")
	if sessionKey == "" {
		sessionKey = strings.TrimSpace(req.Metadata.UserID)
	}
	project := projectFromRequest(r)

	effort := ""
	if req.OutputConfig != nil && req.OutputConfig.Effort != "" {
		effort = router.NormalizeEffort(req.OutputConfig.Effort)
	} else if req.Thinking != nil && req.Thinking.Type == "enabled" {
		effort = router.BudgetToEffort(req.Thinking.BudgetTokens)
	}

	chatReq := backend.ChatRequest{
		Model:           req.Model,
		ReasoningEffort: effort,
		Messages:        msgs,
		Stream:          req.Stream,
		Temperature:     req.Temperature,
		MaxTokens:       req.MaxTokens,
		SessionKey:      sessionKey,
		Cwd:             headerFirst(r, "X-Apx-Cwd", "X-Acpx-Cwd"),
		HasTools:        len(req.Tools) > 0,
	}

	principal := principalFromContext(r.Context())
	if principal != nil && principal.Client != nil && !principal.Client.AllowsModel(req.Model) {
		writeAnthropicError(w, http.StatusForbidden, "permission_error", fmt.Sprintf("API key %q is not allowed to access model %q.", principal.KeyName, req.Model))
		return
	}
	plan, err := s.router.ResolveForProject(req.Model, project)
	if err != nil {
		s.logRequest(r, req.Model, "", http.StatusNotFound, start, err)
		writeAnthropicError(w, http.StatusNotFound, "not_found_error", err.Error())
		return
	}
	if loopErr := checkLoopGuard(r, plan); loopErr != nil {
		s.logRequest(r, req.Model, plan.Backend, http.StatusLoopDetected, start, loopErr)
		writeAnthropicError(w, http.StatusLoopDetected, "loop_detected", loopErr.Error())
		return
	}
	chatReq.UpstreamModel = plan.UpstreamModel

	privSess := s.newPrivacySession()
	chatReq = applyPrivacyToRequest(privSess, chatReq)

	if req.Stream {
		s.executeAnthropicStream(w, r, plan, project, principal, chatReq, privSess, start)
		return
	}

	res := s.executeNonStreamWithFallback(r.Context(), plan, project, principal, chatReq)
	resp, acct, status, execErr := res.Resp, res.Account, res.Status, res.Err
	if execErr != nil {
		acctName := ""
		if acct != nil {
			acctName = acct.Backend + "/" + acct.Name
		}
		s.logRequest(r, req.Model, acctName, status, start, execErr)
		writeAnthropicError(w, status, "api_error", execErr.Error())
		return
	}
	if privSess != nil {
		resp.Content = privSess.RestoreText(resp.Content)
		s.recordPrivacyHeader(w, privSess, res.UsedModel)
	}

	acctName := acct.Backend + "/" + acct.Name
	w.Header().Set("X-Apx-Account", acctName)
	w.Header().Set("X-Apx-Model", res.UsedModel)
	if res.FallbackFrom != "" {
		w.Header().Set("X-Apx-Fallback-From", res.FallbackFrom)
	}
	if project != "" {
		w.Header().Set("X-Apx-Project", project)
	}
	if resp.ToolsIgnored {
		w.Header().Set("X-Apx-Tools-Ignored", "true")
	}
	if resp.SessionID != "" {
		w.Header().Set("X-Apx-Session-Id", resp.SessionID)
	}

	stopReason := "end_turn"
	if resp.FinishReason == "length" {
		stopReason = "max_tokens"
	}

	msgID := fmt.Sprintf("msg_%d", time.Now().UnixNano())
	s.recordUsageForRequest(r, res.UsedModel, acctName, acct.Backend, http.StatusOK, start, resp.Usage, resp.Usage.TotalTokens == 0)
	s.logRequest(r, req.Model, acctName, http.StatusOK, start, nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"model":         req.Model,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"content": []map[string]any{
			{"type": "text", "text": resp.Content},
		},
		"usage": map[string]any{
			"input_tokens":  resp.Usage.PromptTokens,
			"output_tokens": resp.Usage.CompletionTokens,
		},
	})
}

func (s *Server) executeAnthropicStream(w http.ResponseWriter, r *http.Request, primaryPlan *router.Plan, project string, principal *authPrincipal, req backend.ChatRequest, privSess *privacy.Session, start time.Time) {
	chain := s.buildModelChain(primaryPlan, project, principal)
	var lastErr error
	var lastAcct string

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "streaming unsupported")
		return
	}

	for planIdx, plan := range chain {
		if planIdx > 0 {
			if loopErr := checkLoopGuard(r, plan); loopErr != nil {
				continue
			}
		}
		tried := make(map[string]bool)
		var planErr error

		for attempt := 0; attempt < max(len(plan.CandidateIDs), 1); attempt++ {
			lease, err := s.pool.Acquire(s.buildPoolSelectRequest(plan, req.SessionKey, tried))
			if err != nil {
				if planErr == nil {
					planErr = err
				}
				break
			}

			acct := lease.Account
			acctName := acct.Backend + "/" + acct.Name
			lastAcct = acctName
			tried[acct.ID] = true

			headersSent := false
			msgID := fmt.Sprintf("msg_%d", time.Now().UnixNano())
			stopReason := "end_turn"
			var outputTokens int
			usedModel := plan.RequestedModel

			sendStart := func(toolsIgnored bool, sessionID string) {
				if headersSent {
					return
				}
				headersSent = true
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Connection", "keep-alive")
				w.Header().Set("X-Apx-Account", acctName)
				w.Header().Set("X-Apx-Model", usedModel)
				if planIdx > 0 {
					w.Header().Set("X-Apx-Fallback-From", primaryPlan.RequestedModel)
				}
				if project != "" {
					w.Header().Set("X-Apx-Project", project)
				}
				if toolsIgnored {
					w.Header().Set("X-Apx-Tools-Ignored", "true")
				}
				if sessionID != "" {
					w.Header().Set("X-Apx-Session-Id", sessionID)
				}
				s.recordPrivacyHeader(w, privSess, usedModel)
				w.WriteHeader(http.StatusOK)
				writeAnthropicEvent(w, "message_start", map[string]any{
					"type": "message_start",
					"message": map[string]any{
						"id":      msgID,
						"type":    "message",
						"role":    "assistant",
						"model":   usedModel,
						"content": []any{},
						"usage":   map[string]any{"input_tokens": 0, "output_tokens": 0},
					},
				})
				writeAnthropicEvent(w, "content_block_start", map[string]any{
					"type":          "content_block_start",
					"index":         0,
					"content_block": map[string]any{"type": "text", "text": ""},
				})
				flusher.Flush()
			}

			deltaRestorer := privSess.NewStreamRestorer()
			cb := func(ch backend.StreamChunk) error {
				if ch.Delta == "" && ch.FinishReason == "" {
					if ch.Usage != nil && ch.Usage.CompletionTokens > 0 {
						outputTokens = ch.Usage.CompletionTokens
					}
					return nil
				}
				sendStart(ch.ToolsIgnored, ch.SessionID)
				restored := deltaRestorer.Push(ch.Delta)
				if ch.FinishReason != "" {
					restored += deltaRestorer.Flush()
				}
				if restored != "" {
					writeAnthropicEvent(w, "content_block_delta", map[string]any{
						"type":  "content_block_delta",
						"index": 0,
						"delta": map[string]any{"type": "text_delta", "text": restored},
					})
					flusher.Flush()
				}
				if ch.FinishReason == "length" {
					stopReason = "max_tokens"
				}
				if ch.Usage != nil && ch.Usage.CompletionTokens > 0 {
					outputTokens = ch.Usage.CompletionTokens
				}
				return nil
			}

			acctReq := applyRouteDefaults(req, plan.Rule)
			acctReq.Model = usedModel
			acctReq.UpstreamModel = router.ResolveAccountEffortModel(acct, plan.UpstreamModel, acctReq.ReasoningEffort)

			var streamErr error
			switch acct.Kind {
			case config.KindACP:
				streamErr = s.acpBE.Stream(r.Context(), acct, acctReq, cb)
			case config.KindCLI:
				streamErr = s.cliBE.Stream(r.Context(), acct, acctReq, cb)
			default:
				apiKey, _ := s.secrets.Get(acct.ID)
				streamErr = s.httpBE.Stream(r.Context(), acct, apiKey, acctReq, cb)
			}
			lease.Release()

			if streamErr == nil {
				s.pool.RecordSuccess(acct.ID)
				s.pool.PinSession(req.SessionKey, acct.ID)
				sendStart(acct.Kind == config.KindACP && req.HasTools, "")
				if tail := deltaRestorer.Flush(); tail != "" {
					writeAnthropicEvent(w, "content_block_delta", map[string]any{
						"type":  "content_block_delta",
						"index": 0,
						"delta": map[string]any{"type": "text_delta", "text": tail},
					})
				}
				writeAnthropicEvent(w, "content_block_stop", map[string]any{
					"type":  "content_block_stop",
					"index": 0,
				})
				writeAnthropicEvent(w, "message_delta", map[string]any{
					"type":  "message_delta",
					"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
					"usage": map[string]any{"output_tokens": outputTokens},
				})
				writeAnthropicEvent(w, "message_stop", map[string]any{"type": "message_stop"})
				flusher.Flush()
				u := backend.Usage{PromptTokens: estimateMessagesTokens(req.Messages), CompletionTokens: outputTokens}
				u.TotalTokens = u.PromptTokens + u.CompletionTokens
				s.recordUsageForRequest(r, usedModel, acctName, acct.Backend, http.StatusOK, start, u, outputTokens == 0)
				s.logRequest(r, usedModel, acctName, http.StatusOK, start, nil)
				return
			}

			planErr = streamErr
			lastErr = streamErr
			qGroup := quota.GroupForModel(plan.Backend, plan.UpstreamModel)
			allowFailover := s.pool.RecordErrorForGroup(acct.ID, qGroup, streamErr, plan.Rule)
			if headersSent {
				s.logRequest(r, usedModel, acctName, httpStatusFromError(streamErr), start, streamErr)
				writeAnthropicEvent(w, "error", map[string]any{
					"type":  "error",
					"error": map[string]any{"type": "api_error", "message": streamErr.Error()},
				})
				flusher.Flush()
				return
			}
			if !allowFailover {
				break
			}
		}

		if planErr != nil {
			lastErr = planErr
		}
		if !shouldFallbackModel(lastErr, primaryPlan.Rule) {
			break
		}
	}

	if lastErr == nil {
		lastErr = pool.ErrNoAvailableAccount
	}
	status := httpStatusFromError(lastErr)
	s.logRequest(r, req.Model, lastAcct, status, start, lastErr)
	writeAnthropicError(w, status, "api_error", lastErr.Error())
}

func writeAnthropicEvent(w io.Writer, event string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw)
}

func writeAnthropicError(w http.ResponseWriter, status int, errType, msg string) {
	writeJSON(w, status, map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    errType,
			"message": msg,
		},
	})
}
