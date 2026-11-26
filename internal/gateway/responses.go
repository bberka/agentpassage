package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/logbuf"
	"github.com/bberka/agentpassage/internal/router"
	"github.com/bberka/agentpassage/internal/usage"
)

type responsesStore struct {
	mu      sync.Mutex
	history map[string][]backend.Message
	order   []string
}

var globalRespStore = &responsesStore{
	history: make(map[string][]backend.Message),
}

func (rs *responsesStore) get(id string) []backend.Message {
	if id == "" {
		return nil
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if h, ok := rs.history[id]; ok {
		return append([]backend.Message(nil), h...)
	}
	return nil
}

func (rs *responsesStore) put(id string, msgs []backend.Message) {
	if id == "" {
		return
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if _, exists := rs.history[id]; !exists {
		rs.order = append(rs.order, id)
		if len(rs.order) > 500 {
			evict := rs.order[0]
			rs.order = rs.order[1:]
			delete(rs.history, evict)
		}
	}
	rs.history[id] = append([]backend.Message(nil), msgs...)
}

type openAIResponsesRequest struct {
	Model              string          `json:"model"`
	Input              json.RawMessage `json:"input"`
	Instructions       string          `json:"instructions,omitempty"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	Stream             bool            `json:"stream,omitempty"`
	Temperature        *float64        `json:"temperature,omitempty"`
	MaxOutputTokens    int             `json:"max_output_tokens,omitempty"`
	User               string          `json:"user,omitempty"`
	Reasoning          *struct {
		Effort string `json:"effort,omitempty"`
	} `json:"reasoning,omitempty"`
}

func parseResponsesInput(raw json.RawMessage) []backend.Message {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var asStr string
	if json.Unmarshal(raw, &asStr) == nil && asStr != "" {
		return []backend.Message{{Role: "user", Content: asStr}}
	}
	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
		Text    string          `json:"text"`
	}
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	out := make([]backend.Message, 0, len(items))
	for _, it := range items {
		role := it.Role
		if role == "" {
			role = "user"
		}
		text := it.Text
		var atts []backend.Attachment
		if text == "" && len(it.Content) > 0 {
			text, atts = parseContent(it.Content, "\n")
		}
		if text != "" || len(atts) > 0 {
			out = append(out, backend.Message{Role: role, Content: text, Attachments: atts})
		}
	}
	return out
}

// handleOpenAIResponses implements OpenAI's Responses API (`POST /v1/responses`),
// supporting both streaming SSE and non-streaming JSON responses with `reasoning.effort`
// and stateful `previous_response_id` chaining.
func (s *Server) handleOpenAIResponses(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxChatBodyBytes))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "failed to read request body")
		return
	}
	var req openAIResponsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	principal := principalFromContext(r.Context())
	if principal != nil && principal.Client != nil && !principal.Client.AllowsModel(req.Model) {
		writeOpenAIError(w, http.StatusForbidden, "model_not_allowed", fmt.Sprintf("API key %q is not permitted to use model %q", principal.Client.Name, req.Model))
		return
	}
	keyName := "local"
	if principal != nil && principal.KeyName != "" {
		keyName = principal.KeyName
	}

	var msgs []backend.Message
	if prev := globalRespStore.get(req.PreviousResponseID); len(prev) > 0 {
		msgs = append(msgs, prev...)
	}
	if strings.TrimSpace(req.Instructions) != "" {
		msgs = append([]backend.Message{{Role: "system", Content: req.Instructions}}, msgs...)
	}
	msgs = append(msgs, parseResponsesInput(req.Input)...)
	if len(msgs) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "input is required")
		return
	}

	effort := ""
	if req.Reasoning != nil {
		effort = router.NormalizeEffort(req.Reasoning.Effort)
	}
	project := projectFromRequest(r)
	sessionKey := strings.TrimSpace(r.Header.Get("X-Apx-Session"))
	if sessionKey == "" {
		sessionKey = strings.TrimSpace(req.PreviousResponseID)
	}
	if sessionKey == "" {
		sessionKey = strings.TrimSpace(req.User)
	}

	chatReq := backend.ChatRequest{
		Model:           req.Model,
		ReasoningEffort: effort,
		Messages:        msgs,
		Stream:          req.Stream,
		Temperature:     req.Temperature,
		MaxTokens:       req.MaxOutputTokens,
		SessionKey:      sessionKey,
		Cwd:             strings.TrimSpace(r.Header.Get("X-Apx-Cwd")),
	}

	respID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	msgID := fmt.Sprintf("msg_%d", time.Now().UnixNano())
	createdAt := time.Now().Unix()

	if !req.Stream {
		var textSB strings.Builder
		var finalUsage backend.Usage
		acctName, err := s.StreamDirect(r.Context(), chatReq, func(ch backend.StreamChunk) error {
			if ch.Delta != "" {
				textSB.WriteString(ch.Delta)
			}
			if ch.Usage != nil {
				finalUsage = *ch.Usage
			}
			return nil
		})
		durMs := time.Since(start).Milliseconds()
		if err != nil {
			status := httpStatusFromError(err)
			s.logs.Add(logbuf.Entry{
				Level:      logbuf.LevelError,
				Kind:       "request",
				Method:     r.Method,
				Path:       r.URL.Path,
				Model:      req.Model,
				Effort:     effort,
				Project:    project,
				Account:    acctName,
				Status:     status,
				DurationMs: durMs,
				Error:      err.Error(),
			})
			writeOpenAIError(w, status, "api_error", err.Error())
			return
		}
		replyText := textSB.String()
		globalRespStore.put(respID, append(msgs, backend.Message{Role: "assistant", Content: replyText}))
		estimated := false
		if finalUsage.PromptTokens == 0 {
			finalUsage.PromptTokens = estimateMessagesTokens(msgs)
			estimated = true
		}
		if finalUsage.CompletionTokens == 0 {
			finalUsage.CompletionTokens = usage.EstimateTokens(replyText)
			estimated = true
		}
		if finalUsage.TotalTokens == 0 {
			finalUsage.TotalTokens = finalUsage.PromptTokens + finalUsage.CompletionTokens
		}
		if s.usage != nil {
			s.usage.Record(usage.Record{
				Time:             time.Now(),
				Model:            req.Model,
				Account:          acctName,
				Endpoint:         r.URL.Path,
				APIKey:           keyName,
				Project:          project,
				PromptTokens:     finalUsage.PromptTokens,
				CompletionTokens: finalUsage.CompletionTokens,
				TotalTokens:      finalUsage.TotalTokens,
				Estimated:        estimated,
				Status:           http.StatusOK,
				DurationMs:       durMs,
			})
		}
		s.logs.Add(logbuf.Entry{
			Level:      logbuf.LevelInfo,
			Kind:       "request",
			Method:     r.Method,
			Path:       r.URL.Path,
			Model:      req.Model,
			Effort:     effort,
			Project:    project,
			Account:    acctName,
			Status:     http.StatusOK,
			DurationMs: durMs,
			TokensIn:   finalUsage.PromptTokens,
			TokensOut:  finalUsage.CompletionTokens,
		})
		writeJSON(w, http.StatusOK, buildOpenAIResponseObj(respID, msgID, req.Model, replyText, createdAt, finalUsage))
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	writeRespSSE := func(event string, data any) {
		b, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}

	writeRespSSE("response.created", map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id":         respID,
			"object":     "response",
			"created_at": createdAt,
			"status":     "in_progress",
			"model":      req.Model,
			"output":     []any{},
		},
	})

	var textSB strings.Builder
	var finalUsage backend.Usage
	acctName, err := s.StreamDirect(r.Context(), chatReq, func(ch backend.StreamChunk) error {
		if ch.Delta != "" {
			textSB.WriteString(ch.Delta)
			writeRespSSE("response.output_text.delta", map[string]any{
				"type":          "response.output_text.delta",
				"item_id":       msgID,
				"output_index":  0,
				"content_index": 0,
				"delta":         ch.Delta,
			})
		}
		if ch.Usage != nil {
			finalUsage = *ch.Usage
		}
		return nil
	})
	durMs := time.Since(start).Milliseconds()
	if err != nil {
		writeRespSSE("response.failed", map[string]any{
			"type":  "response.failed",
			"error": map[string]any{"message": err.Error()},
		})
		s.logs.Add(logbuf.Entry{
			Level:      logbuf.LevelError,
			Kind:       "request",
			Method:     r.Method,
			Path:       r.URL.Path,
			Model:      req.Model,
			Effort:     effort,
			Project:    project,
			Account:    acctName,
			Status:     httpStatusFromError(err),
			DurationMs: durMs,
			Error:      err.Error(),
		})
		return
	}

	replyText := textSB.String()
	globalRespStore.put(respID, append(msgs, backend.Message{Role: "assistant", Content: replyText}))
	estimated := false
	if finalUsage.PromptTokens == 0 {
		finalUsage.PromptTokens = estimateMessagesTokens(msgs)
		estimated = true
	}
	if finalUsage.CompletionTokens == 0 {
		finalUsage.CompletionTokens = usage.EstimateTokens(replyText)
		estimated = true
	}
	if finalUsage.TotalTokens == 0 {
		finalUsage.TotalTokens = finalUsage.PromptTokens + finalUsage.CompletionTokens
	}
	writeRespSSE("response.completed", map[string]any{
		"type":     "response.completed",
		"response": buildOpenAIResponseObj(respID, msgID, req.Model, replyText, createdAt, finalUsage),
	})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()

	if s.usage != nil {
		s.usage.Record(usage.Record{
			Time:             time.Now(),
			Model:            req.Model,
			Account:          acctName,
			Endpoint:         r.URL.Path,
			APIKey:           keyName,
			Project:          project,
			PromptTokens:     finalUsage.PromptTokens,
			CompletionTokens: finalUsage.CompletionTokens,
			TotalTokens:      finalUsage.TotalTokens,
			Estimated:        estimated,
			Status:           http.StatusOK,
			DurationMs:       durMs,
		})
	}
	s.logs.Add(logbuf.Entry{
		Level:      logbuf.LevelInfo,
		Kind:       "request",
		Method:     r.Method,
		Path:       r.URL.Path,
		Model:      req.Model,
		Effort:     effort,
		Project:    project,
		Account:    acctName,
		Status:     http.StatusOK,
		DurationMs: durMs,
		TokensIn:   finalUsage.PromptTokens,
		TokensOut:  finalUsage.CompletionTokens,
	})
}

func buildOpenAIResponseObj(respID, msgID, model, text string, createdAt int64, u backend.Usage) map[string]any {
	return map[string]any{
		"id":          respID,
		"object":      "response",
		"created_at":  createdAt,
		"status":      "completed",
		"model":       model,
		"output_text": text,
		"output": []map[string]any{
			{
				"id":     msgID,
				"type":   "message",
				"role":   "assistant",
				"status": "completed",
				"content": []map[string]any{
					{
						"type": "output_text",
						"text": text,
					},
				},
			},
		},
		"usage": map[string]int{
			"input_tokens":  u.PromptTokens,
			"output_tokens": u.CompletionTokens,
			"total_tokens":  u.TotalTokens,
		},
	}
}
