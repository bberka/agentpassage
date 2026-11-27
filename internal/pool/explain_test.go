package pool

import (
	"strings"
	"testing"
	"time"

	"github.com/bberka/agentpassage/internal/config"
)

func TestExplainMatchesAcquireWithoutChangingState(t *testing.T) {
	m, now := newQuotaTestManager(t)
	req := SelectRequest{
		Strategy:       config.StrategyRoundRobin,
		RuleKey:        "r",
		CandidateIDs:   []string{"agy-a", "agy-b"},
		QuotaExhausted: map[string]time.Time{"agy-a": now.Add(time.Hour)},
		QuotaUsedPct:   map[string]float64{"agy-a": 100, "agy-b": 40},
		// Round-robin on subscription accounts needs the opt-in.
		SubscriptionWarningAcked: true,
	}
	ex := m.Explain(req)
	if ex.ChosenID != "agy-b" || ex.Error != "" {
		t.Fatalf("explain chose %q (err %q), want agy-b", ex.ChosenID, ex.Error)
	}
	if c := ex.Candidates[0]; c.Status != "quota used up" || !strings.Contains(c.Reason, "resets") {
		t.Fatalf("agy-a should be reported out of quota, got %+v", c)
	}
	if c := ex.Candidates[1]; c.Status != "eligible" || c.QuotaUsedPct == nil || *c.QuotaUsedPct != 40 {
		t.Fatalf("agy-b should be eligible at 40%%, got %+v", c)
	}
	if got := acquireID(t, m, req); got != ex.ChosenID {
		t.Fatalf("acquire picked %s, explain said %s", got, ex.ChosenID)
	}

	// With both accounts eligible, explaining twice must not advance round-robin.
	open := SelectRequest{Strategy: req.Strategy, RuleKey: "open", CandidateIDs: req.CandidateIDs, SubscriptionWarningAcked: true}
	first := m.Explain(open).ChosenID
	if again := m.Explain(open).ChosenID; again != first {
		t.Fatalf("explain moved the round-robin cursor: %s then %s", first, again)
	}
	if got := acquireID(t, m, open); got != first {
		t.Fatalf("acquire picked %s, explain said %s", got, first)
	}
	var requests int64
	for _, s := range m.Snapshots() {
		requests += s.TotalRequests
	}
	if requests != 2 {
		t.Fatalf("only the 2 acquires count as requests, got %d", requests)
	}
}

func TestExplainExistingSessionSwitchesByDefaultAndWaitsWhenOptedIn(t *testing.T) {
	m, now := newQuotaTestManager(t)
	m.PinSession("s1", "agy-a")
	req := SelectRequest{
		CandidateIDs:   []string{"agy-a", "agy-b"},
		SessionKey:     "s1",
		QuotaExhausted: map[string]time.Time{"agy-a": now.Add(time.Hour)},
	}
	ex := m.Explain(req)
	if ex.PinnedTo != "agy/a" || ex.ChosenID != "agy-b" || ex.Error != "" {
		t.Fatalf("default pinned conversation should switch to agy-b, got %+v", ex)
	}
	req.WaitForResetWhenExhausted = true
	ex = m.Explain(req)
	if ex.PinnedTo != "agy/a" || ex.ChosenID != "" || !strings.Contains(ex.Error, "stays on that account") {
		t.Fatalf("opt-in pinned conversation should be refused, got %+v", ex)
	}
}
