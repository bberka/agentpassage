package health_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/health"
)

func TestCheckerTransitionsAndReport(t *testing.T) {
	accts := []config.AccountConfig{
		{ID: "deepseek-main", Name: "main", Backend: "deepseek", Kind: config.KindAPIKey, Enabled: true},
		{ID: "agy-work", Name: "work", Backend: "agy", Kind: config.KindACP, Enabled: true},
		{ID: "claude-off", Name: "off", Backend: "claude", Kind: config.KindCLI, Enabled: false},
	}

	var failAgy atomic.Bool
	failAgy.Store(true)

	var hookCalls atomic.Int32
	probe := func(_ context.Context, acct config.AccountConfig) ([]string, error) {
		if acct.ID == "agy-work" && failAgy.Load() {
			return nil, errors.New("401 unauthorized")
		}
		return []string{"model-a", "model-b"}, nil
	}
	onResult := func(_ config.AccountConfig, _ []string, _ error) {
		hookCalls.Add(1)
	}

	chk := health.New(
		func() []config.AccountConfig { return accts },
		probe,
		onResult,
		nil,
		config.HealthConfig{Enabled: true, IntervalSeconds: 120},
	)

	// Before any sweep, report is unknown
	rep := chk.Report()
	if rep.Overall != health.StatusUnknown || rep.TotalCount != 3 {
		t.Fatalf("expected initial unknown/3, got overall=%s total=%d", rep.Overall, rep.TotalCount)
	}

	// First sweep: deepseek healthy, agy degraded (1st fail), claude-off skipped (unknown)
	rep = chk.CheckAll(t.Context())
	if rep.Overall != health.StatusDegraded {
		t.Fatalf("expected overall degraded after 1st failure, got %s", rep.Overall)
	}
	if rep.HealthyCount != 1 || rep.UnhealthyCount != 1 {
		t.Fatalf("expected 1 healthy / 1 unhealthy, got %d / %d", rep.HealthyCount, rep.UnhealthyCount)
	}
	if hookCalls.Load() != 2 {
		t.Fatalf("expected 2 hook calls (only enabled accounts), got %d", hookCalls.Load())
	}

	byID := make(map[string]health.Result)
	for _, r := range rep.Accounts {
		byID[r.AccountID] = r
	}
	if byID["deepseek-main"].Status != health.StatusHealthy || byID["deepseek-main"].ModelsFound != 2 {
		t.Fatalf("unexpected deepseek result: %+v", byID["deepseek-main"])
	}
	if byID["agy-work"].Status != health.StatusDegraded || byID["agy-work"].ConsecutiveFails != 1 {
		t.Fatalf("expected agy degraded (1 fail), got %+v", byID["agy-work"])
	}
	if byID["claude-off"].Status != health.StatusUnknown {
		t.Fatalf("expected disabled account unknown, got %+v", byID["claude-off"])
	}

	// Second failure on agy escalates to unhealthy
	res := chk.CheckOne(t.Context(), accts[1])
	if res.Status != health.StatusUnhealthy || res.ConsecutiveFails != 2 {
		t.Fatalf("expected agy unhealthy (2 fails), got %+v", res)
	}

	// Recovery back to healthy resets consecutive fails
	failAgy.Store(false)
	res = chk.CheckOne(t.Context(), accts[1])
	if res.Status != health.StatusHealthy || res.ConsecutiveFails != 0 || res.Error != "" {
		t.Fatalf("expected agy recovered healthy, got %+v", res)
	}
	rep = chk.Report()
	if rep.Overall != health.StatusHealthy || rep.HealthyCount != 2 || rep.UnhealthyCount != 0 {
		t.Fatalf("expected overall healthy (2/0), got %+v", rep)
	}

	// UpdateConfig reflects in report
	chk.UpdateConfig(config.HealthConfig{Enabled: false, IntervalSeconds: 0})
	rep = chk.Report()
	if rep.Enabled || rep.IntervalSeconds != 0 {
		t.Fatalf("expected disabled config in report, got enabled=%v interval=%d", rep.Enabled, rep.IntervalSeconds)
	}
}
