package gateway

import (
	"testing"
	"time"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/quota"
)

// T3 Code decodes retrieveUserQuotaSummary strictly: every bucket needs a
// non-empty bucketId, and the window must be "5h" or "weekly" to get a label.
func TestAntigravityQuotaSummaryPayload(t *testing.T) {
	reset := time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)
	aq := quota.AccountQuota{Groups: []quota.QuotaGroup{
		{ID: quota.GroupGemini, Name: "Gemini Models",
			Primary:   &quota.Window{Label: "5h", LeftPercent: 100, WindowMins: 300},
			Secondary: &quota.Window{Label: "Weekly", LeftPercent: 0, WindowMins: 10080, ResetsAt: &reset}},
		{ID: quota.GroupClaudeGPT, Name: "Claude & GPT",
			Primary:   &quota.Window{Label: "5h", LeftPercent: 84, WindowMins: 300},
			Secondary: &quota.Window{Label: "Weekly", LeftPercent: 49, WindowMins: 10080}},
	}}
	p := buildAntigravityQuotaSummaryPayload(aq)
	groups := p["groups"].([]map[string]any)
	if len(groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(groups))
	}
	want := map[string]float64{"gemini-5h": 1, "gemini-weekly": 0, "3p-5h": 0.84, "3p-weekly": 0.49}
	got := map[string]float64{}
	for _, g := range groups {
		for _, b := range g["buckets"].([]map[string]any) {
			id := b["bucketId"].(string)
			got[id] = b["remainingFraction"].(float64)
			if w := b["window"]; w != "5h" && w != "weekly" {
				t.Errorf("bucket %s has window %v", id, w)
			}
		}
	}
	for id, f := range want {
		if g, ok := got[id]; !ok || g != f {
			t.Errorf("bucket %s = %v (present %v), want %v", id, g, ok, f)
		}
	}

	// Accounts without groups still report a Gemini bucket.
	flat := buildAntigravityQuotaSummaryPayload(quota.AccountQuota{Secondary: &quota.Window{Label: "Weekly", LeftPercent: 30}})
	if b := flat["buckets"].([]map[string]any); len(b) != 1 || b[0]["bucketId"] != "gemini-weekly" {
		t.Fatalf("flat account buckets = %v", b)
	}
}

// T3 Code reads model-specific Claude limits from weekly_scoped entries.
func TestClaudeUsagePayloadScopedLimits(t *testing.T) {
	aq := quota.AccountQuota{
		Primary:   &quota.Window{Label: "Weekly", UsedPercent: 72},
		Secondary: &quota.Window{Label: "Weekly · Fable", UsedPercent: 17},
	}
	limits := buildClaudeOAuthUsagePayload(aq, time.Now())["limits"].([]map[string]any)
	if len(limits) != 1 {
		t.Fatalf("want 1 scoped limit, got %v", limits)
	}
	l := limits[0]
	name := l["scope"].(map[string]any)["model"].(map[string]any)["display_name"]
	if l["kind"] != "weekly_scoped" || name != "Fable" || l["percent"] != 17.0 {
		t.Fatalf("bad scoped limit %v", l)
	}
}

// T3 Code reads Grok usage for CLIProxyAPI accounts listed as "xai", from the
// Grok billing response shape: config.creditUsagePercent and config.currentPeriod.
func TestGrokBillingPayloadForT3Code(t *testing.T) {
	if got := cliProxyProviderName("grok"); got != "xai" {
		t.Fatalf("grok must be listed as xai, got %q", got)
	}
	reset := time.Date(2026, 11, 29, 18, 6, 54, 0, time.UTC)
	aq := quota.AccountQuota{Backend: "grok", Primary: &quota.Window{Label: "Weekly", UsedPercent: 1, LeftPercent: 99, WindowMins: 10080, ResetsAt: &reset}}
	code, body := synthesizeCLIProxyUpstreamResponse(config.AccountConfig{Backend: "grok"}, aq, "https://cli-chat-proxy.grok.com/v1/billing?format=credits", reset)
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	cfg := body.(map[string]any)["config"].(map[string]any)
	if cfg["creditUsagePercent"] != 1.0 {
		t.Fatalf("creditUsagePercent = %v", cfg["creditUsagePercent"])
	}
	p := cfg["currentPeriod"].(map[string]any)
	if p["type"] != "USAGE_PERIOD_TYPE_WEEKLY" || p["end"] != "2026-11-29T18:06:54Z" || p["start"] != "2026-11-22T18:06:54Z" {
		t.Fatalf("unexpected period %v", p)
	}
}
