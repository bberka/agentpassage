package pool

import (
	"testing"
	"time"

	"github.com/bberka/acpx/internal/backend"
	"github.com/bberka/acpx/internal/config"
)

func TestPrimaryFirstFailoverStickyAndDisabledRoundRobin(t *testing.T) {
	accts := []config.AccountConfig{
		{ID: "agy-a", Name: "a", Kind: config.KindACP, Backend: "agy", Enabled: true, MaxConcurrency: 1},
		{ID: "agy-b", Name: "b", Kind: config.KindACP, Backend: "agy", Enabled: true, MaxConcurrency: 1},
	}
	m := NewManager(accts)
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m.SetNowFunc(func() time.Time { return now })

	// Default (empty) strategy uses primary-first failover: always picks agy-a while healthy.
	l1, err := m.Acquire(SelectRequest{CandidateIDs: []string{"agy-a", "agy-b"}, SessionKey: "s1"})
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	if l1.Account.ID != "agy-a" {
		t.Fatalf("expected primary agy-a, got %s", l1.Account.ID)
	}
	l1.Release()

	// New session with sticky ALSO picks primary agy-a (never alternates via LRU).
	now = now.Add(time.Second)
	l2, err := m.Acquire(SelectRequest{Strategy: config.StrategySticky, CandidateIDs: []string{"agy-a", "agy-b"}, SessionKey: "s2"})
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	if l2.Account.ID != "agy-a" {
		t.Fatalf("expected sticky new session to pick primary agy-a, got %s", l2.Account.ID)
	}
	l2.Release()

	// Legacy round-robin and least-busy strings normalize to primary-first failover (never rotate).
	for _, legacy := range []string{"round-robin", "least-busy"} {
		now = now.Add(time.Second)
		l, err := m.Acquire(SelectRequest{RuleKey: "agy/*", Strategy: legacy, CandidateIDs: []string{"agy-a", "agy-b"}, SessionKey: "legacy-" + legacy})
		if err != nil {
			t.Fatalf("acquire legacy %s: %v", legacy, err)
		}
		if l.Account.ID != "agy-a" {
			t.Fatalf("expected legacy %s to normalize to primary agy-a, got %s", legacy, l.Account.ID)
		}
		l.Release()
	}

	// Only when primary agy-a hits cooldown/quota exhaustion does traffic switch to agy-b.
	rule := config.RoutingRule{Pattern: "agy/*", Strategy: config.StrategyFailover, AllowCrossAccountRetryOn429: true}
	retrySecs := backend.ParseResetDurationSeconds("Quota exceeded. Resets in 48m24s")
	if retrySecs != 48*60+24 {
		t.Fatalf("expected parsed reset 2904s, got %d", retrySecs)
	}
	if !m.RecordError("agy-a", &backend.BackendError{Kind: backend.ErrKindRateLimit, StatusCode: 429, RetryAfter: retrySecs}, rule) {
		t.Fatalf("expected failover allowed on 429 when AllowCrossAccountRetryOn429=true")
	}

	// agy-a stays in cooldown for the full parsed 48m24s (still cooling down after 10 minutes!)
	now = now.Add(10 * time.Minute)
	l3, err := m.Acquire(SelectRequest{Strategy: config.StrategyFailover, CandidateIDs: []string{"agy-a", "agy-b"}, SessionKey: "s1"})
	if err != nil {
		t.Fatalf("acquire failover to b: %v", err)
	}
	if l3.Account.ID != "agy-b" {
		t.Fatalf("expected failover to agy-b while agy-a cools down, got %s", l3.Account.ID)
	}
	l3.Release()

	// After 48m24s elapses, agy-a recovers and becomes primary again.
	now = now.Add(39 * time.Minute)
	l4, err := m.Acquire(SelectRequest{Strategy: config.StrategyFailover, CandidateIDs: []string{"agy-a", "agy-b"}})
	if err != nil {
		t.Fatalf("acquire recovered a: %v", err)
	}
	if l4.Account.ID != "agy-a" {
		t.Fatalf("expected recovered primary agy-a, got %s", l4.Account.ID)
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

	ruleNo429 := config.RoutingRule{Pattern: "agy/*", Strategy: config.StrategyFailover, AllowCrossAccountRetryOn429: false}

	// 5xx -> failover allowed
	if !m.RecordError("agy-1", &backend.BackendError{Kind: backend.ErrKindTransient, StatusCode: 503, Message: "down"}, ruleNo429) {
		t.Fatalf("expected failover allowed on 5xx")
	}

	// 429 -> cooldown, NO failover when AllowCrossAccountRetryOn429=false
	if m.RecordError("agy-1", &backend.BackendError{Kind: backend.ErrKindRateLimit, StatusCode: 429, RetryAfter: 30}, ruleNo429) {
		t.Fatalf("expected NO failover on 429 when AllowCrossAccountRetryOn429=false")
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
	m.RecordError("agy-2", &backend.BackendError{Kind: backend.ErrKindAuth, StatusCode: 401, Message: "unauthorized"}, ruleNo429)
	if len(m.Banners()) != 0 {
		t.Fatalf("first 401 should mark login_needed, not auto-disable yet")
	}
	m.RecordError("agy-2", &backend.BackendError{Kind: backend.ErrKindAuth, StatusCode: 401, Message: "unauthorized"}, ruleNo429)
	banners := m.Banners()
	if len(banners) != 1 || banners[0].AccountID != "agy-2" {
		t.Fatalf("expected health banner after repeated 401, got %+v", banners)
	}
}
