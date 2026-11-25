package quota_test

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/quota"
	"github.com/bberka/agentpassage/internal/usage"
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

	f := quota.New(t.TempDir(), func() []config.AccountConfig { return accts }, func() usage.Summary { return sum }, nil)
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

func TestParseDeepSeekBalance(t *testing.T) {
	raw := []byte(`{
		"is_available": true,
		"balance_infos": [
			{"currency": "USD", "total_balance": "14.25", "granted_balance": "0.00", "topped_up_balance": "14.25"}
		]
	}`)
	q, err := quota.ParseDeepSeekBalance(raw)
	if err != nil {
		t.Fatalf("ParseDeepSeekBalance failed: %v", err)
	}
	if q.Source != "deepseek-live" || q.Credits != "bal: 14.25 USD" {
		t.Fatalf("unexpected deepseek balance: %+v", q)
	}
	if q.Primary != nil {
		t.Fatalf("expected nil primary when available, got %+v", q.Primary)
	}

	exhausted, err := quota.ParseDeepSeekBalance([]byte(`{"is_available": false, "balance_infos": []}`))
	if err != nil {
		t.Fatalf("ParseDeepSeekBalance exhausted failed: %v", err)
	}
	if exhausted.Primary == nil || exhausted.Primary.LeftPercent != 0 {
		t.Fatalf("expected 0%% left window when exhausted, got %+v", exhausted.Primary)
	}
}

func TestParseOpenRouterKey(t *testing.T) {
	raw := []byte(`{
		"data": {
			"usage": 4.0,
			"usage_daily": 0.5,
			"usage_weekly": 2.25,
			"limit": 10.0,
			"limit_remaining": 6.0,
			"limit_reset": "weekly",
			"is_free_tier": false,
			"free_model_daily_requests": {"limit": 50, "used": 10, "remaining": 40}
		}
	}`)
	q, err := quota.ParseOpenRouterKey(raw)
	if err != nil {
		t.Fatalf("ParseOpenRouterKey failed: %v", err)
	}
	if q.Source != "openrouter-live" || q.PlanType != "paid" {
		t.Fatalf("unexpected openrouter quota: %+v", q)
	}
	if q.Primary == nil || q.Primary.Label != "Weekly" || q.Primary.LeftPercent != 60 {
		t.Fatalf("unexpected openrouter primary: %+v", q.Primary)
	}
	if q.Secondary == nil || q.Secondary.Label != "Free/24h" || q.Secondary.LeftPercent != 80 {
		t.Fatalf("unexpected openrouter secondary: %+v", q.Secondary)
	}
}

func TestScanClaudeProjectsDir(t *testing.T) {
	dir := t.TempDir()
	proj := filepath.Join(dir, "proj-a")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	twoDays := now.Add(-48 * time.Hour).Format(time.RFC3339)
	tenDays := now.Add(-10 * 24 * time.Hour).Format(time.RFC3339)

	content := "" +
		`{"timestamp":"` + recent + `","message":{"usage":{"input_tokens":1000,"output_tokens":500,"cache_read_input_tokens":200}}}` + "\n" +
		`{"timestamp":"` + twoDays + `","message":{"usage":{"input_tokens":3000,"output_tokens":1000}}}` + "\n" +
		`{"timestamp":"` + tenDays + `","message":{"usage":{"input_tokens":9999,"output_tokens":1}}}` + "\n"
	if err := os.WriteFile(filepath.Join(proj, "session.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	got := quota.ScanClaudeProjectsDir(dir, now)
	if !got.ScannedOK {
		t.Fatalf("expected ScannedOK=true")
	}
	if got.Msgs5h != 1 || got.Tokens5h != 1700 {
		t.Fatalf("expected 5h msgs=1 tokens=1700, got %+v", got)
	}
	if got.Msgs7d != 2 || got.Tokens7d != 5700 {
		t.Fatalf("expected 7d msgs=2 tokens=5700, got %+v", got)
	}
}

func TestParseHTTPHeadersAndRecord(t *testing.T) {
	now := time.Now()
	reset5h := now.Add(2 * time.Hour).Unix()

	// 1. Anthropic unified 5h + 7d headers
	ah := make(http.Header)
	ah.Set("anthropic-ratelimit-unified-5h-utilization", "0.35")
	ah.Set("anthropic-ratelimit-unified-5h-reset", time.Unix(reset5h, 0).UTC().Format(time.RFC3339))
	ah.Set("anthropic-ratelimit-unified-7d-utilization", "0.60")
	p, s, ok := quota.ParseHTTPHeaders(ah, now)
	if !ok || p == nil || s == nil {
		t.Fatalf("expected anthropic primary+secondary windows, got p=%+v s=%+v ok=%v", p, s, ok)
	}
	if p.Label != "5h" || p.LeftPercent != 65 || s.Label != "Weekly" || s.LeftPercent != 40 {
		t.Fatalf("unexpected anthropic windows: p=%+v s=%+v", p, s)
	}

	// 2. OpenAI-style x-ratelimit-* headers + Fetcher.RecordHTTPHeaders merge
	oh := make(http.Header)
	oh.Set("x-ratelimit-limit-requests", "100")
	oh.Set("x-ratelimit-remaining-requests", "80")
	oh.Set("x-ratelimit-reset-requests", "30s")
	oh.Set("x-ratelimit-limit-tokens", "100000")
	oh.Set("x-ratelimit-remaining-tokens", "50000")

	acct := config.AccountConfig{ID: "openai-main", Name: "main", Backend: "openai", Kind: config.KindAPIKey, Enabled: true}
	f := quota.New(t.TempDir(), func() []config.AccountConfig { return []config.AccountConfig{acct} }, nil, nil)
	f.RecordHTTPHeaders(acct, oh)
	rep := f.Report()
	if len(rep.Accounts) != 1 || rep.Accounts[0].Source != "headers-live" {
		t.Fatalf("expected headers-live in report, got %+v", rep.Accounts)
	}
	if rep.Accounts[0].Primary == nil || rep.Accounts[0].Primary.LeftPercent != 80 {
		t.Fatalf("expected 80%% requests left, got %+v", rep.Accounts[0].Primary)
	}
	if rep.Accounts[0].Secondary == nil || rep.Accounts[0].Secondary.LeftPercent != 50 {
		t.Fatalf("expected 50%% tokens left, got %+v", rep.Accounts[0].Secondary)
	}
}
