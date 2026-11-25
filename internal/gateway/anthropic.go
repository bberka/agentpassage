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
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
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
		msgs = append(msgs, backend.Message{
			Role:    m.Role,
			Content: extractMessageContent(m.Content),
		})
	}

	sessionKey := strings.TrimSpace(r.Header.Get("X-Apx-Session"))
	if sessionKey == "" {
		sessionKey = strings.TrimSpace(req.Metadata.UserID)
	}

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
		Stream:      req.Stream,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		SessionKey:  sessionKey,
		Cwd:         strings.TrimSpace(r.Header.Get("X-Apx-Cwd")),
		HasTools:    len(req.Tools) > 0,
	}

	if p := principalFromContext(r.Context()); p != nil && p.Client != nil && !p.Client.AllowsModel(req.Model) {
		writeAnthropicError(w, http.StatusForbidden, "permission_error", fmt.Sprintf("API key %q is not allowed to access model %q.", p.KeyName, req.Model))
		return
	}
	plan, err := s.router.Resolve(req.Model)
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

	if req.Stream {
		s.executeAnthropicStream(w, r, plan, chatReq, start)
		return
	}

	resp, acct, status, execErr := s.executeNonStream(r.Context(), plan, chatReq)
	if execErr != nil {
		acctName := ""
		if acct != nil {
			acctName = acct.Backend + "/" + acct.Name
		}
		s.logRequest(r, req.Model, acctName, status, start, execErr)
		writeAnthropicError(w, status, "api_error", execErr.Error())
		return
	}

	acctName := acct.Backend + "/" + acct.Name
	w.Header().Set("X-Apx-Account", acctName)
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
	s.recordUsage(r.URL.Path, req.Model, acctName, acct.Backend, http.StatusOK, start, resp.Usage, resp.Usage.TotalTokens == 0)
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

func (s *Server) executeAnthropicStream(w http.ResponseWriter, r *http.Request, plan *router.Plan, req backend.ChatRequest, start time.Time) {
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
				writeAnthropicError(w, httpStatusFromError(lastErr), "api_error", lastErr.Error())
				return
			}
			s.logRequest(r, req.Model, "", httpStatusFromPoolError(err), start, err)
			writeAnthropicError(w, httpStatusFromPoolError(err), "overloaded_error", err.Error())
			return
		}

		acct := lease.Account
		acctName := acct.Backend + "/" + acct.Name
		lastAcct = acctName
		tried[acct.ID] = true

		flusher, ok := w.(http.Flusher)
		if !ok {
			lease.Release()
			writeAnthropicError(w, http.StatusInternalServerError, "api_error", "streaming unsupported")
			return
		}

		headersSent := false
		msgID := fmt.Sprintf("msg_%d", time.Now().UnixNano())
		stopReason := "end_turn"
		var outputTokens int

		sendStart := func(toolsIgnored bool, sessionID string) {
			if headersSent {
				return
			}
			headersSent = true
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.Header().Set("X-Apx-Account", acctName)
			if toolsIgnored {
				w.Header().Set("X-Apx-Tools-Ignored", "true")
			}
			if sessionID != "" {
				w.Header().Set("X-Apx-Session-Id", sessionID)
			}
			w.WriteHeader(http.StatusOK)
			writeAnthropicEvent(w, "message_start", map[string]any{
				"type": "message_start",
				"message": map[string]any{
					"id":      msgID,
					"type":    "message",
					"role":    "assistant",
					"model":   req.Model,
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

		cb := func(ch backend.StreamChunk) error {
			sendStart(ch.ToolsIgnored, ch.SessionID)
			if ch.Delta != "" {
				writeAnthropicEvent(w, "content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": 0,
					"delta": map[string]any{"type": "text_delta", "text": ch.Delta},
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

		acctReq := req
		acctReq.UpstreamModel = router.ResolveAccountEffortModel(acct, req.UpstreamModel, req.ReasoningEffort)

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
			sendStart(acct.Kind == config.KindACP && req.HasTools, "")
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
			s.recordUsage(r.URL.Path, req.Model, acctName, acct.Backend, http.StatusOK, start, u, outputTokens == 0)
			s.logRequest(r, req.Model, acctName, http.StatusOK, start, nil)
			return
		}

		lastErr = streamErr
		allowFailover := s.pool.RecordError(acct.ID, streamErr, plan.Rule)
		if headersSent {
			s.logRequest(r, req.Model, acctName, httpStatusFromError(streamErr), start, streamErr)
			writeAnthropicEvent(w, "error", map[string]any{
				"type":  "error",
				"error": map[string]any{"type": "api_error", "message": streamErr.Error()},
			})
			flusher.Flush()
			return
		}
		if !allowFailover {
			s.logRequest(r, req.Model, acctName, httpStatusFromError(streamErr), start, streamErr)
			writeAnthropicError(w, httpStatusFromError(streamErr), "api_error", streamErr.Error())
			return
		}
	}

	if lastErr == nil {
		lastErr = pool.ErrNoAvailableAccount
	}
	s.logRequest(r, req.Model, lastAcct, httpStatusFromError(lastErr), start, lastErr)
	writeAnthropicError(w, httpStatusFromError(lastErr), "api_error", lastErr.Error())
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
