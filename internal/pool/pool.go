package pool

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
)

type AccountState string

const (
	StateReady       AccountState = "ready"
	StateBusy        AccountState = "busy"
	StateCoolingDown AccountState = "cooling_down"
	StateLoginNeeded AccountState = "login_needed"
	StateDisabled    AccountState = "disabled"
)

// latencyEWMAWeight is the weight given to the newest latency sample.
const latencyEWMAWeight = 0.3

var (
	ErrNoAvailableAccount = errors.New("no available account for request")
	// ErrSessionWaitForReset marks a request refused because its conversation's
	// account has no quota left and the conversation must not switch accounts.
	ErrSessionWaitForReset = errors.New("conversation's account has no quota left")
	ErrStrategyOptInNeeded = errors.New("round-robin and least-busy are intended for API-key pools and can cause bans on subscription (ACP/CLI) accounts; use failover or acknowledge the risk in Routing settings")
)

type HealthBanner struct {
	AccountID   string    `json:"account_id"`
	AccountName string    `json:"account_name"`
	Severity    string    `json:"severity"` // "critical", "warning"
	Message     string    `json:"message"`
	Time        time.Time `json:"time"`
}

type AccountSnapshot struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Kind           string       `json:"kind"`
	Backend        string       `json:"backend"`
	Enabled        bool         `json:"enabled"`
	State          AccountState `json:"state"`
	InFlight       int          `json:"in_flight"`
	MaxConcurrency int          `json:"max_concurrency"`
	TotalRequests  int64        `json:"total_requests"`
	TotalErrors    int64        `json:"total_errors"`
	HourRequests   int          `json:"hour_requests"`
	DayRequests    int          `json:"day_requests"`
	HourlyLimit    int          `json:"hourly_limit,omitempty"`
	DailyLimit     int          `json:"daily_limit,omitempty"`
	CooldownUntil  *time.Time   `json:"cooldown_until,omitempty"`
	LastUsedAt     *time.Time   `json:"last_used_at,omitempty"`
	LastError      string       `json:"last_error,omitempty"`
	AutoDisabled   bool         `json:"auto_disabled,omitempty"`
	Banner         string       `json:"banner,omitempty"`
}

type runtimeState struct {
	inFlight           int
	totalRequests      int64
	totalErrors        int64
	requestTimestamps  []time.Time
	cooldownUntil      time.Time
	groupCooldowns     map[string]time.Time // quota group -> cooldown end
	group429           map[string]int       // quota group -> consecutive 429s
	ewmaLatencyMs      float64              // 0 until measured
	consecutive429     int
	consecutiveAuthErr int
	loginNeeded        bool
	autoDisabled       bool
	banner             string
	lastUsedAt         time.Time
	lastError          string
}

// Manager tracks health, concurrency, rate-limit cooldowns, budgets, and selection state across all accounts.
type Manager struct {
	mu            sync.Mutex
	now           func() time.Time
	accounts      map[string]*config.AccountConfig
	states        map[string]*runtimeState
	stickyMap     map[string]string // sessionKey -> accountID
	sessionPins   map[string]sessionPin
	rrCursor      map[string]int // ruleKey -> round-robin index
	onAutoDisable func(accountID, reason string)
}

func NewManager(accounts []config.AccountConfig) *Manager {
	m := &Manager{
		now:         time.Now,
		accounts:    make(map[string]*config.AccountConfig),
		states:      make(map[string]*runtimeState),
		stickyMap:   make(map[string]string),
		sessionPins: make(map[string]sessionPin),
		rrCursor:    make(map[string]int),
	}
	m.SyncAccounts(accounts)
	return m
}

func (m *Manager) SetNowFunc(fn func() time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.now = fn
}

func (m *Manager) SetAutoDisableHook(fn func(accountID, reason string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onAutoDisable = fn
}

// SyncAccounts updates account configs while preserving live health counters.
func (m *Manager) SyncAccounts(accounts []config.AccountConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := make(map[string]bool, len(accounts))
	for i := range accounts {
		a := accounts[i]
		cp := a
		seen[a.ID] = true
		m.accounts[a.ID] = &cp
		st, ok := m.states[a.ID]
		if !ok {
			st = &runtimeState{}
			m.states[a.ID] = st
		}
		// If account is enabled, clear any stale auto-disable / auth banner state
		// (including banners loaded from DisabledReason on daemon startup).
		if a.Enabled {
			if st.autoDisabled || st.loginNeeded || st.banner != "" {
				st.autoDisabled = false
				st.loginNeeded = false
				st.consecutiveAuthErr = 0
				st.banner = ""
			}
		} else if a.DisabledReason != "" {
			st.banner = a.DisabledReason
		} else {
			st.banner = ""
		}
	}
	for id := range m.accounts {
		if !seen[id] {
			delete(m.accounts, id)
			delete(m.states, id)
		}
	}
}

func (m *Manager) pruneTimestampsLocked(st *runtimeState, now time.Time) (hourCount, dayCount int) {
	cutoffDay := now.Add(-24 * time.Hour)
	cutoffHour := now.Add(-1 * time.Hour)
	kept := st.requestTimestamps[:0]
	for _, ts := range st.requestTimestamps {
		if ts.After(cutoffDay) {
			kept = append(kept, ts)
			dayCount++
			if ts.After(cutoffHour) {
				hourCount++
			}
		}
	}
	st.requestTimestamps = kept
	return hourCount, dayCount
}

func (m *Manager) stateLocked(a *config.AccountConfig, st *runtimeState, now time.Time) AccountState {
	if !a.Enabled || st.autoDisabled {
		return StateDisabled
	}
	if st.loginNeeded {
		return StateLoginNeeded
	}
	if !st.cooldownUntil.IsZero() && now.Before(st.cooldownUntil) {
		return StateCoolingDown
	}
	if st.inFlight >= a.MaxConcurrency {
		return StateBusy
	}
	return StateReady
}

func (m *Manager) isEligibleLocked(a *config.AccountConfig, st *runtimeState, quotaGroup string, now time.Time) (bool, string) {
	if !a.Enabled || st.autoDisabled {
		return false, "disabled"
	}
	if st.loginNeeded {
		return false, "login needed"
	}
	if !st.cooldownUntil.IsZero() && now.Before(st.cooldownUntil) {
		return false, fmt.Sprintf("cooling down until %s", st.cooldownUntil.Format(time.Kitchen))
	}
	if until := st.groupCooldowns[quotaGroup]; quotaGroup != "" && now.Before(until) {
		return false, fmt.Sprintf("%s cooling down until %s", quotaGroup, until.Format(time.Kitchen))
	}
	if st.inFlight >= a.MaxConcurrency {
		return false, "at concurrency cap"
	}
	hourCount, dayCount := m.pruneTimestampsLocked(st, now)
	if a.HourlyLimit > 0 && hourCount >= a.HourlyLimit {
		return false, "hourly budget reached"
	}
	if a.DailyLimit > 0 && dayCount >= a.DailyLimit {
		return false, "daily budget reached"
	}
	return true, ""
}

type SelectRequest struct {
	RuleKey                  string
	Strategy                 string
	CandidateIDs             []string
	SessionKey               string
	SubscriptionWarningAcked bool
	ExcludeIDs               map[string]bool
	BudgetExceededIDs        map[string]bool
	QuotaUsedPct             map[string]float64 // accountID -> highest used % across quota windows
	QuotaThresholdPct        int                // prefer accounts with QuotaUsedPct < threshold when > 0
	QuotaGroup               string             // model family being requested; "" ignores group cooldowns
	// QuotaExhausted lists accounts whose quota for the requested model is used
	// up, with the reset time (zero when unknown). New conversations skip them.
	QuotaExhausted map[string]time.Time
	// SwitchExhaustedSessions lets a conversation that already ran on an
	// account move to another one when that account runs out of quota. When
	// false the request fails with ErrSessionWaitForReset until the reset.
	SwitchExhaustedSessions bool
}

// sessionPin records the account a conversation last succeeded on.
type sessionPin struct {
	accountID string
	at        time.Time
}

const (
	maxSessionPins = 4096
	sessionPinTTL  = 7 * 24 * time.Hour
)

func sessionPinKey(sessionKey, backendID string) string {
	return sessionKey + "\x00" + backendID
}

// PinSession records that sessionKey's conversation ran on accountID, so later
// requests in it stay on that account. Empty session keys are ignored.
func (m *Manager) PinSession(sessionKey, accountID string) {
	if sessionKey == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.accounts[accountID]
	if !ok {
		return
	}
	now := m.now()
	if len(m.sessionPins) >= maxSessionPins {
		for k, p := range m.sessionPins {
			if now.Sub(p.at) > sessionPinTTL {
				delete(m.sessionPins, k)
			}
		}
	}
	m.sessionPins[sessionPinKey(sessionKey, a.Backend)] = sessionPin{accountID: accountID, at: now}
}

// pinnedLocked returns the candidate account sessionKey's conversation last
// succeeded on, or "" for a new conversation.
func (m *Manager) pinnedLocked(sessionKey string, candidateIDs []string, now time.Time) string {
	if sessionKey == "" {
		return ""
	}
	seen := make(map[string]bool, 1)
	for _, id := range candidateIDs {
		a, ok := m.accounts[id]
		if !ok || seen[a.Backend] {
			continue
		}
		seen[a.Backend] = true
		p, ok := m.sessionPins[sessionPinKey(sessionKey, a.Backend)]
		if !ok || now.Sub(p.at) > sessionPinTTL {
			continue
		}
		for _, cid := range candidateIDs {
			if cid == p.accountID {
				return p.accountID
			}
		}
	}
	return ""
}

// quotaBlockLocked reports whether the account is out of quota for the request
// (live quota used up, or a rate-limit cooldown) and when that ends.
func (m *Manager) quotaBlockLocked(id string, req SelectRequest, now time.Time) (bool, time.Time) {
	if until, ok := req.QuotaExhausted[id]; ok {
		return true, until
	}
	st := m.states[id]
	if st == nil {
		return false, time.Time{}
	}
	if now.Before(st.cooldownUntil) {
		return true, st.cooldownUntil
	}
	if until := st.groupCooldowns[req.QuotaGroup]; req.QuotaGroup != "" && now.Before(until) {
		return true, until
	}
	return false, time.Time{}
}

func sessionWaitError(name string, until, now time.Time) error {
	be := &backend.BackendError{
		Kind:       backend.ErrKindRateLimit,
		StatusCode: 429,
		Cause:      ErrSessionWaitForReset,
	}
	when := "its quota resets"
	if !until.IsZero() {
		when = "it resets at " + until.Local().Format("Jan 2 15:04")
		be.RetryAfter = max(int(until.Sub(now).Seconds()), 1)
	}
	be.Message = fmt.Sprintf("%s has no quota left for this model; this conversation stays on that account until %s (set [sessions] switch_account_when_exhausted = true to switch accounts instead)", name, when)
	return be
}

type Lease struct {
	Account config.AccountConfig
	release func()
}

func (l *Lease) Release() {
	if l != nil && l.release != nil {
		l.release()
		l.release = nil
	}
}

// Acquire picks an eligible account according to the strategy and reserves one concurrency slot.
// Default strategy is primary-first failover. round-robin and least-busy work freely for API-key
// accounts, and require explicit SubscriptionWarningAcked opt-in when subscription (ACP/CLI)
// accounts are in the candidate pool.
func (m *Manager) Acquire(req SelectRequest) (*Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()

	strategy := normalizeStrategy(req.Strategy)

	if err := m.optInCheckLocked(strategy, req); err != nil {
		return nil, err
	}

	pinnedID := m.pinnedLocked(req.SessionKey, req.CandidateIDs, now)
	if pinnedID != "" && !req.SwitchExhaustedSessions {
		if blocked, until := m.quotaBlockLocked(pinnedID, req, now); blocked {
			return nil, sessionWaitError(m.accounts[pinnedID].Backend+"/"+m.accounts[pinnedID].Name, until, now)
		}
	}

	eligible, err := m.eligibleLocked(req, pinnedID, now, nil)
	if err != nil {
		return nil, err
	}

	var chosen *config.AccountConfig
	if pinnedID != "" && strategy != config.StrategyManual {
		for _, a := range eligible {
			if a.ID == pinnedID {
				chosen = a
				break
			}
		}
	}

	switch {
	case chosen != nil:
		// An existing conversation stays on its account to keep its context.
		if strategy == config.StrategySticky && req.SessionKey != "" {
			m.stickyMap[req.SessionKey] = chosen.ID
		}
	default:
		chosen = m.pickLocked(strategy, req, eligible, true)
	}

	st := m.states[chosen.ID]
	st.inFlight++
	st.totalRequests++
	st.lastUsedAt = now
	st.requestTimestamps = append(st.requestTimestamps, now)

	acctCopy := *chosen
	id := chosen.ID
	released := false
	return &Lease{
		Account: acctCopy,
		release: func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if released {
				return
			}
			released = true
			if s, ok := m.states[id]; ok && s.inFlight > 0 {
				s.inFlight--
			}
		},
	}, nil
}

// optInCheckLocked enforces the explicit ban-risk opt-in for round-robin and
// least-busy when subscription (ACP / CLI) accounts are candidates.
func (m *Manager) optInCheckLocked(strategy string, req SelectRequest) error {
	if strategy != config.StrategyRoundRobin && strategy != config.StrategyLeastBusy {
		return nil
	}
	for _, id := range req.CandidateIDs {
		if a, ok := m.accounts[id]; ok && (a.Kind == config.KindACP || a.Kind == config.KindCLI) {
			if !req.SubscriptionWarningAcked {
				return ErrStrategyOptInNeeded
			}
			return nil
		}
	}
	return nil
}

func normalizeStrategy(strategy string) string {
	switch strategy {
	case config.StrategyFailover, config.StrategySticky, config.StrategyManual, config.StrategyRoundRobin, config.StrategyLeastBusy, config.StrategyMostQuota, config.StrategyFastest:
		return strategy
	default:
		return config.StrategyFailover
	}
}

// eligibleLocked returns the candidates a request may use, in priority order.
// When note is non-nil it is called with each candidate's status and reason.
func (m *Manager) eligibleLocked(req SelectRequest, pinnedID string, now time.Time, note func(id, status, reason string)) ([]*config.AccountConfig, error) {
	if note == nil {
		note = func(string, string, string) {}
	}
	eligible := make([]*config.AccountConfig, 0, len(req.CandidateIDs))
	var exhausted []*config.AccountConfig
	var reasons []string
	for _, id := range req.CandidateIDs {
		if req.ExcludeIDs != nil && req.ExcludeIDs[id] {
			note(id, "skipped", "already tried")
			continue
		}
		a, ok := m.accounts[id]
		if !ok {
			note(id, "skipped", "unknown account")
			continue
		}
		if req.BudgetExceededIDs != nil && req.BudgetExceededIDs[id] {
			reasons = append(reasons, fmt.Sprintf("%s (USD budget exceeded)", a.Name))
			note(id, "skipped", "USD budget exceeded")
			continue
		}
		st := m.states[id]
		if okElig, reason := m.isEligibleLocked(a, st, req.QuotaGroup, now); okElig {
			if until, out := req.QuotaExhausted[id]; out {
				exhausted = append(exhausted, a)
				r := "no quota left for this model"
				if !until.IsZero() {
					r += ", resets " + until.Local().Format("Jan 2 15:04")
				}
				note(id, "quota used up", r)
				continue
			}
			eligible = append(eligible, a)
			note(id, "eligible", "")
		} else {
			reasons = append(reasons, fmt.Sprintf("%s (%s)", a.Name, reason))
			note(id, "skipped", reason)
		}
	}

	// Quota data can be a few minutes stale, so when every usable account looks
	// used up, try them anyway and let the provider decide.
	if len(eligible) == 0 {
		eligible = exhausted
	}
	if len(eligible) == 0 {
		if len(reasons) > 0 {
			return nil, fmt.Errorf("%w: %v", ErrNoAvailableAccount, reasons)
		}
		return nil, ErrNoAvailableAccount
	}

	// If a quota threshold is configured (e.g. 90%), prefer accounts below that threshold
	// while still falling back to all eligible accounts if every candidate is above it.
	if req.QuotaThresholdPct > 0 && len(req.QuotaUsedPct) > 0 {
		thresh := float64(req.QuotaThresholdPct)
		under := make([]*config.AccountConfig, 0, len(eligible))
		for _, a := range eligible {
			if used, ok := req.QuotaUsedPct[a.ID]; !ok || used < thresh || a.ID == pinnedID {
				under = append(under, a)
			}
		}
		if len(under) > 0 {
			eligible = under
		}
	}
	return eligible, nil
}

// pickLocked chooses among eligible accounts by strategy. With commit false it
// only previews the choice and leaves round-robin and sticky state unchanged.
func (m *Manager) pickLocked(strategy string, req SelectRequest, eligible []*config.AccountConfig, commit bool) *config.AccountConfig {
	var chosen *config.AccountConfig
	switch strategy {
	case config.StrategyManual, config.StrategyFailover:
		chosen = eligible[0]

	case config.StrategyMostQuota:
		chosen = eligible[0]
		bestUsed := req.QuotaUsedPct[chosen.ID]
		for _, a := range eligible[1:] {
			u := req.QuotaUsedPct[a.ID]
			if u < bestUsed {
				chosen = a
				bestUsed = u
			}
		}

	case config.StrategyFastest:
		chosen = eligible[0]
		for _, a := range eligible[1:] {
			if m.states[a.ID].ewmaLatencyMs < m.states[chosen.ID].ewmaLatencyMs {
				chosen = a
			}
		}

	case config.StrategyRoundRobin:
		if req.SessionKey != "" {
			if pinnedID, ok := m.stickyMap[req.SessionKey]; ok {
				for _, a := range eligible {
					if a.ID == pinnedID {
						chosen = a
						break
					}
				}
			}
		}
		if chosen == nil {
			key := req.RuleKey
			idx := m.rrCursor[key] % len(eligible)
			chosen = eligible[idx]
			if commit {
				m.rrCursor[key] = idx + 1
				if req.SessionKey != "" {
					m.stickyMap[req.SessionKey] = chosen.ID
				}
			}
		}

	case config.StrategyLeastBusy:
		chosen = eligible[0]
		for _, a := range eligible[1:] {
			stA := m.states[a.ID]
			stC := m.states[chosen.ID]
			if stA.inFlight < stC.inFlight || (stA.inFlight == stC.inFlight && stA.lastUsedAt.Before(stC.lastUsedAt)) {
				chosen = a
			}
		}

	default: // StrategySticky (primary-first on new/fallback sessions, never LRU-alternating)
		if req.SessionKey != "" {
			if pinnedID, ok := m.stickyMap[req.SessionKey]; ok {
				for _, a := range eligible {
					if a.ID == pinnedID {
						chosen = a
						break
					}
				}
			}
		}
		if chosen == nil {
			chosen = eligible[0]
		}
		if commit && req.SessionKey != "" {
			m.stickyMap[req.SessionKey] = chosen.ID
		}
	}
	return chosen
}

// RecordSuccess resets transient auth/429 counters and clears any stale auth/disable banner.
func (m *Manager) RecordSuccess(accountID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.states[accountID]; ok {
		st.consecutive429 = 0
		st.group429 = nil
		st.consecutiveAuthErr = 0
		st.loginNeeded = false
		st.autoDisabled = false
		st.banner = ""
		st.lastError = ""
	}
}

// RecordLatency folds a request duration into the account's moving average,
// which StrategyFastest selects on. Non-positive durations are ignored.
func (m *Manager) RecordLatency(accountID string, durMs int64) {
	if durMs <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.states[accountID]
	if !ok {
		return
	}
	if st.ewmaLatencyMs == 0 {
		st.ewmaLatencyMs = float64(durMs)
		return
	}
	st.ewmaLatencyMs = latencyEWMAWeight*float64(durMs) + (1-latencyEWMAWeight)*st.ewmaLatencyMs
}

// RecordError updates health state according to section 7 rules.
// Returns whether the gateway is allowed to fail over to another account.
func (m *Manager) RecordError(accountID string, err error, rule config.RoutingRule) (allowFailover bool) {
	return m.RecordErrorForGroup(accountID, "", err, rule)
}

// RecordErrorForGroup is RecordError for a request against one quota group: a
// rate limit then cools down only that group. Other error kinds affect the
// whole account.
func (m *Manager) RecordErrorForGroup(accountID, quotaGroup string, err error, rule config.RoutingRule) (allowFailover bool) {
	m.mu.Lock()
	now := m.now()
	a, okA := m.accounts[accountID]
	st, okS := m.states[accountID]
	if !okA || !okS {
		m.mu.Unlock()
		return false
	}

	st.totalErrors++
	st.lastError = err.Error()

	var autoDisableReason string
	be, isBE := backend.AsBackendError(err)
	if !isBE {
		// Treat untyped network/process errors as transient.
		m.mu.Unlock()
		return rule.Strategy == config.StrategyFailover
	}

	switch be.Kind {
	case backend.ErrKindTransient:
		allowFailover = rule.Strategy == config.StrategyFailover

	case backend.ErrKindRateLimit:
		var count int
		if quotaGroup != "" {
			if st.group429 == nil {
				st.group429 = make(map[string]int)
			}
			st.group429[quotaGroup]++
			count = st.group429[quotaGroup]
		} else {
			st.consecutive429++
			count = st.consecutive429
		}
		cooldown := 60 * time.Second
		if be.RetryAfter > 0 {
			cooldown = time.Duration(be.RetryAfter) * time.Second
		} else {
			mult := 1 << min(count-1, 4)
			cooldown = time.Duration(mult) * 60 * time.Second
			if cooldown > 10*time.Minute {
				cooldown = 10 * time.Minute
			}
		}
		if quotaGroup != "" {
			if st.groupCooldowns == nil {
				st.groupCooldowns = make(map[string]time.Time)
			}
			st.groupCooldowns[quotaGroup] = now.Add(cooldown)
		} else {
			st.cooldownUntil = now.Add(cooldown)
		}
		allowFailover = rule.AllowCrossAccountRetryOn429 && rule.Strategy != config.StrategyManual

	case backend.ErrKindAuth:
		st.consecutiveAuthErr++
		st.loginNeeded = true
		if st.consecutiveAuthErr >= 2 {
			st.autoDisabled = true
			a.Enabled = false
			autoDisableReason = fmt.Sprintf("Disabled after repeated auth errors (%d): stop using this account and re-authenticate or check provider status.", be.StatusCode)
			st.banner = autoDisableReason
		}
		allowFailover = false

	case backend.ErrKindRestricted:
		st.autoDisabled = true
		a.Enabled = false
		autoDisableReason = "Account restricted by provider. Stop using this account immediately to protect it."
		st.banner = autoDisableReason
		allowFailover = false

	default:
		allowFailover = false
	}

	hook := m.onAutoDisable
	m.mu.Unlock()

	if autoDisableReason != "" && hook != nil {
		hook(accountID, autoDisableReason)
	}
	return allowFailover
}

// ClearHealth resets cooldown, login-needed, and auto-disable flags (e.g. after re-login or manual re-enable).
func (m *Manager) ClearHealth(accountID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st, ok := m.states[accountID]; ok {
		st.cooldownUntil = time.Time{}
		st.groupCooldowns = nil
		st.group429 = nil
		st.consecutive429 = 0
		st.consecutiveAuthErr = 0
		st.loginNeeded = false
		st.autoDisabled = false
		st.banner = ""
		st.lastError = ""
	}
	if a, ok := m.accounts[accountID]; ok {
		a.Enabled = true
		a.DisabledReason = ""
	}
}

func (m *Manager) Snapshots() []AccountSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := make([]AccountSnapshot, 0, len(m.accounts))
	for id, a := range m.accounts {
		st := m.states[id]
		hourCount, dayCount := m.pruneTimestampsLocked(st, now)
		snap := AccountSnapshot{
			ID:             a.ID,
			Name:           a.Name,
			Kind:           a.Kind,
			Backend:        a.Backend,
			Enabled:        a.Enabled && !st.autoDisabled,
			State:          m.stateLocked(a, st, now),
			InFlight:       st.inFlight,
			MaxConcurrency: a.MaxConcurrency,
			TotalRequests:  st.totalRequests,
			TotalErrors:    st.totalErrors,
			HourRequests:   hourCount,
			DayRequests:    dayCount,
			HourlyLimit:    a.HourlyLimit,
			DailyLimit:     a.DailyLimit,
			LastError:      st.lastError,
			AutoDisabled:   st.autoDisabled,
			Banner:         st.banner,
		}
		if !st.cooldownUntil.IsZero() && now.Before(st.cooldownUntil) {
			t := st.cooldownUntil
			snap.CooldownUntil = &t
		}
		if !st.lastUsedAt.IsZero() {
			t := st.lastUsedAt
			snap.LastUsedAt = &t
		}
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Backend != out[j].Backend {
			return out[i].Backend < out[j].Backend
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (m *Manager) Banners() []HealthBanner {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []HealthBanner
	for id, a := range m.accounts {
		st := m.states[id]
		if st.banner != "" {
			out = append(out, HealthBanner{
				AccountID:   a.ID,
				AccountName: a.Name,
				Severity:    "critical",
				Message:     st.banner,
				Time:        st.lastUsedAt,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}
