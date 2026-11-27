package gateway

import (
	"testing"
	"time"

	"github.com/bberka/agentpassage/internal/quota"
)

func TestQuotaAlertTextIsShortAndNotRepeated(t *testing.T) {
	reset := time.Date(2026, 12, 1, 9, 30, 0, 0, time.Local)
	weekly := func() *quota.Window { r := reset; return &quota.Window{Label: "Weekly", UsedPercent: 100, ResetsAt: &r} }
	got := quotaAlertsFor(quota.Report{Accounts: []quota.AccountQuota{{
		AccountID: "agy-bb", AccountName: "agy/bb", Secondary: weekly(),
		Groups: []quota.QuotaGroup{{Name: "Gemini Models", Secondary: weekly()}},
	}}}, 90)
	if len(got) != 1 {
		t.Fatalf("want one alert for one limit, got %d", len(got))
	}
	if got[0].title != "agy/bb: Gemini weekly 100% used" || got[0].message != "Resets Dec 1 09:30" {
		t.Fatalf("got %q / %q", got[0].title, got[0].message)
	}
}
