package gateway

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bberka/agentpassage/internal/alerts"
	"github.com/bberka/agentpassage/internal/quota"
)

// quotaAlertState remembers which limit windows already sent a low-quota
// alert, so each window alerts once per period instead of on every refresh.
type quotaAlertState struct {
	mu   sync.Mutex
	sent map[string]time.Time // alert key -> when the window resets (zero if unknown)
}

type quotaAlert struct {
	key     string
	resets  time.Time
	account string
	title   string
	message string
}

// quotaAlertsFor lists every limit window at or above thresholdPct used:
// the account-wide windows and each model group (agy Gemini and Claude & GPT,
// Claude Fable).
func quotaAlertsFor(rep quota.Report, thresholdPct int) []quotaAlert {
	var out []quotaAlert
	add := func(aq quota.AccountQuota, scope string, w *quota.Window) {
		if w == nil || w.UsedPercent < float64(thresholdPct) {
			return
		}
		name := w.Label
		if scope != "" {
			name = scope + " " + w.Label
		}
		reset := ""
		resetKey := "none"
		var resets time.Time
		if w.ResetsAt != nil {
			resets = *w.ResetsAt
			reset = ", resets " + w.ResetsAt.Local().Format("Jan 2 15:04")
			resetKey = w.ResetsAt.UTC().Truncate(time.Hour).Format(time.RFC3339)
		}
		out = append(out, quotaAlert{
			key:     fmt.Sprintf("quota:%s:%s:%s:%s", aq.AccountID, scope, w.Label, resetKey),
			resets:  resets,
			account: aq.AccountID,
			title:   fmt.Sprintf("apx: %s %s limit at %.0f%%", aq.AccountName, name, w.UsedPercent),
			message: fmt.Sprintf("%s has used %.0f%% of its %s limit (%.0f%% left%s).", aq.AccountName, w.UsedPercent, name, w.LeftPercent, reset),
		})
	}
	for _, aq := range rep.Accounts {
		add(aq, "", aq.Primary)
		add(aq, "", aq.Secondary)
		for _, g := range aq.Groups {
			add(aq, g.Name, g.Primary)
			add(aq, g.Name, g.Secondary)
		}
	}
	return out
}

// checkQuotaAlerts sends a quota_threshold alert for each limit window that
// crossed [alerts] quota_threshold_pct since its last reset, and a
// balance_threshold alert for each account low on prepaid balance.
//
// Nothing is marked as sent while alerts cannot be delivered, so enabling them
// later still reports whatever is low at that point.
func (s *Server) checkQuotaAlerts(rep quota.Report) {
	if s.alerts == nil {
		return
	}
	cfg := s.alerts.Config()
	deliver := cfg.Enabled && (cfg.Desktop || strings.TrimSpace(cfg.WebhookURL) != "")
	now := time.Now()

	var toSend []quotaAlert
	if deliver && cfg.QuotaThresholdPct > 0 {
		toSend = s.quotaAlerts.pending(quotaAlertsFor(rep, cfg.QuotaThresholdPct), now)
	}
	for _, a := range toSend {
		s.alerts.Notify(a.key, alerts.Event{
			Kind:     "quota_threshold",
			Severity: "warning",
			Account:  a.account,
			Title:    a.title,
			Message:  a.message,
		})
	}

	remind := time.Duration(cfg.ReminderHours) * time.Hour
	for _, a := range s.balanceAlerts.pending(s.enabledAccounts(rep), cfg.BalanceThreshold, remind, now, deliver) {
		s.alerts.Notify(a.key, alerts.Event{
			Kind:     "balance_threshold",
			Severity: "warning",
			Account:  a.account,
			Title:    a.title,
			Message:  a.message,
		})
	}
}

// enabledAccounts drops disabled accounts from rep: they are no longer
// refreshed, so their cached balance would be repeated forever.
func (s *Server) enabledAccounts(rep quota.Report) []quota.AccountQuota {
	s.mu.RLock()
	defer s.mu.RUnlock()
	disabled := make(map[string]bool)
	for _, a := range s.cfg.Accounts {
		if !a.Enabled {
			disabled[a.ID] = true
		}
	}
	var out []quota.AccountQuota
	for _, aq := range rep.Accounts {
		if !disabled[aq.AccountID] {
			out = append(out, aq)
		}
	}
	return out
}

// balanceAlertState remembers when each low account was last alerted.
type balanceAlertState struct {
	mu   sync.Mutex
	sent map[string]time.Time // account ID -> last alert
}

// pending returns the low-balance alerts due now. An account is low only when
// every currency it reports is below threshold: providers such as DeepSeek list
// unused currencies at 0.00 next to the funded one.
//
// A due alert repeats every remind (0 = once) until the account recovers. An
// account that reports no balance (failed fetch) keeps its state. With deliver
// false nothing is marked sent, but recoveries are still recorded. A threshold
// of 0 turns the feature off and clears all state.
func (st *balanceAlertState) pending(accounts []quota.AccountQuota, threshold float64, remind time.Duration, now time.Time, deliver bool) []quotaAlert {
	st.mu.Lock()
	defer st.mu.Unlock()
	if threshold <= 0 {
		st.sent = nil
		return nil
	}
	if st.sent == nil {
		st.sent = make(map[string]time.Time)
	}
	var out []quotaAlert
	for _, aq := range accounts {
		if len(aq.Balances) == 0 {
			continue
		}
		low := true
		parts := make([]string, 0, len(aq.Balances))
		for _, b := range aq.Balances {
			if b.Amount >= threshold {
				low = false
			}
			parts = append(parts, fmt.Sprintf("%.2f %s", b.Amount, b.Currency))
		}
		if !low {
			delete(st.sent, aq.AccountID)
			continue
		}
		if !deliver {
			continue
		}
		if last, done := st.sent[aq.AccountID]; done && (remind <= 0 || now.Sub(last) < remind) {
			continue
		}
		st.sent[aq.AccountID] = now
		left := strings.Join(parts, " / ")
		out = append(out, quotaAlert{
			key:     "balance:" + aq.AccountID,
			account: aq.AccountID,
			title:   fmt.Sprintf("apx: %s balance low (%s)", aq.AccountName, left),
			message: fmt.Sprintf("%s has %s left, below the alert threshold of %.2f.", aq.AccountName, left, threshold),
		})
	}
	return out
}

// pending returns the alerts not yet sent for their window and marks them sent.
// Entries are forgotten once their window resets.
func (st *quotaAlertState) pending(all []quotaAlert, now time.Time) []quotaAlert {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.sent == nil {
		st.sent = make(map[string]time.Time)
	}
	for k, until := range st.sent {
		if !until.IsZero() && now.After(until) {
			delete(st.sent, k)
		}
	}
	var out []quotaAlert
	for _, a := range all {
		if _, done := st.sent[a.key]; done {
			continue
		}
		st.sent[a.key] = a.resets
		out = append(out, a)
	}
	return out
}
