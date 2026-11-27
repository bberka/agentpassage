package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type Message struct {
	Role        string       `json:"role"`
	Content     string       `json:"content"`
	Attachments []Attachment `json:"-"` // images, audio and files sent with the message
}

type ChatRequest struct {
	Model             string
	UpstreamModel     string
	ReasoningEffort   string // optional thinking/effort level: minimal, low, medium, high, xhigh, max, ultra
	PermissionMode    string // optional per-request override ("auto-approve", "deny", "ask") from ACP host
	ACPMode           string // optional ACP session mode override (e.g. "default", "acceptEdits", "auto", "bypassPermissions", "plan")
	PermissionHandler func(ctx context.Context, req PermissionRequest) PermissionDecision
	MCPServers        []any
	// HostTools adds the MCP servers from the user's own agent config. Only
	// local ACP hosts set it, so API callers can't use those servers.
	HostTools     bool
	Messages      []Message
	Stream        bool
	Temperature   *float64
	MaxTokens     int
	SessionKey    string
	Cwd           string
	HasTools      bool
	RawOpenAIBody []byte // preserved for transparent OpenAI HTTP pass-through
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// CacheReadTokens and CacheWriteTokens are the parts of PromptTokens read
	// from or written to the provider's prompt cache.
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

// UnmarshalJSON also reads the cache fields OpenAI-compatible providers send.
func (u *Usage) UnmarshalJSON(b []byte) error {
	type plain Usage
	var v struct {
		plain
		Details struct {
			Cached int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		DeepSeekHit int `json:"prompt_cache_hit_tokens"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*u = Usage(v.plain)
	if u.CacheReadTokens == 0 {
		u.CacheReadTokens = max(v.Details.Cached, v.DeepSeekHit)
	}
	return nil
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
	RawACPUpdate json.RawMessage // raw upstream ACP session/update payload (thoughts, tools, plan, usage_update)
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
	ToolCallID  string
	Kind        string
	Title       string
	ToolName    string
	Arguments   string
	RawToolCall json.RawMessage
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
	switch {
	case (status == 401 || status == 403) && restrictedRe.MatchString(body):
		return ErrKindRestricted
	// 402 is an account that is out of credit, which another account may cover.
	case status == 402 || status == 429:
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

const maxParsedRetryAfterSeconds = 6 * 3600 // cap parsed cooldowns at 6 hours

var (
	resetCueRe = regexp.MustCompile(`(?i)(?:resets?\s+in|try\s+again\s+(?:in|after)|retry[- ]?after|retry\s+in|available\s+(?:in|again\s+in)|please\s+wait(?:\s+for)?|wait\s+(?:for\s+)?|cooldown(?:\s+of)?)\s*:?\s*`)
	durPartRe  = regexp.MustCompile(`(?i)^\s*(?:(?:and|,)\s*)?(\d+)\s*([a-z]+)`)
	bareSecRe  = regexp.MustCompile(`^\s*(\d+)\b`)
)

// ParseResetDurationSeconds extracts a rate-limit/quota reset countdown from an error message
// (e.g. "Resets in 48m24s", "try again in 2h 15m", "retry after 90s", "wait 5 minutes") and
// returns the duration in seconds, capped at 6 hours. Returns 0 if no reset duration is found.
func ParseResetDurationSeconds(msg string) int {
	if strings.TrimSpace(msg) == "" {
		return 0
	}
	locs := resetCueRe.FindAllStringIndex(msg, -1)
	for _, loc := range locs {
		cue := strings.ToLower(msg[loc[0]:loc[1]])
		rest := msg[loc[1]:]
		if secs := parseLeadingDuration(rest); secs > 0 {
			if secs > maxParsedRetryAfterSeconds {
				return maxParsedRetryAfterSeconds
			}
			return secs
		}
		// Bare integer seconds right after "retry-after:" / "retry after "
		if strings.Contains(cue, "retry") {
			if m := bareSecRe.FindStringSubmatch(rest); len(m) == 2 {
				if n, err := strconv.Atoi(m[1]); err == nil && n > 0 {
					if n > maxParsedRetryAfterSeconds {
						return maxParsedRetryAfterSeconds
					}
					return n
				}
			}
		}
	}
	return 0
}

func parseLeadingDuration(s string) int {
	total := 0
	matched := false
	for {
		loc := durPartRe.FindStringSubmatchIndex(s)
		if loc == nil {
			break
		}
		n, err := strconv.Atoi(s[loc[2]:loc[3]])
		if err != nil {
			break
		}
		unit := strings.ToLower(s[loc[4]:loc[5]])
		switch unit {
		case "d", "day", "days":
			total += n * 86400
		case "h", "hr", "hrs", "hour", "hours":
			total += n * 3600
		case "m", "min", "mins", "minute", "minutes":
			total += n * 60
		case "s", "sec", "secs", "second", "seconds":
			total += n
		default:
			if !matched {
				return 0
			}
			return total
		}
		matched = true
		s = s[loc[1]:]
	}
	if !matched {
		return 0
	}
	return total
}

// StreamCallback receives streaming deltas. Returning an error stops streaming.
type StreamCallback func(StreamChunk) error
