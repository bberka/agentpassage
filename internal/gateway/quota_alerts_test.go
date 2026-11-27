package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/bberka/agentpassage/internal/quota"
)

// Low-quota alerts cover the account windows and every model group (agy
// Gemini and Claude & GPT, Claude Fable), and fire once per window period.
func TestQuotaAlertsCoverGroupsAndFireOnce(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	reset := now.Add(48 * time.Hour)
	rep := quota.Report{Accounts: []quota.AccountQuota{
		{AccountID: "agy-zdk", AccountName: "agy/zdk", Groups: []quota.QuotaGroup{
			{ID: quota.GroupGemini, Name: "Gemini Models", Secondary: &quota.Window{Label: "Weekly", UsedPercent: 25, LeftPercent: 75}},
			{ID: quota.GroupClaudeGPT, Name: "Claude & GPT", Secondary: &quota.Window{Label: "Weekly", UsedPercent: 100, LeftPercent: 0, ResetsAt: &reset}},
		}},
		{AccountID: "claude-1", AccountName: "claude/1",
			Secondary: &quota.Window{Label: "Weekly", UsedPercent: 72, LeftPercent: 28},
			Groups:    []quota.QuotaGroup{{ID: "fable", Name: "Fable", Primary: &quota.Window{Label: "Weekly", UsedPercent: 95, LeftPercent: 5}}}},
	}}

	got := quotaAlertsFor(rep, 90)
	if len(got) != 2 {
		t.Fatalf("want 2 alerts (agy Claude & GPT, Claude Fable), got %+v", got)
	}
	if !strings.Contains(got[0].title, "Claude & GPT Weekly") || !strings.Contains(got[1].title, "Fable Weekly") {
		t.Fatalf("unexpected titles: %q, %q", got[0].title, got[1].title)
	}

	var st quotaAlertState
	if n := len(st.pending(got, now)); n != 2 {
		t.Fatalf("first refresh should send 2, sent %d", n)
	}
	if n := len(st.pending(quotaAlertsFor(rep, 90), now.Add(5*time.Minute))); n != 0 {
		t.Fatalf("later refreshes in the same window should send nothing, sent %d", n)
	}
	// After the agy window resets, a new period may alert again.
	if n := len(st.pending(got[:1], reset.Add(time.Minute))); n != 1 {
		t.Fatalf("after reset the window should alert again, sent %d", n)
	}
}
