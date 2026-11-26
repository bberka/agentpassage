package gateway

import (
	"testing"
	"time"

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
