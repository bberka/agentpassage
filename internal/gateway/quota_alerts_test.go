package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bberka/agentpassage/internal/alerts"
	"github.com/bberka/agentpassage/internal/config"
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

func lowBalanceReport(balances ...quota.Balance) []quota.AccountQuota {
	return []quota.AccountQuota{{AccountID: "ds", AccountName: "deepseek/main", Balances: balances}}
}

func usd(amount float64) quota.Balance { return quota.Balance{Currency: "USD", Amount: amount} }

// A low balance alerts, repeats only after the reminder interval, survives
// failed fetches, and alerts again after a recovery.
func TestBalanceAlertRemindsUntilRecovery(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	var st balanceAlertState
	step := func(name string, after time.Duration, accounts []quota.AccountQuota, remind time.Duration, want int) {
		t.Helper()
		if n := len(st.pending(accounts, 5, remind, now.Add(after), true)); n != want {
			t.Fatalf("%s: sent %d, want %d", name, n, want)
		}
	}
	day := 24 * time.Hour
	step("first drop", 0, lowBalanceReport(usd(3)), day, 1)
	step("before reminder", 23*time.Hour, lowBalanceReport(usd(3)), day, 0)
	step("reminder due", 24*time.Hour, lowBalanceReport(usd(2)), day, 1)
	step("failed fetch", 25*time.Hour, lowBalanceReport(), 0, 0)
	step("still low, once only", 100*time.Hour, lowBalanceReport(usd(2)), 0, 0)
	step("recovered at threshold", 101*time.Hour, lowBalanceReport(usd(5)), 0, 0)
	step("next drop", 102*time.Hour, lowBalanceReport(usd(4.99)), 0, 1)
}

// An unused currency at zero must not mark a funded account as low.
func TestBalanceAlertMultiCurrency(t *testing.T) {
	var st balanceAlertState
	cny := quota.Balance{Currency: "CNY"}
	if got := st.pending(lowBalanceReport(cny, usd(20)), 5, 0, time.Now(), true); len(got) != 0 {
		t.Fatalf("funded USD next to 0.00 CNY should stay quiet, got %+v", got)
	}
	got := st.pending(lowBalanceReport(cny, usd(1)), 5, 0, time.Now(), true)
	if len(got) != 1 || !strings.Contains(got[0].message, "0.00 CNY / 1.00 USD") {
		t.Fatalf("want one alert listing both balances, got %+v", got)
	}
}

// Nothing is marked as sent while alerts are off or have nowhere to go, so
// enabling them later still reports an already-low window and balance.
func TestAlertsEnabledLaterStillFire(t *testing.T) {
	var mu sync.Mutex
	var kinds []string
	hook := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var ev alerts.Event
		_ = json.NewDecoder(r.Body).Decode(&ev)
		mu.Lock()
		kinds = append(kinds, ev.Kind)
		mu.Unlock()
	}))
	defer hook.Close()

	rep := quota.Report{Accounts: lowBalanceReport(usd(1))}
	rep.Accounts[0].Primary = &quota.Window{Label: "Weekly", UsedPercent: 99}

	cfg := config.AlertsConfig{QuotaThresholdPct: 90, BalanceThreshold: 5, WebhookURL: hook.URL}
	s := &Server{cfg: &config.Config{}, alerts: alerts.New(cfg)}
	s.checkQuotaAlerts(rep) // disabled
	s.alerts.UpdateConfig(config.AlertsConfig{Enabled: true, QuotaThresholdPct: 90, BalanceThreshold: 5})
	s.checkQuotaAlerts(rep) // no destination

	cfg.Enabled = true
	s.alerts.UpdateConfig(cfg)
	s.checkQuotaAlerts(rep)
	s.checkQuotaAlerts(rep)

	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		got := slices.Clone(kinds)
		mu.Unlock()
		slices.Sort(got)
		if slices.Equal(got, []string{"balance_threshold", "quota_threshold"}) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("want one alert of each kind, got %v", got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Balance alerts work without a quota threshold and skip disabled accounts,
// whose cached balance is never refreshed.
func TestBalanceAlertWithoutQuotaThreshold(t *testing.T) {
	cfg := config.AlertsConfig{Enabled: true, Desktop: false, WebhookURL: "http://127.0.0.1:1", BalanceThreshold: 5}
	s := &Server{
		cfg:    &config.Config{Accounts: []config.AccountConfig{{ID: "off"}, {ID: "ds", Enabled: true}}},
		alerts: alerts.New(cfg),
	}
	rep := quota.Report{Accounts: append(lowBalanceReport(usd(1)),
		quota.AccountQuota{AccountID: "off", Balances: []quota.Balance{usd(0)}})}
	s.checkQuotaAlerts(rep)
	if _, ok := s.balanceAlerts.sent["ds"]; !ok || len(s.balanceAlerts.sent) != 1 {
		t.Fatalf("want only the enabled account alerted, got %v", s.balanceAlerts.sent)
	}
}
