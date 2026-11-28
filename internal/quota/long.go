package quota

import (
	"strings"
	"time"
)

// Classes of limits that last long enough to be rationed over days.
const (
	ClassWeekly  = "weekly"
	ClassMonthly = "monthly"
	ClassBalance = "balance"
)

// LongLimit is one weekly or monthly window, or one prepaid balance, as the
// provider reports it.
type LongLimit struct {
	Class    string
	Scope    string // model group ID, or "" for an account-wide window
	Label    string // the window's label, or the balance's currency
	Value    float64
	ResetsAt time.Time // zero when unknown
}

// LongLimits returns the limits of q that came from the provider. Short windows
// such as 5-hour ones, and counts apx keeps itself, are left out.
func (q AccountQuota) LongLimits() []LongLimit {
	if !strings.HasSuffix(q.Source, "-live") || q.Source == "headers-live" {
		return nil
	}
	var out []LongLimit
	add := func(scope string, w *Window) {
		if w == nil {
			return
		}
		class := windowClass(w)
		if class == "" {
			return
		}
		l := LongLimit{Class: class, Scope: scope, Label: w.Label, Value: w.UsedPercent}
		if w.ResetsAt != nil {
			l.ResetsAt = *w.ResetsAt
		}
		out = append(out, l)
	}
	// agy's account-wide windows only repeat its tightest model group.
	if q.Backend != "agy" || len(q.Groups) == 0 {
		add("", q.Primary)
		add("", q.Secondary)
	}
	for _, g := range q.Groups {
		add(g.ID, g.Primary)
		add(g.ID, g.Secondary)
	}
	for _, b := range q.Balances {
		out = append(out, LongLimit{Class: ClassBalance, Label: b.Currency, Value: b.Amount})
	}
	return out
}

func windowClass(w *Window) string {
	const day = 24 * 60
	label := strings.ToLower(w.Label)
	switch {
	case w.WindowMins >= 6*day && w.WindowMins <= 8*day:
		return ClassWeekly
	case w.WindowMins >= 27*day:
		return ClassMonthly
	case w.WindowMins > 0 || strings.Contains(label, "5h") || strings.Contains(label, "hour"):
		return ""
	case strings.Contains(label, "month"):
		return ClassMonthly
	// A model family's own window, such as Claude's Fable one, is weekly.
	case strings.Contains(label, "week") || ModelFamily(label) != "":
		return ClassWeekly
	}
	return ""
}

// Meters reports whether a limit with this scope and label counts requests to
// upstreamModel on backend.
func Meters(backend, scope, label, upstreamModel string) bool {
	if backend == "agy" {
		group := GroupForModel(backend, upstreamModel)
		return scope == "" || group == "" || scope == group
	}
	family := ModelFamily(scope + " " + label)
	return family == "" || family == ModelFamily(upstreamModel)
}
