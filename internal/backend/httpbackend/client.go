package httpbackend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
)

const anthropicVersion = "2023-06-01"

type Client struct {
	httpClient *http.Client
	hookMu     sync.RWMutex
	onHeaders  func(config.AccountConfig, http.Header)
}

func New() *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 0, // per-request context controls deadline; streaming can be long-lived
		},
	}
}

func (c *Client) SetHTTPClient(hc *http.Client) {
	if hc != nil {
		c.httpClient = hc
	}
}

// SetHeaderHook registers a callback invoked with upstream HTTP response headers
// (used to capture live provider rate-limit windows such as Anthropic 5h/7d or x-ratelimit-*).
func (c *Client) SetHeaderHook(fn func(config.AccountConfig, http.Header)) {
	c.hookMu.Lock()
	c.onHeaders = fn
	c.hookMu.Unlock()
}

func (c *Client) emitHeaders(acct config.AccountConfig, h http.Header) {
	if h == nil {
		return
	}
	c.hookMu.RLock()
	fn := c.onHeaders
	c.hookMu.RUnlock()
	if fn != nil {
		fn(acct, h)
	}
}

func applyAuth(req *http.Request, acct config.AccountConfig, apiKey string) {
	switch acct.AuthStyle {
	case config.AuthAnthropic:
		if apiKey != "" {
			req.Header.Set("x-api-key", apiKey)
		}
		req.Header.Set("anthropic-version", anthropicVersion)
	case config.AuthNone:
		// No auth header needed (e.g. local Ollama).
	default:
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}
}

func trimBaseURL(base string) string {
	return strings.TrimRight(strings.TrimSpace(base), "/")
}

func parseRetryAfter(h string) int {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil && secs > 0 {
		return secs
	}
	if t, err := http.ParseTime(h); err == nil {
		d := int(time.Until(t).Seconds())
		if d > 0 {
			return d
		}
	}
	return 0
}

func makeHTTPError(resp *http.Response, body []byte) *backend.BackendError {
	msg := strings.TrimSpace(string(body))
	if len(msg) > 400 {
		msg = msg[:400] + "..."
	}
	// Try to extract structured error message from OpenAI/Anthropic JSON error payloads.
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error.Message != "" {
		msg = parsed.Error.Message
	}
	kind := backend.ClassifyHTTPStatus(resp.StatusCode, string(body))
	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
	if retryAfter == 0 && kind == backend.ErrKindRateLimit {
		retryAfter = backend.ParseResetDurationSeconds(msg)
	}
	return &backend.BackendError{
		Kind:       kind,
		StatusCode: resp.StatusCode,
		Message:    msg,
		RetryAfter: retryAfter,
	}
}

// FetchModels fetches available model IDs from GET <baseURL>/models.
func (c *Client) FetchModels(ctx context.Context, acct config.AccountConfig, apiKey string) ([]string, error) {
	base := trimBaseURL(acct.BaseURL)
	if base == "" {
		return nil, fmt.Errorf("base URL is empty")
	}
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	applyAuth(req, acct, apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, &backend.BackendError{Kind: backend.ErrKindTransient, Message: err.Error(), Cause: err}
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, makeHTTPError(resp, body)
	}

	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode /models response: %w", err)
	}
	seen := make(map[string]bool)
	models := make([]string, 0, len(parsed.Data)+len(parsed.Models))
	for _, m := range parsed.Data {
		if m.ID != "" && !seen[m.ID] {
			seen[m.ID] = true
			models = append(models, m.ID)
		}
	}
	for _, m := range parsed.Models {
		id := m.ID
		if id == "" {
			id = m.Name
		}
		if id != "" && !seen[id] {
			seen[id] = true
			models = append(models, id)
		}
	}
	sort.Strings(models)
	return models, nil
}

// TestAccount checks that the endpoint and API key work with a single request.
func (c *Client) TestAccount(ctx context.Context, acct config.AccountConfig, apiKey string) ([]string, error) {
	models, err := c.FetchModels(ctx, acct, apiKey)
	if err == nil {
		return models, nil
	}
	// If /models returned 404 (some compatible endpoints omit /models), try a tiny completion if we have a model.
	if be, ok := backend.AsBackendError(err); ok && be.StatusCode == http.StatusNotFound && len(acct.Models) > 0 {
		testCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		_, err2 := c.Complete(testCtx, acct, apiKey, backend.ChatRequest{
			Model:         acct.Models[0],
			UpstreamModel: acct.Models[0],
			Messages:      []backend.Message{{Role: "user", Content: "ping"}},
			MaxTokens:     1,
		})
		if err2 == nil {
			return acct.Models, nil
		}
		return nil, err2
	}
	return nil, err
}

func (c *Client) Complete(ctx context.Context, acct config.AccountConfig, apiKey string, req backend.ChatRequest) (*backend.ChatResponse, error) {
	if acct.AuthStyle == config.AuthAnthropic {
		return c.completeAnthropic(ctx, acct, apiKey, req)
	}
	return c.completeOpenAI(ctx, acct, apiKey, req)
}

func (c *Client) Stream(ctx context.Context, acct config.AccountConfig, apiKey string, req backend.ChatRequest, cb backend.StreamCallback) error {
	if acct.AuthStyle == config.AuthAnthropic {
		return c.streamAnthropic(ctx, acct, apiKey, req, cb)
	}
	return c.streamOpenAI(ctx, acct, apiKey, req, cb)
}

func buildOpenAIPayload(req backend.ChatRequest, stream bool) ([]byte, error) {
	if len(req.RawOpenAIBody) > 0 {
		var raw map[string]any
		if err := json.Unmarshal(req.RawOpenAIBody, &raw); err == nil {
			raw["model"] = req.UpstreamModel
			raw["stream"] = stream
			return json.Marshal(raw)
		}
	}
	payload := map[string]any{
		"model":    req.UpstreamModel,
		"messages": req.Messages,
		"stream":   stream,
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	if req.ReasoningEffort != "" {
		payload["reasoning_effort"] = req.ReasoningEffort
	}
	return json.Marshal(payload)
}

func (c *Client) completeOpenAI(ctx context.Context, acct config.AccountConfig, apiKey string, req backend.ChatRequest) (*backend.ChatResponse, error) {
	bodyBytes, err := buildOpenAIPayload(req, false)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, trimBaseURL(acct.BaseURL)+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	applyAuth(httpReq, acct, apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, &backend.BackendError{Kind: backend.ErrKindTransient, Message: err.Error(), Cause: err}
	}
	defer resp.Body.Close()
	c.emitHeaders(acct, resp.Header)

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, &backend.BackendError{Kind: backend.ErrKindTransient, Message: err.Error(), Cause: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, makeHTTPError(resp, respBody)
	}

	var parsed struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage backend.Usage `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("decode completion response: %w", err)
	}
	out := &backend.ChatResponse{
		ID:           parsed.ID,
		Model:        req.Model,
		FinishReason: "stop",
		Usage:        parsed.Usage,
	}
	if len(parsed.Choices) > 0 {
		out.Content = parsed.Choices[0].Message.Content
		if parsed.Choices[0].FinishReason != "" {
			out.FinishReason = parsed.Choices[0].FinishReason
		}
	}
	return out, nil
}

func (c *Client) streamOpenAI(ctx context.Context, acct config.AccountConfig, apiKey string, req backend.ChatRequest, cb backend.StreamCallback) error {
	bodyBytes, err := buildOpenAIPayload(req, true)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, trimBaseURL(acct.BaseURL)+"/chat/completions", bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	applyAuth(httpReq, acct, apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return &backend.BackendError{Kind: backend.ErrKindTransient, Message: err.Error(), Cause: err}
	}
	defer resp.Body.Close()
	c.emitHeaders(acct, resp.Header)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		return makeHTTPError(resp, respBody)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			return nil
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage *backend.Usage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		chunk := backend.StreamChunk{Usage: ev.Usage}
		if len(ev.Choices) > 0 {
			chunk.Delta = ev.Choices[0].Delta.Content
			chunk.Reasoning = ev.Choices[0].Delta.ReasoningContent
			chunk.FinishReason = ev.Choices[0].FinishReason
		}
		if chunk.Delta != "" || chunk.Reasoning != "" || chunk.FinishReason != "" || chunk.Usage != nil {
			if err := cb(chunk); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return &backend.BackendError{Kind: backend.ErrKindTransient, Message: err.Error(), Cause: err}
	}
	return nil
}

// --- Outbound Anthropic Messages API translation ---

func buildAnthropicPayload(req backend.ChatRequest, stream bool) ([]byte, error) {
	var systemParts []string
	msgs := make([]map[string]string, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == "system" {
			systemParts = append(systemParts, m.Content)
			continue
		}
		role := m.Role
		if role != "assistant" {
			role = "user"
		}
		msgs = append(msgs, map[string]string{
			"role":    role,
			"content": m.Content,
		})
	}
	if len(msgs) == 0 {
		msgs = append(msgs, map[string]string{"role": "user", "content": ""})
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	payload := map[string]any{
		"model":      req.UpstreamModel,
		"messages":   msgs,
		"max_tokens": maxTokens,
		"stream":     stream,
	}
	if len(systemParts) > 0 {
		payload["system"] = strings.Join(systemParts, "\n\n")
	}
	if req.Temperature != nil {
		payload["temperature"] = *req.Temperature
	}
	return json.Marshal(payload)
}

func mapAnthropicStopReason(r string) string {
	switch r {
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return "stop"
	}
}

func (c *Client) completeAnthropic(ctx context.Context, acct config.AccountConfig, apiKey string, req backend.ChatRequest) (*backend.ChatResponse, error) {
	bodyBytes, err := buildAnthropicPayload(req, false)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, trimBaseURL(acct.BaseURL)+"/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	applyAuth(httpReq, acct, apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, &backend.BackendError{Kind: backend.ErrKindTransient, Message: err.Error(), Cause: err}
	}
	defer resp.Body.Close()
	c.emitHeaders(acct, resp.Header)

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, &backend.BackendError{Kind: backend.ErrKindTransient, Message: err.Error(), Cause: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, makeHTTPError(resp, respBody)
	}

	var parsed struct {
		ID         string `json:"id"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("decode anthropic response: %w", err)
	}
	var sb strings.Builder
	for _, b := range parsed.Content {
		if b.Type == "text" {
			sb.WriteString(b.Text)
		}
	}
	return &backend.ChatResponse{
		ID:           parsed.ID,
		Model:        req.Model,
		Content:      sb.String(),
		FinishReason: mapAnthropicStopReason(parsed.StopReason),
		Usage: backend.Usage{
			PromptTokens:     parsed.Usage.InputTokens,
			CompletionTokens: parsed.Usage.OutputTokens,
			TotalTokens:      parsed.Usage.InputTokens + parsed.Usage.OutputTokens,
		},
	}, nil
}

func (c *Client) streamAnthropic(ctx context.Context, acct config.AccountConfig, apiKey string, req backend.ChatRequest, cb backend.StreamCallback) error {
	bodyBytes, err := buildAnthropicPayload(req, true)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, trimBaseURL(acct.BaseURL)+"/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	applyAuth(httpReq, acct, apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return &backend.BackendError{Kind: backend.ErrKindTransient, Message: err.Error(), Cause: err}
	}
	defer resp.Body.Close()
	c.emitHeaders(acct, resp.Header)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		return makeHTTPError(resp, respBody)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var eventName string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		switch eventName {
		case "content_block_delta":
			var ev struct {
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err == nil && ev.Delta.Text != "" {
				if err := cb(backend.StreamChunk{Delta: ev.Delta.Text}); err != nil {
					return err
				}
			}
		case "message_delta":
			var ev struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err == nil {
				if ev.Delta.StopReason != "" {
					if err := cb(backend.StreamChunk{
						FinishReason: mapAnthropicStopReason(ev.Delta.StopReason),
						Usage:        &backend.Usage{CompletionTokens: ev.Usage.OutputTokens, TotalTokens: ev.Usage.OutputTokens},
					}); err != nil {
						return err
					}
				}
			}
		case "message_stop":
			return nil
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return &backend.BackendError{Kind: backend.ErrKindTransient, Message: err.Error(), Cause: err}
	}
	return nil
}
