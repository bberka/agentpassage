package pool

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
)

func oneAccount(kind string) (*Manager, *time.Time) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := NewManager([]config.AccountConfig{{ID: "a", Name: "a", Kind: kind, Backend: "x", Enabled: true, MaxConcurrency: 1}})
	m.now = func() time.Time { return now }
	return m, &now
}

func usable(m *Manager) bool {
	lease, err := m.Acquire(SelectRequest{CandidateIDs: []string{"a"}})
	if err != nil {
		return false
	}
	lease.Release()
	return true
}

func TestAPIKeyAccountRecoversFromAuthError(t *testing.T) {
	m, now := oneAccount(config.KindAPIKey)
	m.RecordError("a", &backend.BackendError{Kind: backend.ErrKindAuth, StatusCode: 401}, config.RoutingRule{})
	if usable(m) {
		t.Fatal("account should back off after a 401")
	}
	*now = now.Add(11 * time.Minute)
	if !usable(m) {
		t.Fatal("API-key account should recover once the cooldown ends")
	}
}

func TestShortRateLimitKeepsTheLongerCooldown(t *testing.T) {
	for _, group := range []string{"", "g"} {
		m, now := oneAccount(config.KindACP)
		for _, secs := range []int{3600, 5} {
			m.RecordErrorForGroup("a", group, &backend.BackendError{Kind: backend.ErrKindRateLimit, RetryAfter: secs}, config.RoutingRule{})
		}
		*now = now.Add(time.Minute)
		if _, err := m.Acquire(SelectRequest{CandidateIDs: []string{"a"}, QuotaGroup: group}); err == nil {
			t.Errorf("group %q: the one-hour cooldown was replaced by the 5s one", group)
		}
	}
}

func TestCanceledCallCostsTheAccountNothing(t *testing.T) {
	m, _ := oneAccount(config.KindACP)
	rule := config.RoutingRule{Strategy: config.StrategyFailover}
	for _, err := range []error{
		context.Canceled,
		&backend.BackendError{Kind: backend.ErrKindAuth, Cause: fmt.Errorf("read: %w", context.Canceled)},
	} {
		if m.RecordError("a", err, rule) {
			t.Errorf("%v: should not fail over", err)
		}
	}
	if snap := m.Snapshots()[0]; snap.TotalErrors != 0 || !usable(m) {
		t.Errorf("cancel counted against the account: %+v", snap)
	}
}
