// Package limits rations a provider's weekly, monthly and balance limits over
// days: at most so much of them may be used in each period.
//
// It counts the change between the readings providers report, so it is only as
// exact as those, and use outside apx counts too.
package limits

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/quota"
)

// MaxDays is the longest period.
const MaxDays = 30

// Rules are the allowances of one account per period. Zero means no limit.
type Rules struct {
	Days    int
	Weekly  float64 // percentage points of the whole window
	Monthly float64
	Balance float64 // in the balance's currency
}

// ParseDays reads a period such as "1d" or "3d".
func ParseDays(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(s), "d"))
	if err != nil || n < 1 || n > MaxDays {
		return 0, fmt.Errorf("period must be 1d to %dd", MaxDays)
	}
	return n, nil
}

// For returns acct's rules: the default ones, with the values the account sets
// replacing them. A negative account value removes that limit.
func For(cfg *config.Config, acct config.AccountConfig) Rules {
	merged := cfg.Limits
	if own := acct.Limits; own != nil {
		pick := func(dst *float64, v float64) {
			if v != 0 {
				*dst = v
			}
		}
		pick(&merged.Weekly, own.Weekly)
		pick(&merged.Monthly, own.Monthly)
		pick(&merged.Balance, own.Balance)
		if own.Every != "" {
			merged.Every = own.Every
		}
	}
	days, err := ParseDays(merged.Every)
	if err != nil {
		days = 1
	}
	return Rules{Days: days, Weekly: max(merged.Weekly, 0), Monthly: max(merged.Monthly, 0), Balance: max(merged.Balance, 0)}
}

func (r Rules) allowance(class string) float64 {
	switch class {
	case quota.ClassWeekly:
		return r.Weekly
	case quota.ClassMonthly:
		return r.Monthly
	}
	return r.Balance
}

// entry is what apx remembers about one limit of one account.
type entry struct {
	Account  string    `json:"account"`
	Class    string    `json:"class"`
	Scope    string    `json:"scope,omitempty"`
	Label    string    `json:"label"`
	Start    time.Time `json:"start"` // local midnight the period began
	Spent    float64   `json:"spent"`
	Extra    float64   `json:"extra,omitempty"` // allowed on top in this period
	Last     float64   `json:"last"`            // newest reading
	ResetsAt time.Time `json:"resets_at,omitzero"`
	At       time.Time `json:"at"` // when the newest reading was taken
	Notified bool      `json:"notified,omitempty"`
}

// Status is one limit that has an allowance.
type Status struct {
	AccountID string        `json:"account_id"`
	Class     string        `json:"class"`
	Scope     string        `json:"scope,omitempty"`
	Label     string        `json:"label"`
	Used      float64       `json:"used"`
	Allowed   float64       `json:"allowed"`
	Days      int           `json:"days"`
	EndsIn    time.Duration `json:"-"`
	Reached   bool          `json:"reached"`
	key       string
}

// Amount formats a quantity of this limit.
func (s Status) Amount(v float64) string {
	if s.Class == quota.ClassBalance {
		return fmt.Sprintf("%.2f %s", v, s.Label)
	}
	return fmt.Sprintf("%.0f%%", v)
}

// Name is the limit in plain words, such as "weekly" or "weekly Fable".
func (s Status) Name() string {
	name := s.Class
	if s.Class != quota.ClassBalance && !strings.EqualFold(s.Label, s.Class) {
		name += " " + s.Label
	}
	if s.Scope != "" {
		name += " " + s.Scope
	}
	return name
}

// Period is the period in plain words: "daily" or "3 day".
func (s Status) Period() string {
	if s.Days == 1 {
		return "daily"
	}
	return fmt.Sprintf("%d day", s.Days)
}

// Tracker keeps the counts in limits.json, so a restart doesn't reset them.
type Tracker struct {
	mu   sync.Mutex
	path string
}

func New(dataDir string) *Tracker {
	return &Tracker{path: filepath.Join(dataDir, "limits.json")}
}

func (t *Tracker) load() (map[string]*entry, error) {
	entries := map[string]*entry{}
	data, err := os.ReadFile(t.path)
	if errors.Is(err, fs.ErrNotExist) {
		return entries, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &entries)
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", t.path, err)
	}
	return entries, nil
}

func (t *Tracker) save(entries map[string]*entry) error {
	data, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(t.path), "limits-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), t.path)
}

func midnight(t time.Time) time.Time {
	y, m, d := t.Local().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.Local)
}

// roll starts a new period when e's one is over.
func (e *entry) roll(days int, now time.Time) bool {
	if now.Before(e.Start.AddDate(0, 0, days)) {
		return false
	}
	e.Start, e.Spent, e.Extra, e.Notified = midnight(now), 0, 0, false
	return true
}

// lateStart is how old the previous reading may be for the change since it to
// still count in a period that began after it.
const lateStart = 15 * time.Minute

// add counts the change from the previous reading to l.
func (e *entry) add(l quota.LongLimit, at time.Time) {
	switch {
	case e.Class == quota.ClassBalance:
		// A top-up raises the balance and is not negative spending.
		e.Spent += max(e.Last-l.Value, 0)
		e.Last = l.Value
		return
	// The window reset: everything used since then is new. Providers may report
	// the old reset time for a while, so a clear drop counts as one too.
	case !e.ResetsAt.IsZero() && !at.Before(e.ResetsAt) && l.ResetsAt.After(e.ResetsAt.Add(time.Hour)),
		l.Value < e.Last-0.5 && (e.ResetsAt.IsZero() || l.ResetsAt.After(e.ResetsAt.Add(time.Hour))):
		e.Spent += l.Value
		e.Last = l.Value
	// Without a reset a small drop is noise, so the highest reading stays.
	case l.Value > e.Last:
		e.Spent += l.Value - e.Last
		e.Last = l.Value
	}
}

// Observe counts the readings in rep that are newer than the ones seen before.
func (t *Tracker) Observe(cfg *config.Config, rep quota.Report, now time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	entries, err := t.load()
	if err != nil {
		return err
	}
	changed := false
	for _, q := range rep.Accounts {
		acct := cfg.FindAccount(q.AccountID)
		if acct == nil || q.CheckedAt.IsZero() {
			continue
		}
		days := For(cfg, *acct).Days
		for _, l := range q.LongLimits() {
			key := strings.Join([]string{q.AccountID, l.Class, l.Scope, l.Label}, "|")
			e := entries[key]
			if e == nil {
				entries[key] = &entry{Account: q.AccountID, Class: l.Class, Scope: l.Scope, Label: l.Label,
					Start: midnight(now), Last: l.Value, ResetsAt: l.ResetsAt, At: q.CheckedAt}
				changed = true
				continue
			}
			if !q.CheckedAt.After(e.At) {
				continue
			}
			if e.roll(days, now) && e.At.Before(e.Start.Add(-lateStart)) {
				// Use between an old reading and this period isn't counted in it.
				e.Last = l.Value
			}
			e.add(l, q.CheckedAt)
			e.ResetsAt, e.At = l.ResetsAt, q.CheckedAt
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return t.save(entries)
}

func statuses(cfg *config.Config, entries map[string]*entry, now time.Time) []Status {
	var out []Status
	for key, e := range entries {
		acct := cfg.FindAccount(e.Account)
		if acct == nil {
			continue
		}
		rules := For(cfg, *acct)
		allowance := rules.allowance(e.Class)
		if allowance <= 0 {
			continue
		}
		cur := *e
		cur.roll(rules.Days, now)
		s := Status{
			AccountID: e.Account, Class: e.Class, Scope: e.Scope, Label: e.Label,
			Used: math.Round(cur.Spent*100) / 100, Allowed: allowance + cur.Extra,
			Days: rules.Days, EndsIn: cur.Start.AddDate(0, 0, rules.Days).Sub(now), key: key,
		}
		s.Reached = s.Used >= s.Allowed
		out = append(out, s)
	}
	slices.SortFunc(out, func(a, b Status) int { return strings.Compare(a.key, b.key) })
	return out
}

// Statuses lists every limit that has an allowance, by account.
func (t *Tracker) Statuses(cfg *config.Config, now time.Time) ([]Status, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entries, err := t.load()
	if err != nil {
		return nil, err
	}
	return statuses(cfg, entries, now), nil
}

// Announce returns the limits reached since the last call.
func (t *Tracker) Announce(cfg *config.Config, now time.Time) ([]Status, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entries, err := t.load()
	if err != nil {
		return nil, err
	}
	var out []Status
	for _, s := range statuses(cfg, entries, now) {
		e := entries[s.key]
		if s.Reached && !e.Notified {
			e.Notified = true
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, t.save(entries)
}

// Allow adds one more allowance, for this period only, to each of the account's
// limits that is reached. It returns how many it raised.
func (t *Tracker) Allow(cfg *config.Config, accountID string, now time.Time) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	entries, err := t.load()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, s := range statuses(cfg, entries, now) {
		if s.AccountID != accountID || !s.Reached {
			continue
		}
		e := entries[s.key]
		rules := For(cfg, *cfg.FindAccount(accountID))
		e.roll(rules.Days, now)
		// A full allowance becomes free again, however far past it the use went.
		e.Extra = max(e.Spent, s.Allowed) - s.Allowed + e.Extra + rules.allowance(e.Class)
		e.Notified = false
		n++
	}
	if n == 0 {
		return 0, nil
	}
	return n, t.save(entries)
}

// Blocking returns the reached limit that keeps the account from serving
// upstreamModel longest.
func Blocking(list []Status, accountID, backend, upstreamModel string) (Status, bool) {
	var out Status
	found := false
	for _, s := range list {
		if s.AccountID == accountID && s.Reached && quota.Meters(backend, s.Scope, s.Label, upstreamModel) && (!found || s.EndsIn > out.EndsIn) {
			out, found = s, true
		}
	}
	return out, found
}

// Fields are the settings of a LimitsConfig, in display order.
var Fields = []string{"every", quota.ClassWeekly, quota.ClassMonthly, quota.ClassBalance}

// Value returns a setting as text: "" when unset and "off" when removed.
func Value(c config.LimitsConfig, field string) string {
	if field == "every" {
		return c.Every
	}
	v := *number(&c, field)
	switch {
	case v < 0:
		return "off"
	case v == 0:
		return ""
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func number(c *config.LimitsConfig, field string) *float64 {
	switch field {
	case quota.ClassWeekly:
		return &c.Weekly
	case quota.ClassMonthly:
		return &c.Monthly
	}
	return &c.Balance
}

// SetValue changes one setting from text. "" unsets it, which in an account
// means the default. "off" removes the limit.
func SetValue(c *config.LimitsConfig, field, text string, account bool) error {
	text = strings.TrimSuffix(strings.TrimSpace(text), "%")
	if field == "every" {
		if text != "" {
			days, err := ParseDays(text)
			if err != nil {
				return err
			}
			text = fmt.Sprintf("%dd", days)
		}
		c.Every = text
		return nil
	}
	var v float64
	switch {
	case text == "off":
		if account {
			v = -1
		}
	case text != "":
		n, err := strconv.ParseFloat(text, 64)
		if err != nil || !(n > 0) || math.IsInf(n, 1) || (field != quota.ClassBalance && n > 100) {
			if field == quota.ClassBalance {
				return fmt.Errorf("balance must be an amount above 0, or off")
			}
			return fmt.Errorf("%s must be a percentage from 1 to 100, or off", field)
		}
		v = n
	}
	*number(c, field) = v
	return nil
}

// Describe lists, in one line each, how much of every limit of an account is
// used. An account with rules but nothing to count them against says so.
func Describe(cfg *config.Config, acct config.AccountConfig, list []Status) []string {
	var out []string
	for _, s := range list {
		if s.AccountID != acct.ID {
			continue
		}
		line := fmt.Sprintf("%s: %s of %s used (%s), %s left", s.Name(), s.Amount(s.Used), s.Amount(s.Allowed), s.Period(), quota.FormatResetIn(ptr(time.Now().Add(s.EndsIn)), time.Now()))
		if s.Reached {
			line += ", reached"
		}
		out = append(out, line)
	}
	if r := For(cfg, acct); len(out) == 0 && r.Weekly+r.Monthly+r.Balance > 0 {
		out = append(out, "not limited: no matching limit reported by the provider yet")
	}
	return out
}

func ptr[T any](v T) *T { return &v }
