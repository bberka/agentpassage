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

func TestRestrictedAgyAccountPausesTheOthers(t *testing.T) {
	account := func(id, backendID, kind string) config.AccountConfig {
		return config.AccountConfig{ID: id, Name: id, Kind: kind, Backend: backendID, Enabled: true, MaxConcurrency: 1}
	}
	m := NewManager([]config.AccountConfig{
		account("one", "agy", config.KindACP), account("two", "agy", config.KindACP),
		account("codex", "codex", config.KindACP), account("key", "agy", config.KindAPIKey),
	})
	m.RecordError("one", &backend.BackendError{Kind: backend.ErrKindRestricted, StatusCode: 403}, config.RoutingRule{})
	m.RecordSuccess("two") // a turn that was already running
	for id, want := range map[string]bool{"one": false, "two": false, "codex": true, "key": true} {
		lease, err := m.Acquire(SelectRequest{CandidateIDs: []string{id}})
		lease.Release()
		if (err == nil) != want {
			t.Errorf("%s usable=%v, want %v", id, err == nil, want)
		}
	}
	for _, s := range m.Snapshots() {
		if s.ID == "two" && s.Banner == "" {
			t.Error("paused account should say why")
		}
	}
}

func TestRepeatedServerErrorsPauseAnAPIKeyAccount(t *testing.T) {
	for kind, pauses := range map[string]bool{config.KindAPIKey: true, config.KindACP: false} {
		m, now := oneAccount(kind)
		fail := func() {
			m.RecordError("a", &backend.BackendError{Kind: backend.ErrKindTransient, StatusCode: 502}, config.RoutingRule{})
		}
		fail()
		fail()
		m.RecordSuccess("a")
		fail()
		fail()
		if !usable(m) {
			t.Fatalf("%s: paused before three failures in a row", kind)
		}
		fail()
		if usable(m) == pauses {
			t.Errorf("%s: paused=%v, want %v", kind, !usable(m), pauses)
		}
		*now = now.Add(31 * time.Second)
		if !usable(m) {
			t.Errorf("%s: should be back after 30s", kind)
		}
	}
}
