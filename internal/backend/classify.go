package backend

import (
	"regexp"
	"strings"
)

// These patterns decide whether apx disables an account or marks it signed
// out, so they match whole phrases, not words that merely appear in an error.
var (
	restrictedRe = regexp.MustCompile(`(?i)\b(account|organi[sz]ation|user)\b[\s:]+(\w+\s+){0,3}?(restricted|suspended|banned|disabled|deactivated|terminated)\b` +
		`|\b(restricted|suspended|banned|disabled|terminated)\b.{0,80}\bterms of service\b` +
		`|\bterms of service\b.{0,80}\b(restricted|suspended|banned|disabled|terminated)\b`)
	rateLimitRe = regexp.MustCompile(`(?i)\b429\b|rate.?limit|too many requests|quota|usage limit|resource.?exhausted`)
	signedOutRe = regexp.MustCompile(`(?i)\b40[13]\b|unauthenticated|unauthori[sz]ed|not (logged|signed) in` +
		`|(log|sign)[ -]?in required|please (log|sign) ?in|authentication.?(required|failed|error|fails)` +
		`|invalid.{0,20}(api.?key|credentials|auth\w*|access token|refresh token)|(token|session|credentials?) (has |have |is )?(expired|revoked)` +
		`|auth_required`)
)

// ClassifyAgentError reads the error text of an ACP or CLI agent, which has no
// HTTP status to go by.
func ClassifyAgentError(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := AsBackendError(err); ok {
		return err
	}
	msg := err.Error()
	switch {
	case restrictedRe.MatchString(msg):
		return &BackendError{Kind: ErrKindRestricted, StatusCode: 403, Message: msg, Cause: err}
	case rateLimitRe.MatchString(msg):
		return &BackendError{Kind: ErrKindRateLimit, StatusCode: 429, Message: msg, RetryAfter: ParseResetDurationSeconds(msg), Cause: err}
	case signedOutRe.MatchString(msg):
		return &BackendError{Kind: ErrKindAuth, StatusCode: 401, Message: msg, Cause: err}
	default:
		return &BackendError{Kind: ErrKindTransient, StatusCode: 502, Message: strings.TrimSpace(msg), Cause: err}
	}
}
