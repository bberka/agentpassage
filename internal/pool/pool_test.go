package pool

import (
	"errors"
	"testing"
	"time"

	"github.com/bberka/acpx/internal/backend"
	"github.com/bberka/acpx/internal/config"
)

func TestStickyAndOptInSubscriptionGuard(t *testing.T) {
	accts := []config.AccountConfig{
		{ID: "agy-a", Name: "a", Kind: config.KindACP, Backend: "agy", Enabled: true, MaxConcurrency: 1},
		{ID: "agy-b", Name: "b", Kind: config.KindACP, Backend: "agy", Enabled: true, MaxConcurrency: 1},
	}
	m := NewManager(accts)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m.SetNowFunc(func() time.Time { return now })

	// First session gets a
	l1, err := m.Acquire(SelectRequest{Strategy: config.StrategySticky, CandidateIDs: []string{"agy-a", "agy-b"}, SessionKey: "s1"})
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	l1.Release()

	// New session gets LRU (b)
	now = now.Add(time.Second)
	l2, err := m.Acquire(SelectRequest{Strategy: config.StrategySticky, CandidateIDs: []string{"agy-a", "agy-b"}, SessionKey: "s2"})
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if l2.Account.ID == l1.Account.ID {
		t.Fatalf("expected new session to pick LRU account, both got %s", l2.Account.ID)
	}
	l2.Release()

	// Returning session s1 stays pinned to l1.Account.ID
	now = now.Add(time.Second)
	l3, err := m.Acquire(SelectRequest{Strategy: config.StrategySticky, CandidateIDs: []string{"agy-a", "agy-b"}, SessionKey: "s1"})
	if err != nil {
		t.Fatalf("acquire 3: %v", err)
	}
	if l3.Account.ID != l1.Account.ID {
		t.Fatalf("expected sticky session s1 -> %s, got %s", l1.Account.ID, l3.Account.ID)
	}
	l3.Release()

	// Round-robin on subscription accounts without opt-in must be rejected.
	_, err = m.Acquire(SelectRequest{Strategy: config.StrategyRoundRobin, CandidateIDs: []string{"agy-a", "agy-b"}, SubscriptionWarningAcked: false})
	if !errors.Is(err, ErrStrategyOptInNeeded) {
		t.Fatalf("expected ErrStrategyOptInNeeded, got %v", err)
	}

	// With opt-in acknowledged, round-robin succeeds.
	l4, err := m.Acquire(SelectRequest{Strategy: config.StrategyRoundRobin, CandidateIDs: []string{"agy-a", "agy-b"}, SubscriptionWarningAcked: true})
	if err != nil {
		t.Fatalf("expected round-robin with opt-in to succeed: %v", err)
	}
	l4.Release()
}

func TestHealthCooldownAuthDisableAndFailoverRules(t *testing.T) {
	accts := []config.AccountConfig{
		{ID: "agy-1", Name: "one", Kind: config.KindACP, Backend: "agy", Enabled: true, MaxConcurrency: 1},
		{ID: "agy-2", Name: "two", Kind: config.KindACP, Backend: "agy", Enabled: true, MaxConcurrency: 1},
	}
	m := NewManager(accts)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m.SetNowFunc(func() time.Time { return now })

	rule := config.RoutingRule{Pattern: "agy/*", Strategy: config.StrategyFailover}

	// 5xx -> failover allowed
	if !m.RecordError("agy-1", &backend.BackendError{Kind: backend.ErrKindTransient, StatusCode: 503, Message: "down"}, rule) {
		t.Fatalf("expected failover allowed on 5xx")
	}

	// 429 -> cooldown, NO failover on subscription account unless opted in
	if m.RecordError("agy-1", &backend.BackendError{Kind: backend.ErrKindRateLimit, StatusCode: 429, RetryAfter: 30}, rule) {
		t.Fatalf("expected NO failover on 429 for subscription account")
	}
	// Check agy-1 is cooling down
	_, err := m.Acquire(SelectRequest{Strategy: config.StrategyManual, CandidateIDs: []string{"agy-1"}})
	if err == nil {
		t.Fatalf("expected agy-1 ineligible during cooldown")
	}

	// Advance past cooldown
	now = now.Add(31 * time.Second)
	l, err := m.Acquire(SelectRequest{Strategy: config.StrategyManual, CandidateIDs: []string{"agy-1"}})
	if err != nil {
		t.Fatalf("expected agy-1 recovered after cooldown: %v", err)
	}
	l.Release()

	// Repeated 401 -> auto-disables account and emits banner
	m.RecordError("agy-2", &backend.BackendError{Kind: backend.ErrKindAuth, StatusCode: 401, Message: "unauthorized"}, rule)
	if len(m.Banners()) != 0 {
		t.Fatalf("first 401 should mark login_needed, not auto-disable yet")
	}
	m.RecordError("agy-2", &backend.BackendError{Kind: backend.ErrKindAuth, StatusCode: 401, Message: "unauthorized"}, rule)
	banners := m.Banners()
	if len(banners) != 1 || banners[0].AccountID != "agy-2" {
		t.Fatalf("expected health banner after repeated 401, got %+v", banners)
	}
}
