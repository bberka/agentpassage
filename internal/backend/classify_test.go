package backend

import (
	"errors"
	"testing"
)

func TestClassifyAgentError(t *testing.T) {
	for msg, want := range map[string]ErrorKind{
		"Your account has been suspended": ErrKindRestricted,
		"account banned":                  ErrKindRestricted,
		"This service has been disabled for violation of the Terms of Service":  ErrKindRestricted,
		"RPC error -32603: write to /home/x/restricted/file: permission denied": ErrKindTransient,
		"tool failed: the banned word list is empty":                            ErrKindTransient,
		"see our terms of service":                                              ErrKindTransient,
		"RPC error -32000: Authentication required":                             ErrKindAuth,
		"You are not logged in. Please log in.":                                 ErrKindAuth,
		"OAuth token has expired":                                               ErrKindAuth,
		"unexpected status 401":                                                 ErrKindAuth,
		"failed to read author list":                                            ErrKindTransient,
		"dial tcp 127.0.0.1:4013: connection refused":                           ErrKindTransient,
		"OAuth callback server could not start":                                 ErrKindTransient,
		"HTTP 429: rate limit reached, try again in 5m":                         ErrKindRateLimit,
		"RESOURCE_EXHAUSTED: quota reached":                                     ErrKindRateLimit,
	} {
		be, ok := AsBackendError(ClassifyAgentError(errors.New(msg)))
		if !ok || be.Kind != want {
			t.Errorf("%q classified as %v, want %s", msg, be.Kind, want)
		}
	}
}

func TestClassifyHTTPStatus(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   ErrorKind
	}{
		{400, "your prompt violates our terms of service and was disabled", ErrKindClient},
		{404, "account disabled", ErrKindClient},
		{403, "review the terms of service", ErrKindAuth},
		{403, "Your account has been disabled", ErrKindRestricted},
		{402, "Insufficient Balance", ErrKindRateLimit},
		{429, "slow down", ErrKindRateLimit},
		{503, "overloaded", ErrKindTransient},
	} {
		if got := ClassifyHTTPStatus(tc.status, tc.body); got != tc.want {
			t.Errorf("HTTP %d %q classified as %s, want %s", tc.status, tc.body, got, tc.want)
		}
	}
}
