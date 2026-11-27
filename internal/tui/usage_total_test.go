package tui

import (
	"testing"
	"time"

	"github.com/bberka/agentpassage/internal/quota"
)

func TestQuotaRowsAddPooledTotalPerGroup(t *testing.T) {
	early := time.Date(2026, 11, 29, 0, 0, 0, 0, time.UTC)
	late := early.Add(48 * time.Hour)
	agy := func(id string, gemLeft, cgLeft float64, reset time.Time) quota.AccountQuota {
		r := reset
		return quota.AccountQuota{AccountID: id, AccountName: "agy/" + id, Backend: "agy", Groups: []quota.QuotaGroup{
			{ID: quota.GroupGemini, Name: "Gemini Models", Secondary: &quota.Window{Label: "Weekly", LeftPercent: gemLeft, UsedPercent: 100 - gemLeft}},
			{ID: quota.GroupClaudeGPT, Name: "Claude & GPT", Secondary: &quota.Window{Label: "Weekly", LeftPercent: cgLeft, UsedPercent: 100 - cgLeft, ResetsAt: &r}},
		}}
	}
	rows := quotaRows([]quota.AccountQuota{
		agy("bb", 75, 50, late),
		agy("zdk", 75, 0, early),
		{AccountID: "c", AccountName: "claude/1", Backend: "claude", Secondary: &quota.Window{Label: "Weekly", LeftPercent: 28}},
	})
	// 4 agy group rows + 1 claude row + 2 agy totals; a single claude account gets no total.
	if len(rows) != 7 {
		t.Fatalf("want 7 rows, got %d", len(rows))
	}
	cg := rows[6]
	if cg.name != "agy total (2) · Claude & GPT" || cg.Secondary.LeftPercent != 25 {
		t.Fatalf("unexpected Claude & GPT total %q %+v", cg.name, cg.Secondary)
	}
	if !cg.Secondary.ResetsAt.Equal(early) {
		t.Fatalf("total should show the soonest reset, got %v", cg.Secondary.ResetsAt)
	}
	if rows[5].name != "agy total (2) · Gemini Models" || rows[5].Secondary.LeftPercent != 75 {
		t.Fatalf("unexpected Gemini total %q %+v", rows[5].name, rows[5].Secondary)
	}
}
