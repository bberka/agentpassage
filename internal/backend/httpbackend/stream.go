package httpbackend

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/bberka/agentpassage/internal/backend"
)

// providerError is the error object providers send inside a stream.
type providerError struct {
	Message string          `json:"message"`
	Type    string          `json:"type"`
	Code    json.RawMessage `json:"code"`
}

// backendError classifies an in-stream error, which has no HTTP status of its own.
func (e providerError) backendError() error {
	status := 0
	_ = json.Unmarshal(e.Code, &status)
	if status < 400 {
		switch e.Type {
		case "rate_limit_error":
			status = 429
		case "authentication_error", "permission_error":
			status = 401
		case "overloaded_error":
			status = 529
		case "invalid_request_error":
			status = 400
		}
	}
	if status < 400 {
		return backend.ClassifyAgentError(errors.New(e.Type + ": " + e.Message))
	}
	return &backend.BackendError{
		Kind:       backend.ClassifyHTTPStatus(status, e.Message),
		StatusCode: status,
		Message:    e.Message,
		RetryAfter: backend.ParseResetDurationSeconds(e.Message),
	}
}

// streamEnd is the result of a stream that stopped sending lines. One that
// never reported its end was cut off, so it must not count as an answer.
func streamEnd(ctx context.Context, finished bool, readErr error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case finished:
		return nil
	case readErr == nil:
		readErr = errors.New("upstream stream ended before the reply was complete")
	}
	return &backend.BackendError{Kind: backend.ErrKindTransient, StatusCode: 502, Message: readErr.Error(), Cause: readErr}
}
