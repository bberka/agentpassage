package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/logbuf"
	"github.com/bberka/agentpassage/internal/router"
	"github.com/bberka/agentpassage/internal/usage"
)

// storedResponse is what a follow-up request needs to continue a response.
type storedResponse struct {
	messages []backend.Message // without instructions, which don't carry over
	session  string
}

type responsesStore struct {
	mu      sync.Mutex
	history map[string]storedResponse
	order   []string
}

var globalRespStore = &responsesStore{history: make(map[string]storedResponse)}

func (rs *responsesStore) get(id string) (storedResponse, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	h, ok := rs.history[id]
	h.messages = slices.Clone(h.messages)
	return h, ok
}

func (rs *responsesStore) put(id string, h storedResponse) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if _, exists := rs.history[id]; !exists {
		rs.order = append(rs.order, id)
		if len(rs.order) > 500 {
			delete(rs.history, rs.order[0])
			rs.order = rs.order[1:]
		}
	}
	h.messages = slices.Clone(h.messages)
	rs.history[id] = h
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
		switch role {
		case "":
			role = "user"
		case "developer": // many chat completions providers reject this role
			role = "system"
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
	var prev storedResponse
	if id := strings.TrimSpace(req.PreviousResponseID); id != "" {
		var ok bool
		if prev, ok = globalRespStore.get(sessionScope(principal, id)); !ok {
			writeOpenAIError(w, http.StatusNotFound, "previous_response_not_found",
				fmt.Sprintf("Previous response %q is unknown or expired. Send the full input without previous_response_id.", id))
			return
		}
	}
	history := append(prev.messages, parseResponsesInput(req.Input)...)
	msgs := history
	if strings.TrimSpace(req.Instructions) != "" {
		msgs = append([]backend.Message{{Role: "system", Content: req.Instructions}}, history...)
	}
	if len(msgs) == 0 {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "input is required")
		return
	}

	effort := ""
	if req.Reasoning != nil {
		effort = router.NormalizeEffort(req.Reasoning.Effort)
	}
	project := projectFromRequest(r)

	respID := fmt.Sprintf("resp_%d", time.Now().UnixNano())
	msgID := fmt.Sprintf("msg_%d", time.Now().UnixNano())
	createdAt := time.Now().Unix()

	// A conversation keeps one session across its chain of responses.
	session := sessionScope(principal, headerFirst(r, "X-Apx-Session", "X-Acpx-Session"))
	if session == "" {
		session = prev.session
	}
	shared := false
	if session == "" {
		session = sessionScope(principal, strings.TrimSpace(req.User))
		shared = session != ""
	}
	if session == "" {
		session = sessionScope(principal, respID)
	}

	chatReq := backend.ChatRequest{
		Model:           req.Model,
		ReasoningEffort: effort,
		Messages:        msgs,
		Stream:          req.Stream,
		Temperature:     req.Temperature,
		MaxTokens:       req.MaxOutputTokens,
		SessionKey:      session,
		SharedSession:   shared,
		Cwd:             headerFirst(r, "X-Apx-Cwd", "X-Acpx-Cwd"),
	}
	if resolved, _, ok := s.ResolveDecisionModel(req.Model, chatReq); ok && resolved != "" {
		chatReq.Model = resolved
	}
	plan, err := s.router.ResolveForProject(chatReq.Model, project)
	if err != nil {
		s.logRequest(r, req.Model, "", http.StatusNotFound, start, err)
		writeOpenAIError(w, http.StatusNotFound, "model_not_found", err.Error())
		return
	}
	if loopErr := checkLoopGuard(r, plan); loopErr != nil {
		s.logRequest(r, req.Model, plan.Backend, http.StatusLoopDetected, start, loopErr)
		writeOpenAIError(w, http.StatusLoopDetected, "loop_detected", loopErr.Error())
		return
	}
	if accessErr := s.agentAccessError(principal, plan); accessErr != nil {
		s.logRequest(r, req.Model, "", http.StatusForbidden, start, accessErr)
		writeOpenAIError(w, http.StatusForbidden, "model_not_allowed", accessErr.Error())
		return
	}

	var flusher http.Flusher
	if req.Stream {
		var ok bool
		if flusher, ok = w.(http.Flusher); !ok {
			writeOpenAIError(w, http.StatusInternalServerError, "server_error", "streaming unsupported")
			return
		}
	}
	writeRespSSE := func(event string, data any) {
		b, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		flusher.Flush()
	}
	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
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
		// Clients such as Codex ignore text deltas until the item they belong to is open.
		writeRespSSE("response.output_item.added", map[string]any{
			"type":         "response.output_item.added",
			"output_index": 0,
			"item": map[string]any{
				"id": msgID, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{},
			},
		})
		writeRespSSE("response.content_part.added", map[string]any{
			"type": "response.content_part.added", "item_id": msgID, "output_index": 0, "content_index": 0,
			"part": map[string]any{"type": "output_text", "text": ""},
		})
	}

	var textSB strings.Builder
	res, err := s.streamPlan(r.Context(), plan, project, chatReq, func(ch backend.StreamChunk) error {
		if ch.Delta != "" {
			textSB.WriteString(ch.Delta)
			if req.Stream {
				writeRespSSE("response.output_text.delta", map[string]any{
					"type":          "response.output_text.delta",
					"item_id":       msgID,
					"output_index":  0,
					"content_index": 0,
					"delta":         ch.Delta,
				})
			}
		}
		return nil
	})
	replyText := textSB.String()

	var u backend.Usage
	if res.usage != nil {
		u = *res.usage
	}
	estimated := false
	if err == nil || res.emitted {
		if u.PromptTokens == 0 {
			u.PromptTokens = estimateMessagesTokens(msgs)
			estimated = true
		}
		if u.CompletionTokens == 0 {
			u.CompletionTokens = usage.EstimateTokens(replyText)
			estimated = true
		}
		if u.TotalTokens == 0 {
			u.TotalTokens = u.PromptTokens + u.CompletionTokens
		}
	}

	status := http.StatusOK
	if err != nil {
		status, err = finalFailure(err)
	}
	model := res.model
	if model == "" {
		model = req.Model
	}
	s.recordUsageForRequest(r, model, res.account, res.backendID, status, start, u, estimated)
	entry := logbuf.Entry{
		Level:      logbuf.LevelInfo,
		Kind:       "request",
		Method:     r.Method,
		Path:       r.URL.Path,
		Model:      req.Model,
		Effort:     effort,
		Project:    project,
		Account:    res.account,
		Status:     status,
		DurationMs: time.Since(start).Milliseconds(),
		TokensIn:   u.PromptTokens,
		TokensOut:  u.CompletionTokens,
	}
	if err != nil {
		entry.Level, entry.Error = logbuf.LevelError, err.Error()
	}
	if s.logs != nil {
		s.logs.Add(entry)
	}

	if err != nil {
		if req.Stream {
			writeRespSSE("response.failed", map[string]any{
				"type":  "response.failed",
				"error": map[string]any{"message": err.Error()},
			})
			return
		}
		writeOpenAIError(w, status, "api_error", err.Error())
		return
	}

	globalRespStore.put(sessionScope(principal, respID), storedResponse{
		messages: append(history, backend.Message{Role: "assistant", Content: replyText}),
		session:  session,
	})
	obj := buildOpenAIResponseObj(respID, msgID, req.Model, replyText, createdAt, u)
	if !req.Stream {
		writeJSON(w, http.StatusOK, obj)
		return
	}
	writeRespSSE("response.output_text.done", map[string]any{
		"type": "response.output_text.done", "item_id": msgID, "output_index": 0, "content_index": 0, "text": replyText,
	})
	writeRespSSE("response.content_part.done", map[string]any{
		"type": "response.content_part.done", "item_id": msgID, "output_index": 0, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": replyText},
	})
	writeRespSSE("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "output_index": 0,
		"item": obj["output"].([]map[string]any)[0],
	})
	writeRespSSE("response.completed", map[string]any{"type": "response.completed", "response": obj})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
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
