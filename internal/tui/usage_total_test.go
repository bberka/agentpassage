package tui

import (
	"testing"
	"time"

	"github.com/bberka/agentpassage/internal/quota"
)

func TestQuotaViewModesSeparateAccountsAndTotals(t *testing.T) {
	early := time.Date(2026, 11, 29, 0, 0, 0, 0, time.UTC)
	late := early.Add(48 * time.Hour)
	agy := func(id string, gemLeft, cgLeft float64, reset time.Time, req5h int) quota.AccountQuota {
		r := reset
		return quota.AccountQuota{AccountID: id, AccountName: "agy/" + id, Backend: "agy", Local5h: req5h, Groups: []quota.QuotaGroup{
			{ID: quota.GroupGemini, Name: "Gemini Models", Secondary: &quota.Window{Label: "Weekly", LeftPercent: gemLeft, UsedPercent: 100 - gemLeft}},
			{ID: quota.GroupClaudeGPT, Name: "Claude & GPT", Secondary: &quota.Window{Label: "Weekly", LeftPercent: cgLeft, UsedPercent: 100 - cgLeft, ResetsAt: &r}},
		}}
	}
	accounts := []quota.AccountQuota{
		agy("bb", 75, 50, late, 3),
		agy("zdk", 75, 0, early, 5),
		{AccountID: "c", AccountName: "claude/1", Backend: "claude", Local5h: 2, Secondary: &quota.Window{Label: "Weekly", LeftPercent: 28}},
	}

	// Account view: 4 agy group rows + 1 claude row (no pooled rows mixed in).
	acctRows := accountQuotaRows(accounts)
	if len(acctRows) != 5 {
		t.Fatalf("account view: want 5 rows, got %d", len(acctRows))
	}

	// Total view: 2 pooled agy group rows + 1 claude row.
	totRows := totalQuotaRows(accounts)
	if len(totRows) != 3 {
		t.Fatalf("total view: want 3 rows, got %d", len(totRows))
	}
	if totRows[0].name != "agy (2) · Gemini Models" || totRows[0].Secondary.LeftPercent != 75 || totRows[0].Local5h != 8 || !totRows[0].showLocal {
		t.Fatalf("unexpected Gemini total %+v", totRows[0])
	}
	cg := totRows[1]
	if cg.name != "agy (2) · Claude & GPT" || cg.Secondary.LeftPercent != 25 || cg.showLocal {
		t.Fatalf("unexpected Claude & GPT total %+v", cg)
	}
	if !cg.Secondary.ResetsAt.Equal(early) {
		t.Fatalf("total should show the soonest reset, got %v", cg.Secondary.ResetsAt)
	}
	if totRows[2].name != "claude (1)" || totRows[2].Secondary.LeftPercent != 28 || totRows[2].Local5h != 2 {
		t.Fatalf("unexpected claude total %+v", totRows[2])
	}
}
