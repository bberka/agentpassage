package pool

import (
	"errors"
	"testing"
	"time"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
)

func newQuotaTestManager(t *testing.T) (*Manager, *time.Time) {
	t.Helper()
	m := NewManager([]config.AccountConfig{
		{ID: "agy-a", Name: "a", Kind: config.KindACP, Backend: "agy", Enabled: true, MaxConcurrency: 4},
		{ID: "agy-b", Name: "b", Kind: config.KindACP, Backend: "agy", Enabled: true, MaxConcurrency: 4},
	})
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m.SetNowFunc(func() time.Time { return now })
	return m, &now
}

func acquireID(t *testing.T, m *Manager, req SelectRequest) string {
	t.Helper()
	l, err := m.Acquire(req)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	l.Release()
	return l.Account.ID
}

func TestNewSessionSkipsExhaustedAccount(t *testing.T) {
	m, now := newQuotaTestManager(t)
	req := SelectRequest{
		CandidateIDs:   []string{"agy-a", "agy-b"},
		SessionKey:     "new",
		QuotaExhausted: map[string]time.Time{"agy-a": now.Add(time.Hour)},
	}
	if got := acquireID(t, m, req); got != "agy-b" {
		t.Fatalf("new session picked %s, want agy-b", got)
	}

	// Stale quota data: when every account looks used up, still try one.
	req.QuotaExhausted["agy-b"] = time.Time{}
	if got := acquireID(t, m, req); got != "agy-a" {
		t.Fatalf("all exhausted picked %s, want agy-a", got)
	}
}

func TestExistingSessionWaitsForReset(t *testing.T) {
	m, now := newQuotaTestManager(t)
	m.PinSession("s1", "agy-a")
	reset := now.Add(90 * time.Minute)
	req := SelectRequest{
		CandidateIDs:   []string{"agy-a", "agy-b"},
		SessionKey:     "s1",
		QuotaExhausted: map[string]time.Time{"agy-a": reset},
	}
	_, err := m.Acquire(req)
	if !errors.Is(err, ErrSessionWaitForReset) {
		t.Fatalf("expected ErrSessionWaitForReset, got %v", err)
	}
	be, ok := backend.AsBackendError(err)
	if !ok || be.Kind != backend.ErrKindRateLimit || be.StatusCode != 429 || be.RetryAfter != 5400 {
		t.Fatalf("unexpected wait error %+v", be)
	}

	// Opt-in switches the conversation to another account instead.
	req.SwitchExhaustedSessions = true
	if got := acquireID(t, m, req); got != "agy-b" {
		t.Fatalf("switch opt-in picked %s, want agy-b", got)
	}
}

func TestExistingSessionWaitsOutRateLimitCooldown(t *testing.T) {
	m, _ := newQuotaTestManager(t)
	m.PinSession("s1", "agy-a")
	rule := config.RoutingRule{Strategy: config.StrategyFailover, AllowCrossAccountRetryOn429: true}
	m.RecordErrorForGroup("agy-a", "gemini", &backend.BackendError{Kind: backend.ErrKindRateLimit, RetryAfter: 120}, rule)

	// The gateway retries with the failed account excluded; the conversation must not move.
	req := SelectRequest{CandidateIDs: []string{"agy-a", "agy-b"}, SessionKey: "s1", QuotaGroup: "gemini", ExcludeIDs: map[string]bool{"agy-a": true}}
	if _, err := m.Acquire(req); !errors.Is(err, ErrSessionWaitForReset) {
		t.Fatalf("expected wait during cooldown, got %v", err)
	}
	// Another model group on the same account is unaffected.
	req.QuotaGroup = "claude-gpt"
	req.ExcludeIDs = nil
	if got := acquireID(t, m, req); got != "agy-a" {
		t.Fatalf("claude-gpt request picked %s, want agy-a", got)
	}
	// A new conversation fails over freely.
	req = SelectRequest{CandidateIDs: []string{"agy-a", "agy-b"}, SessionKey: "fresh", QuotaGroup: "gemini"}
	if got := acquireID(t, m, req); got != "agy-b" {
		t.Fatalf("new session picked %s, want agy-b", got)
	}
}

func TestExistingSessionStaysOnItsAccount(t *testing.T) {
	m, _ := newQuotaTestManager(t)
	// The conversation moved to agy-b earlier; failover would prefer agy-a again.
	m.PinSession("s1", "agy-b")
	req := SelectRequest{CandidateIDs: []string{"agy-a", "agy-b"}, SessionKey: "s1", QuotaThresholdPct: 50, QuotaUsedPct: map[string]float64{"agy-a": 10, "agy-b": 80}}
	if got := acquireID(t, m, req); got != "agy-b" {
		t.Fatalf("existing session picked %s, want agy-b", got)
	}
	// A pin on an account outside this route's candidates does not count.
	m.PinSession("s2", "agy-b")
	req = SelectRequest{CandidateIDs: []string{"agy-a"}, SessionKey: "s2", QuotaExhausted: map[string]time.Time{"agy-a": {}}}
	if got := acquireID(t, m, req); got != "agy-a" {
		t.Fatalf("got %s", got)
	}
}
