package pool

import (
	"errors"
	"testing"
	"time"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
)

func TestPrimaryFirstFailoverStickyAndSubscriptionGuard(t *testing.T) {
	accts := []config.AccountConfig{
		{ID: "agy-a", Name: "a", Kind: config.KindACP, Backend: "agy", Enabled: true, MaxConcurrency: 1},
		{ID: "agy-b", Name: "b", Kind: config.KindACP, Backend: "agy", Enabled: true, MaxConcurrency: 1},
		{ID: "ds-1", Name: "one", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, MaxConcurrency: 4},
		{ID: "ds-2", Name: "two", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, MaxConcurrency: 4},
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

	// round-robin & least-busy work freely on API-key accounts without subscription opt-in!
	api1, err := m.Acquire(SelectRequest{RuleKey: "deepseek/*", Strategy: config.StrategyRoundRobin, CandidateIDs: []string{"ds-1", "ds-2"}})
	if err != nil {
		t.Fatalf("expected round-robin on API keys to succeed: %v", err)
	}
	api1.Release()
	api2, err := m.Acquire(SelectRequest{RuleKey: "deepseek/*", Strategy: config.StrategyRoundRobin, CandidateIDs: []string{"ds-1", "ds-2"}})
	if err != nil {
		t.Fatalf("expected second round-robin on API keys to succeed: %v", err)
	}
	api2.Release()
	if api1.Account.ID == api2.Account.ID {
		t.Fatalf("expected API-key round-robin to rotate between ds-1 and ds-2, both got %s", api1.Account.ID)
	}

	// round-robin & least-busy on subscription accounts WITHOUT opt-in are blocked!
	for _, strat := range []string{config.StrategyRoundRobin, config.StrategyLeastBusy} {
		_, err := m.Acquire(SelectRequest{RuleKey: "agy/*", Strategy: strat, CandidateIDs: []string{"agy-a", "agy-b"}, SubscriptionWarningAcked: false})
		if !errors.Is(err, ErrStrategyOptInNeeded) {
			t.Fatalf("expected ErrStrategyOptInNeeded for subscription %s without opt-in, got %v", strat, err)
		}
	}

	// With explicit opt-in acknowledged, round-robin succeeds on subscriptions.
	lOpt, err := m.Acquire(SelectRequest{RuleKey: "agy/*", Strategy: config.StrategyRoundRobin, CandidateIDs: []string{"agy-a", "agy-b"}, SubscriptionWarningAcked: true})
	if err != nil {
		t.Fatalf("expected subscription round-robin with opt-in to succeed: %v", err)
	}
	lOpt.Release()

	// Only when primary agy-a hits cooldown/quota exhaustion does failover switch to agy-b.
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

	// RecordSuccess clears stale auth/disable banner
	m.RecordSuccess("agy-2")
	if got := m.Banners(); len(got) != 0 {
		t.Fatalf("expected RecordSuccess to clear banner, got %+v", got)
	}

	// Banner loaded from DisabledReason on daemon startup must clear once SyncAccounts re-enables the account
	m2 := NewManager([]config.AccountConfig{
		{ID: "agy-bb", Name: "bb", Backend: "agy", Enabled: false, DisabledReason: "Disabled after repeated auth errors (401)", MaxConcurrency: 1},
	})
	if len(m2.Banners()) != 1 {
		t.Fatalf("expected startup banner from DisabledReason")
	}
	m2.SyncAccounts([]config.AccountConfig{
		{ID: "agy-bb", Name: "bb", Backend: "agy", Enabled: true, DisabledReason: "", MaxConcurrency: 1},
	})
	if got := m2.Banners(); len(got) != 0 {
		t.Fatalf("expected SyncAccounts with Enabled=true to clear stale banner, got %+v", got)
	}
}

func TestGroupRateLimitCooldownIsolation(t *testing.T) {
	m := NewManager([]config.AccountConfig{
		{ID: "agy-1", Name: "one", Kind: config.KindACP, Backend: "agy", Enabled: true, MaxConcurrency: 4},
	})
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m.SetNowFunc(func() time.Time { return now })
	rule := config.RoutingRule{Pattern: "agy/*", Strategy: config.StrategyFailover}
	limited := &backend.BackendError{Kind: backend.ErrKindRateLimit, StatusCode: 429}

	acquire := func(group string) error {
		l, err := m.Acquire(SelectRequest{CandidateIDs: []string{"agy-1"}, QuotaGroup: group})
		if err == nil {
			l.Release()
		}
		return err
	}

	m.RecordErrorForGroup("agy-1", "claude-gpt", limited, rule)
	if err := acquire("claude-gpt"); !errors.Is(err, ErrNoAvailableAccount) {
		t.Fatalf("expected claude-gpt cooling down, got %v", err)
	}
	if err := acquire("gemini"); err != nil {
		t.Fatalf("claude-gpt 429 must not block gemini: %v", err)
	}

	// A first Gemini 429 gets the base 60s backoff, not one inflated by Claude's.
	m.RecordErrorForGroup("agy-1", "claude-gpt", limited, rule)
	m.RecordErrorForGroup("agy-1", "gemini", limited, rule)
	now = now.Add(61 * time.Second)
	if err := acquire("gemini"); err != nil {
		t.Fatalf("expected gemini recovered after 60s: %v", err)
	}
	if err := acquire("claude-gpt"); err == nil {
		t.Fatalf("expected claude-gpt still cooling down on its 120s backoff")
	}

	m.ClearHealth("agy-1")
	if err := acquire("claude-gpt"); err != nil {
		t.Fatalf("expected ClearHealth to reset group cooldowns: %v", err)
	}

	// A rate limit without a group still cools down the whole account.
	m.RecordError("agy-1", limited, rule)
	if err := acquire("gemini"); err == nil {
		t.Fatalf("expected account-wide cooldown to block every group")
	}
}

func TestFastestStrategy(t *testing.T) {
	m := NewManager([]config.AccountConfig{
		{ID: "a", Name: "a", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, MaxConcurrency: 4},
		{ID: "b", Name: "b", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, MaxConcurrency: 4},
	})
	pick := func() string {
		t.Helper()
		l, err := m.Acquire(SelectRequest{Strategy: config.StrategyFastest, CandidateIDs: []string{"a", "b"}})
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
		l.Release()
		return l.Account.ID
	}

	m.RecordLatency("a", 100)
	if got := pick(); got != "b" {
		t.Fatalf("expected unmeasured b tried first, got %s", got)
	}
	m.RecordLatency("b", 500)
	if got := pick(); got != "a" {
		t.Fatalf("expected faster a, got %s", got)
	}
	// 0.3*2000 + 0.7*100 = 670ms, now slower than b.
	m.RecordLatency("a", 2000)
	if got := pick(); got != "b" {
		t.Fatalf("expected b after a slowed down, got %s", got)
	}
}
