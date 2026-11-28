package pool

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
)

func TestUsageLimitSwitchesThenBlocks(t *testing.T) {
	m := NewManager([]config.AccountConfig{
		{ID: "a", Name: "a", Enabled: true, MaxConcurrency: 1},
		{ID: "b", Name: "b", Enabled: true, MaxConcurrency: 1},
	})
	block := LimitBlock{Reason: "daily weekly limit reached", Until: time.Now().Add(2 * time.Hour)}
	m.PinSession("conv", "a")
	req := SelectRequest{CandidateIDs: []string{"a", "b"}, SessionKey: "conv", LimitReached: map[string]LimitBlock{"a": block}}
	lease, err := m.Acquire(req)
	if err != nil || lease.Account.ID != "b" {
		t.Fatalf("lease %+v, %v", lease, err)
	}
	lease.Release()

	// A used-up account is still tried, but a limited one never is.
	req.LimitReached["b"] = block
	_, err = m.Acquire(req)
	be, ok := backend.AsBackendError(err)
	if !ok || be.StatusCode != 429 || be.RetryAfter < 7000 || !errors.Is(err, ErrUsageLimit) || !strings.Contains(be.Message, "daily weekly limit reached, ") {
		t.Fatalf("error %v", err)
	}
}
