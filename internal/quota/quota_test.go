package quota_test

import (
	"testing"
	"time"

	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/quota"
	"github.com/bberka/acpx/internal/usage"
)

func TestParseCodexRateLimits(t *testing.T) {
	// Real Codex Plus/Pro style payload with both 5h primary and 7d weekly secondary windows.
	raw := []byte(`{
		"rateLimits": {
			"planType": "plus",
			"primary": {"usedPercent": 24, "windowDurationMins": 300, "resetsAt": 1797873277},
			"secondary": {"usedPercent": 61, "windowDurationMins": 10080, "resetsAt": 1798478077},
			"credits": {"hasCredits": false, "unlimited": false, "balance": null}
		},
		"rateLimitResetCredits": {"availableCount": 1}
	}`)
	q, err := quota.ParseCodexRateLimits(raw)
	if err != nil {
		t.Fatalf("ParseCodexRateLimits failed: %v", err)
	}
	if q.Source != "codex-live" || q.PlanType != "plus" {
		t.Fatalf("unexpected source/plan: %+v", q)
	}
	if q.Primary == nil || q.Primary.Label != "5h" || q.Primary.UsedPercent != 24 || q.Primary.LeftPercent != 76 {
		t.Fatalf("unexpected primary window: %+v", q.Primary)
	}
	if q.Secondary == nil || q.Secondary.Label != "Weekly" || q.Secondary.UsedPercent != 61 || q.Secondary.LeftPercent != 39 {
		t.Fatalf("unexpected secondary window: %+v", q.Secondary)
	}
	if q.Credits != "1 reset credit" {
		t.Fatalf("expected reset credit label, got %q", q.Credits)
	}
}

func TestParseGrokUsageJSON(t *testing.T) {
	raw := []byte(`{
		"plan": "supergrok",
		"hourly": {"used_percent": 30, "windowDurationMins": 120},
		"weekly": {"used_percent": 55, "windowDurationMins": 10080}
	}`)
	q, err := quota.ParseGrokUsageJSON(raw)
	if err != nil {
		t.Fatalf("ParseGrokUsageJSON failed: %v", err)
	}
	if q.Source != "grok-live" || q.PlanType != "supergrok" {
		t.Fatalf("unexpected grok quota: %+v", q)
	}
	if q.Primary == nil || q.Primary.LeftPercent != 70 {
		t.Fatalf("unexpected grok primary: %+v", q.Primary)
	}
	if q.Secondary == nil || q.Secondary.LeftPercent != 45 || q.Secondary.Label != "Weekly" {
		t.Fatalf("unexpected grok secondary: %+v", q.Secondary)
	}
}

func TestFetcherReportMergesLocalRollingUsageAndBudgets(t *testing.T) {
	accts := []config.AccountConfig{
		{ID: "agy-bb", Name: "bb", Backend: "agy", Kind: config.KindACP, Enabled: true, HourlyLimit: 20, WeeklyLimit: 200},
		{ID: "deepseek-main", Name: "main", Backend: "deepseek", Kind: config.KindAPIKey, Enabled: true},
	}
	sum := usage.Summary{
		ByAccount: []usage.AccountUsage{
			{Account: "agy/bb", Backend: "agy", Reqs1h: 5, Reqs5h: 10, Reqs24h: 15, Reqs7d: 50},
			{Account: "deepseek/main", Backend: "deepseek", Reqs1h: 2, Reqs5h: 4, Reqs24h: 6, Reqs7d: 12},
		},
	}

	f := quota.New(t.TempDir(), func() []config.AccountConfig { return accts }, func() usage.Summary { return sum })
	rep := f.Report()
	if len(rep.Accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %d", len(rep.Accounts))
	}

	agy := rep.Accounts[0]
	if agy.AccountName != "agy/bb" || agy.Source != "local-budget" {
		t.Fatalf("expected agy/bb local-budget, got %+v", agy)
	}
	if agy.Primary == nil || agy.Primary.Label != "Hourly" || agy.Primary.LeftPercent != 75 {
		t.Fatalf("expected 75%% left on hourly budget (5/20), got %+v", agy.Primary)
	}
	if agy.Secondary == nil || agy.Secondary.Label != "Weekly" || agy.Secondary.LeftPercent != 75 {
		t.Fatalf("expected 75%% left on weekly budget (50/200), got %+v", agy.Secondary)
	}

	ds := rep.Accounts[1]
	if ds.Source != "local-tracked" || ds.Local5h != 4 || ds.Local7d != 12 {
		t.Fatalf("unexpected deepseek local counts: %+v", ds)
	}

	// Formatting helpers
	now := time.Now()
	in2h := now.Add(2*time.Hour + 15*time.Minute)
	if got := quota.FormatResetIn(&in2h, now); got != "2h 15m" {
		t.Fatalf("expected 2h 15m, got %q", got)
	}
	if got := quota.FormatWindowLabel(43200); got != "Monthly" {
		t.Fatalf("expected Monthly, got %q", got)
	}
}
