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

func TestParseAgyUsageOutput(t *testing.T) {
	now := time.Date(2026, 6, 5, 18, 0, 0, 0, time.UTC)

	// 1. Structured quota_summary.groups JSON
	structured := []byte(`{
		"status": "SUCCESS",
		"plan_tier": "Pro",
		"quota_summary": {
			"groups": [
				{
					"model_group": "Gemini Models",
					"buckets": [
						{"window": "5h", "usage_fraction": 0.21, "remaining_fraction": 0.79, "reset_time": "2026-06-05T20:40:00Z"},
						{"window": "7d", "usage_fraction": 0.16, "remaining_fraction": 0.84, "reset_time": "2026-06-12T00:00:00Z"}
					]
				},
				{
					"model_group": "Claude and GPT models",
					"buckets": [
						{"window": "7d", "usage_fraction": 0.45, "remaining_fraction": 0.55, "reset_time": "2026-06-08T14:30:00Z"}
					]
				}
			]
		}
	}`)
	q, err := quota.ParseAgyUsageOutput(structured, now)
	if err != nil {
		t.Fatalf("ParseAgyUsageOutput structured failed: %v", err)
	}
	if q.Source != "agy-live" || q.PlanType != "Pro" {
		t.Fatalf("unexpected source/plan: %+v", q)
	}
	if q.Primary == nil || q.Primary.Label != "5h" || q.Primary.LeftPercent != 79 {
		t.Fatalf("unexpected agy primary: %+v", q.Primary)
	}
	if q.Secondary == nil || q.Secondary.Label != "Weekly" || q.Secondary.LeftPercent != 55 {
		t.Fatalf("expected most constrained weekly bucket (55%%), got %+v", q.Secondary)
	}

	// 2. Statusline-style quota map
	mapJSON := []byte(`{
		"plan_tier": "Ultra",
		"quota": {
			"gemini-5h": {"remaining_fraction": 0.90, "reset_in_seconds": 3600},
			"gemini-weekly": {"remaining_fraction": 0.94, "reset_time": "2026-06-12T00:00:00Z"}
		}
	}`)
	qm, err := quota.ParseAgyUsageOutput(mapJSON, now)
	if err != nil {
		t.Fatalf("ParseAgyUsageOutput map failed: %v", err)
	}
	if qm.Primary == nil || qm.Primary.LeftPercent != 90 || qm.Secondary == nil || qm.Secondary.LeftPercent != 94 {
		t.Fatalf("unexpected map windows: p=%+v s=%+v", qm.Primary, qm.Secondary)
	}

	// 3. Text report inside JSON response field
	textEnv := []byte(`{
		"status": "SUCCESS",
		"response": "Plan: Pro\nGemini Models (5h): 78% remaining (resets in 3h 20m)\nWeekly cap: 64% left (resets in 4d 2h)"
	}`)
	qt, err := quota.ParseAgyUsageOutput(textEnv, now)
	if err != nil {
		t.Fatalf("ParseAgyUsageOutput text envelope failed: %v", err)
	}
	if qt.Primary == nil || qt.Primary.LeftPercent != 78 || qt.Secondary == nil || qt.Secondary.LeftPercent != 64 {
		t.Fatalf("unexpected text windows: p=%+v s=%+v", qt.Primary, qt.Secondary)
	}

	// 4. Google account verification required error
	verifyErr := []byte(`{"status":"ERROR","error":"Eligibility check failed: Your current account is not eligible for Antigravity. Verify your account to continue."}`)
	qe, err := quota.ParseAgyUsageOutput(verifyErr, now)
	if err != nil {
		t.Fatalf("ParseAgyUsageOutput verify error failed: %v", err)
	}
	if qe.Source != "unavailable" || qe.Credits != "verify account" {
		t.Fatalf("expected unavailable + verify account badge, got %+v", qe)
	}
}

func TestParseClaudeUsageMarkdown(t *testing.T) {
	loc := time.FixedZone("GMT+3", 3*3600)
	now := time.Date(2026, 11, 25, 20, 35, 0, 0, loc)

	// 1. Live Claude Max/Ultra weekly + model-specific limits (real format from claude-agent-acp /usage)
	md := "## Usage\n\n> Claude max subscription usage\n\n### Limits\n\n" +
		"**Weekly · all models** — **67%** · Resets Nov 27, 4:00 PM GMT+3\n\n" +
		"`█████████████░░░░░░░`\n\n" +
		"**Weekly · Fable** — **10%** · Resets Nov 27, 4:00 PM GMT+3\n"
	q, err := quota.ParseClaudeUsageMarkdown(md, "Claude Ultra", now)
	if err != nil {
		t.Fatalf("ParseClaudeUsageMarkdown failed: %v", err)
	}
	if q.Source != "claude-live" || q.PlanType != "ultra" {
		t.Fatalf("unexpected source/plan: %+v", q)
	}
	if q.Primary == nil || q.Primary.Label != "Weekly" || q.Primary.UsedPercent != 67 || q.Primary.LeftPercent != 33 {
		t.Fatalf("unexpected primary: %+v", q.Primary)
	}
	if q.Primary.ResetsAt == nil || q.Primary.ResetsAt.Day() != 27 || q.Primary.ResetsAt.Hour() != 16 {
		t.Fatalf("unexpected primary reset time: %+v", q.Primary.ResetsAt)
	}
	if q.Secondary == nil || q.Secondary.Label != "Fable" || q.Secondary.UsedPercent != 10 || q.Secondary.LeftPercent != 90 {
		t.Fatalf("unexpected secondary: %+v", q.Secondary)
	}

	// 2. Pro plan with 5h session + Weekly limits
	proMD := "## Usage\n\n> Claude pro subscription usage\n\n**Current session** — **25%** · Resets 11:00 PM GMT+3\n**Weekly · all models** — **40%** · Resets Nov 30, 4:00 PM GMT+3\n"
	qp, err := quota.ParseClaudeUsageMarkdown(proMD, "", now)
	if err != nil {
		t.Fatalf("ParseClaudeUsageMarkdown pro failed: %v", err)
	}
	if qp.PlanType != "pro" || qp.Primary == nil || qp.Primary.Label != "5h" || qp.Primary.LeftPercent != 75 {
		t.Fatalf("unexpected pro primary: %+v", qp)
	}
	if qp.Secondary == nil || qp.Secondary.Label != "Weekly" || qp.Secondary.LeftPercent != 60 {
		t.Fatalf("unexpected pro secondary: %+v", qp.Secondary)
	}
}

func TestParseAgyUsageCommandGroups(t *testing.T) {
	now := time.Date(2026, 6, 5, 18, 0, 0, 0, time.UTC)
	raw := []byte(`{
		"status": "SUCCESS",
		"response": "",
		"command": {
			"name": "usage",
			"data": {
				"groups": [
					{
						"display_name": "Gemini Models",
						"buckets": [
							{"bucket_id": "gemini-weekly", "window": "weekly", "remaining_fraction": 0.84, "reset_time": "2026-06-12T00:00:00Z"},
							{"bucket_id": "gemini-5h", "window": "5h", "remaining_fraction": 0.79, "reset_time": "2026-06-05T20:40:00Z"}
						]
					},
					{
						"display_name": "Claude and GPT models",
						"buckets": [
							{"bucket_id": "3p-weekly", "display_name": "Weekly Limit", "remaining_fraction": 0.55, "reset_time": "2026-06-08T14:30:00Z"},
							{"bucket_id": "3p-5h", "display_name": "Five Hour Limit", "remaining_fraction": 0, "reset_time": "2026-06-05T19:00:00Z"}
						]
					}
				]
			}
		}
	}`)
	q, err := quota.ParseAgyUsageOutput(raw, now)
	if err != nil {
		t.Fatalf("ParseAgyUsageOutput: %v", err)
	}
	if len(q.Groups) != 2 {
		t.Fatalf("expected 2 groups, got %+v", q.Groups)
	}
	want := []struct {
		id, name           string
		fiveH, weekly      float64
		fiveReset, wkReset string
	}{
		{"gemini", "Gemini", 79, 84, "2026-06-05T20:40:00Z", "2026-06-12T00:00:00Z"},
		{"claude-gpt", "Claude & GPT", 0, 55, "2026-06-05T19:00:00Z", "2026-06-08T14:30:00Z"},
	}
	for i, w := range want {
		g := q.Groups[i]
		if g.ID != w.id || g.Name != w.name || g.Primary == nil || g.Secondary == nil {
			t.Fatalf("unexpected group %d: %+v", i, g)
		}
		if g.Primary.Label != "5h" || g.Primary.LeftPercent != w.fiveH || g.Secondary.Label != "Weekly" || g.Secondary.LeftPercent != w.weekly {
			t.Fatalf("%s windows: 5h=%+v weekly=%+v", w.id, g.Primary, g.Secondary)
		}
		if g.Primary.ResetsAt.Format(time.RFC3339) != w.fiveReset || g.Secondary.ResetsAt.Format(time.RFC3339) != w.wkReset {
			t.Fatalf("%s resets: 5h=%v weekly=%v", w.id, g.Primary.ResetsAt, g.Secondary.ResetsAt)
		}
	}
	// Account-level windows stay at the most constrained bucket.
	if q.Primary == nil || q.Primary.LeftPercent != 0 || q.Secondary == nil || q.Secondary.LeftPercent != 55 {
		t.Fatalf("account windows: p=%+v s=%+v", q.Primary, q.Secondary)
	}
	if q.Credits != "Gemini 79% · Claude/GPT 0%" {
		t.Fatalf("unexpected credits %q", q.Credits)
	}

	for group, wantUsed := range map[string]float64{"gemini": 16, "claude-gpt": 100, "": 100} {
		if used, ok := q.UsedPercentForGroup(group); !ok || used != wantUsed {
			t.Fatalf("UsedPercentForGroup(%q) = %v, %v; want %v", group, used, ok, wantUsed)
		}
	}
	// Gemini without a weekly window falls back to its 5h window.
	g5 := quota.AccountQuota{Groups: []quota.QuotaGroup{{ID: quota.GroupGemini, Primary: &quota.Window{UsedPercent: 40}}}}
	if used, ok := g5.UsedPercentForGroup(quota.GroupGemini); !ok || used != 40 {
		t.Fatalf("gemini 5h fallback = %v, %v; want 40", used, ok)
	}
	if _, ok := q.UsedPercentForGroup("other"); ok {
		t.Fatalf("expected unknown group to report no data")
	}
}

func TestGroupForModel(t *testing.T) {
	cases := []struct{ backend, model, want string }{
		{"agy", "Claude-Opus-4", "claude-gpt"},
		{"agy", "gpt-oss-120b", "claude-gpt"},
		{"agy", "gemini-3-pro", "gemini"},
		{"agy", "orchestrator", "gemini"},
		{"agy", "mystery", ""},
		{"codex", "gpt-5", ""},
	}
	for _, c := range cases {
		if got := quota.GroupForModel(c.backend, c.model); got != c.want {
			t.Errorf("GroupForModel(%q, %q) = %q, want %q", c.backend, c.model, got, c.want)
		}
	}
}
