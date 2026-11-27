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
	if len(m.rrCursor) != 0 || m.states["agy-b"].totalRequests != 0 {
		t.Fatal("explain must not move the round-robin cursor or count a request")
	}
	if got := acquireID(t, m, req); got != ex.ChosenID {
		t.Fatalf("acquire picked %s, explain said %s", got, ex.ChosenID)
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
