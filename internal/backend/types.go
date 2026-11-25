package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ChatRequest struct {
	Model           string
	UpstreamModel   string
	ReasoningEffort string // optional thinking/effort level: minimal, low, medium, high, xhigh, max, ultra
	Messages        []Message
	Stream          bool
	Temperature     *float64
	MaxTokens       int
	SessionKey      string
	Cwd             string
	HasTools        bool
	RawOpenAIBody   []byte // preserved for transparent OpenAI HTTP pass-through
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ChatResponse struct {
	ID           string
	Model        string
	Content      string
	FinishReason string
	Usage        Usage
	ToolsIgnored bool
	SessionID    string
}

type StreamChunk struct {
	Delta        string
	Reasoning    string
	FinishReason string
	Usage        *Usage
	ToolsIgnored bool
	SessionID    string
	Err          error
}

type PermissionRequest struct {
	ID          string
	AccountID   string
	AccountName string
	SessionID   string
	Title       string
	ToolName    string
	Arguments   string
	Options     []PermissionOption
	ReplyCh     chan PermissionDecision
}

type PermissionOption struct {
	ID   string
	Name string
	Kind string
}

type PermissionDecision struct {
	Approved bool
	OptionID string
}

// ErrorKind classifies backend errors for pool health and retry rules.
type ErrorKind string

const (
	ErrKindTransient  ErrorKind = "transient"  // network error or 5xx -> failover allowed
	ErrKindRateLimit  ErrorKind = "rate_limit" // 429 -> cooldown, no subscription failover unless opted in
	ErrKindAuth       ErrorKind = "auth"       // 401/403 -> login needed / repeated -> disable
	ErrKindRestricted ErrorKind = "restricted" // account restricted/suspended -> disable immediately + banner
	ErrKindClient     ErrorKind = "client"     // 400/404/etc -> surface to caller, no health penalty
)

type BackendError struct {
	Kind       ErrorKind
	StatusCode int
	Message    string
	RetryAfter int // seconds, optional
	Cause      error
}

func (e *BackendError) Error() string {
	if e.StatusCode > 0 {
		return fmt.Sprintf("backend error (%s, HTTP %d): %s", e.Kind, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("backend error (%s): %s", e.Kind, e.Message)
}

func (e *BackendError) Unwrap() error { return e.Cause }

func ClassifyHTTPStatus(status int, body string) ErrorKind {
	lower := strings.ToLower(body)
	if strings.Contains(lower, "account restricted") ||
		strings.Contains(lower, "account suspended") ||
		strings.Contains(lower, "account banned") ||
		strings.Contains(lower, "account disabled") ||
		strings.Contains(lower, "terms of service") {
		return ErrKindRestricted
	}
	switch {
	case status == 429:
		return ErrKindRateLimit
	case status == 401 || status == 403:
		return ErrKindAuth
	case status >= 500:
		return ErrKindTransient
	default:
		return ErrKindClient
	}
}

func AsBackendError(err error) (*BackendError, bool) {
	var be *BackendError
	if errors.As(err, &be) {
		return be, true
	}
	return nil, false
}

// StreamCallback receives streaming deltas. Returning an error stops streaming.
type StreamCallback func(StreamChunk) error

// Executor executes chat completions against a specific account.
type Executor interface {
	Complete(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	Stream(ctx context.Context, req ChatRequest, cb StreamCallback) error
}
