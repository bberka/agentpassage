package limits

import (
	"testing"
	"time"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/quota"
)

func testConfig() *config.Config {
	cfg := config.Default()
	cfg.Limits = config.LimitsConfig{Weekly: 20, Balance: 1}
	cfg.Accounts = []config.AccountConfig{{ID: "claude-a", Name: "a", Backend: "claude", Kind: config.KindACP}}
	return cfg
}

func report(at time.Time, weekly, balance float64, resets time.Time) quota.Report {
	return quota.Report{Accounts: []quota.AccountQuota{{
		AccountID: "claude-a", Backend: "claude", Source: "claude-live", CheckedAt: at,
		Primary:  &quota.Window{Label: "Weekly", UsedPercent: weekly, ResetsAt: &resets},
		Balances: []quota.Balance{{Currency: "USD", Amount: balance}},
	}}}
}

func used(t *testing.T, tr *Tracker, cfg *config.Config, now time.Time) (weekly, balance Status) {
	t.Helper()
	list, err := tr.Statuses(cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.Class == quota.ClassWeekly {
			weekly = s
		} else if s.Class == quota.ClassBalance {
			balance = s
		}
	}
	return weekly, balance
}

func TestCountsUsePerPeriod(t *testing.T) {
	cfg, dir := testConfig(), t.TempDir()
	tr := New(dir)
	day := time.Date(2026, 3, 10, 9, 0, 0, 0, time.Local)
	reset := day.Add(5 * time.Hour)
	observe := func(at time.Time, weekly, balance float64, resets time.Time) {
		t.Helper()
		if err := tr.Observe(cfg, report(at, weekly, balance, resets), at); err != nil {
			t.Fatal(err)
		}
	}
	check := func(at time.Time, weekly, balance float64) {
		t.Helper()
		w, b := used(t, tr, cfg, at)
		if w.Used != weekly || b.Used != balance {
			t.Fatalf("at %s used weekly %v balance %v, want %v and %v", at.Format("02 15:04"), w.Used, b.Used, weekly, balance)
		}
	}

	observe(day, 40, 10, reset) // the first reading is the baseline
	check(day, 0, 0)
	observe(day.Add(time.Hour), 52, 9.5, reset)
	observe(day.Add(time.Hour), 52, 9.5, reset) // the same reading counts once
	observe(day.Add(30*time.Minute), 45, 9.8, reset)
	check(day.Add(time.Hour), 12, 0.5)
	observe(day.Add(2*time.Hour), 51.8, 20, reset) // noise, and a top-up
	check(day.Add(2*time.Hour), 12, 0.5)

	// The window resets inside the period: no negative or free use.
	next := reset.Add(7 * 24 * time.Hour)
	observe(day.Add(6*time.Hour), 9, 19, next)
	w, b := used(t, tr, cfg, day.Add(6*time.Hour))
	if w.Used != 21 || !w.Reached || b.Used != 1.5 || !b.Reached || w.EndsIn != 9*time.Hour {
		t.Fatalf("after reset: %+v %+v", w, b)
	}

	// A restart keeps the count, and a limit is announced once.
	tr = New(dir)
	if got, err := tr.Announce(cfg, day.Add(6*time.Hour)); err != nil || len(got) != 2 {
		t.Fatalf("announce = %v, %v", got, err)
	}
	if got, _ := tr.Announce(cfg, day.Add(6*time.Hour)); len(got) != 0 {
		t.Fatalf("announced twice: %v", got)
	}

	// Allowing more frees one more allowance.
	if n, err := tr.Allow(cfg, "claude-a", day.Add(6*time.Hour)); err != nil || n != 2 {
		t.Fatalf("allow = %d, %v", n, err)
	}
	if w, _ := used(t, tr, cfg, day.Add(6*time.Hour)); w.Reached || w.Allowed-w.Used != 20 {
		t.Fatalf("after allowing: %+v", w)
	}

	// The next day starts over; use while apx was off isn't counted.
	morning := day.Add(24 * time.Hour)
	check(morning, 0, 0)
	observe(morning, 30, 15, next)
	check(morning, 0, 0)
	if w, _ := used(t, tr, cfg, morning); w.Allowed != 20 {
		t.Fatalf("extra allowance outlived its period: %+v", w)
	}
}

func TestUseAcrossMidnightCountsInTheNewDay(t *testing.T) {
	cfg, tr := testConfig(), New(t.TempDir())
	before := time.Date(2026, 3, 10, 23, 58, 0, 0, time.Local)
	after := before.Add(5 * time.Minute)
	reset := before.Add(72 * time.Hour)
	for _, r := range []quota.Report{report(before, 10, 5, reset), report(after, 13, 5, reset)} {
		if err := tr.Observe(cfg, r, r.Accounts[0].CheckedAt); err != nil {
			t.Fatal(err)
		}
	}
	if w, _ := used(t, tr, cfg, after); w.Used != 3 {
		t.Fatalf("used %v", w.Used)
	}
}

func TestRulesAndMetering(t *testing.T) {
	cfg := testConfig()
	acct := cfg.Accounts[0]
	acct.Limits = &config.LimitsConfig{Every: "3d", Weekly: -1, Monthly: 5}
	if got := For(cfg, acct); got != (Rules{Days: 3, Monthly: 5, Balance: 1}) {
		t.Fatalf("rules %+v", got)
	}
	list := []Status{{AccountID: "claude-a", Class: quota.ClassWeekly, Label: "Fable", Reached: true}}
	if _, ok := Blocking(list, "claude-a", "claude", "sonnet"); ok {
		t.Fatal("the Fable limit blocked another model")
	}
	if _, ok := Blocking(list, "claude-a", "claude", "claude-fable-5"); !ok {
		t.Fatal("the Fable limit didn't block Fable")
	}
	short := quota.AccountQuota{Source: "agy-live", Backend: "agy", Groups: []quota.QuotaGroup{{
		ID: quota.GroupGemini, Primary: &quota.Window{Label: "5h"}, Secondary: &quota.Window{Label: "Weekly"},
	}}}
	if got := short.LongLimits(); len(got) != 1 || got[0].Label != "Weekly" {
		t.Fatalf("long limits %+v", got)
	}
	var c config.LimitsConfig
	if SetValue(&c, "weekly", "120", false) == nil || SetValue(&c, "every", "0d", false) == nil {
		t.Fatal("accepted values out of range")
	}
}
