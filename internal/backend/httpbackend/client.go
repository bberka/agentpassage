package httpbackend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
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

var scanBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 64*1024)
		return &b
	},
}

type Client struct {
	httpClient *http.Client
	// How long a provider may stay silent, before or during its answer.
	streamIdle time.Duration
	replyIdle  time.Duration
	hookMu     sync.RWMutex
	onHeaders  func(config.AccountConfig, http.Header)
}

func New() *Client {
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          128,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	return &Client{
		streamIdle: 5 * time.Minute,
		replyIdle:  10 * time.Minute, // a whole non-streaming answer is silent until done
		httpClient: &http.Client{
			Transport: tr,
			Timeout:   0, // per-request context controls deadline; streaming can be long-lived
		},
	}
}

// post sends a JSON request to path under the account's base URL and reports
// the response headers to the header hook. The caller closes the body.
func (c *Client) post(ctx context.Context, acct config.AccountConfig, apiKey, path string, body []byte, accept string) (*http.Response, error) {
	limit := c.replyIdle
	if accept == "text/event-stream" {
		limit = c.streamIdle
	}
	ctx, watch := watchStall(ctx, limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, trimBaseURL(acct.BaseURL)+path, bytes.NewReader(body))
	if err != nil {
		watch.stop()
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	applyAuth(req, acct, apiKey)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		err = watch.explain(err)
		watch.stop()
		return nil, &backend.BackendError{Kind: backend.ErrKindTransient, Message: err.Error(), Cause: err}
	}
	watch.body = resp.Body
	resp.Body = watch
	c.emitHeaders(acct, resp.Header)
	return resp, nil
}

// readBody reads up to limit bytes of a response, turning a read failure or a
// non-2xx status into an error.
func readBody(resp *http.Response, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, &backend.BackendError{Kind: backend.ErrKindTransient, Message: err.Error(), Cause: err}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, makeHTTPError(resp, body)
	}
	return body, nil
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
		var bare []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err2 := json.Unmarshal(body, &bare); err2 != nil {
			return nil, fmt.Errorf("decode /models response: %w", err)
		}
		parsed.Models = bare
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

func (c *Client) Complete(ctx context.Context, acct config.AccountConfig, apiKey string, req backend.ChatRequest) (resp *backend.ChatResponse, err error) {
	defer func() {
		if err == nil {
			backend.RecordAnswer(req.SessionKey, acct.ID)
		}
	}()
	if acct.AuthStyle == config.AuthAnthropic {
		return c.completeAnthropic(ctx, acct, apiKey, req)
	}
	return c.completeOpenAI(ctx, acct, apiKey, req)
}

func (c *Client) Stream(ctx context.Context, acct config.AccountConfig, apiKey string, req backend.ChatRequest, cb backend.StreamCallback) (err error) {
	defer func() {
		if err == nil {
			backend.RecordAnswer(req.SessionKey, acct.ID)
		}
	}()
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
		"messages": openAIMessages(req.Messages),
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
	resp, err := c.post(ctx, acct, apiKey, "/chat/completions", bodyBytes, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := readBody(resp, 16<<20)
	if err != nil {
		return nil, err
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
	resp, err := c.post(ctx, acct, apiKey, "/chat/completions", bodyBytes, "text/event-stream")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		return makeHTTPError(resp, respBody)
	}

	bufPtr := scanBufPool.Get().(*[]byte)
	defer scanBufPool.Put(bufPtr)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(*bufPtr, 4*1024*1024)
	finished := false
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
			Error *providerError `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		if ev.Error != nil {
			return ev.Error.backendError()
		}
		chunk := backend.StreamChunk{Usage: ev.Usage}
		if len(ev.Choices) > 0 {
			chunk.Delta = ev.Choices[0].Delta.Content
			chunk.Reasoning = ev.Choices[0].Delta.ReasoningContent
			chunk.FinishReason = ev.Choices[0].FinishReason
			finished = finished || chunk.FinishReason != ""
		}
		if chunk.Delta != "" || chunk.Reasoning != "" || chunk.FinishReason != "" || chunk.Usage != nil {
			if err := cb(chunk); err != nil {
				return err
			}
		}
	}
	return streamEnd(ctx, finished, scanner.Err())
}

// --- Outbound Anthropic Messages API translation ---

func buildAnthropicPayload(req backend.ChatRequest, stream bool) ([]byte, error) {
	var systemParts []string
	msgs := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == "system" {
			systemParts = append(systemParts, m.Content)
			continue
		}
		role := m.Role
		if role != "assistant" {
			role = "user"
		}
		msgs = append(msgs, map[string]any{
			"role":    role,
			"content": anthropicContent(m),
		})
	}
	if len(msgs) == 0 {
		msgs = append(msgs, map[string]any{"role": "user", "content": ""})
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

// anthropicUsage is the Messages API usage block, where input_tokens counts
// only the uncached input.
type anthropicUsage struct {
	InputTokens   int `json:"input_tokens"`
	OutputTokens  int `json:"output_tokens"`
	CacheRead     int `json:"cache_read_input_tokens"`
	CacheCreation int `json:"cache_creation_input_tokens"`
}

func (a anthropicUsage) usage() backend.Usage {
	prompt := a.InputTokens + a.CacheRead + a.CacheCreation
	return backend.Usage{
		PromptTokens:     prompt,
		CompletionTokens: a.OutputTokens,
		TotalTokens:      prompt + a.OutputTokens,
		CacheReadTokens:  a.CacheRead,
		CacheWriteTokens: a.CacheCreation,
	}
}

func (c *Client) completeAnthropic(ctx context.Context, acct config.AccountConfig, apiKey string, req backend.ChatRequest) (*backend.ChatResponse, error) {
	bodyBytes, err := buildAnthropicPayload(req, false)
	if err != nil {
		return nil, err
	}
	resp, err := c.post(ctx, acct, apiKey, "/messages", bodyBytes, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := readBody(resp, 16<<20)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		ID         string `json:"id"`
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Usage anthropicUsage `json:"usage"`
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
		Usage:        parsed.Usage.usage(),
	}, nil
}

func (c *Client) streamAnthropic(ctx context.Context, acct config.AccountConfig, apiKey string, req backend.ChatRequest, cb backend.StreamCallback) error {
	bodyBytes, err := buildAnthropicPayload(req, true)
	if err != nil {
		return err
	}
	resp, err := c.post(ctx, acct, apiKey, "/messages", bodyBytes, "text/event-stream")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		return makeHTTPError(resp, respBody)
	}

	bufPtr := scanBufPool.Get().(*[]byte)
	defer scanBufPool.Put(bufPtr)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(*bufPtr, 4*1024*1024)
	var eventName string
	var started anthropicUsage
	finished := false
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
		case "message_start":
			var ev struct {
				Message struct {
					Usage anthropicUsage `json:"usage"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err == nil {
				started = ev.Message.Usage
			}
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
					finished = true
					started.OutputTokens = ev.Usage.OutputTokens
					u := started.usage()
					if err := cb(backend.StreamChunk{
						FinishReason: mapAnthropicStopReason(ev.Delta.StopReason),
						Usage:        &u,
					}); err != nil {
						return err
					}
				}
			}
		case "message_stop":
			return nil
		case "error":
			var ev struct {
				Error providerError `json:"error"`
			}
			_ = json.Unmarshal([]byte(data), &ev)
			return ev.Error.backendError()
		}
	}
	return streamEnd(ctx, finished, scanner.Err())
}
