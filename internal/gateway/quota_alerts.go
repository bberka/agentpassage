package gateway

import (
	"fmt"
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
// crossed [alerts] quota_threshold_pct since its last reset.
func (s *Server) checkQuotaAlerts(rep quota.Report) {
	s.mu.RLock()
	threshold := s.cfg.Alerts.QuotaThresholdPct
	s.mu.RUnlock()
	if threshold <= 0 || s.alerts == nil {
		return
	}
	toSend := s.quotaAlerts.pending(quotaAlertsFor(rep, threshold), time.Now())

	for _, a := range toSend {
		s.alerts.Notify(a.key, alerts.Event{
			Kind:     "quota_threshold",
			Severity: "warning",
			Account:  a.account,
			Title:    a.title,
			Message:  a.message,
		})
	}
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
