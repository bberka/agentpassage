package quota

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/usage"
)

// Window describes one quota window (e.g. 5h / hourly, Weekly / 7d, Monthly / 30d).
type Window struct {
	Label       string     `json:"label"`
	UsedPercent float64    `json:"used_percent"`
	LeftPercent float64    `json:"left_percent"`
	WindowMins  int        `json:"window_mins,omitempty"`
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
	UsedCount   int        `json:"used_count,omitempty"`
	LimitCount  int        `json:"limit_count,omitempty"`
}

// AccountQuota holds live provider subscription limits (when supported) plus local rolling counts.
type AccountQuota struct {
	AccountID   string    `json:"account_id"`
	AccountName string    `json:"account_name"`
	Backend     string    `json:"backend"`
	Kind        string    `json:"kind"`
	Source      string    `json:"source"` // "codex-live", "grok-live", "local-budget", "local-tracked", "unavailable"
	PlanType    string    `json:"plan_type,omitempty"`
	Credits     string    `json:"credits,omitempty"`
	Primary     *Window   `json:"primary,omitempty"`
	Secondary   *Window   `json:"secondary,omitempty"`
	Local1h     int       `json:"local_1h_reqs"`
	Local5h     int       `json:"local_5h_reqs"`
	Local24h    int       `json:"local_24h_reqs"`
	Local7d     int       `json:"local_7d_reqs"`
	Tokens5h    int       `json:"tokens_5h"`
	Tokens7d    int       `json:"tokens_7d"`
	HourlyLimit int       `json:"hourly_limit,omitempty"`
	DailyLimit  int       `json:"daily_limit,omitempty"`
	WeeklyLimit int       `json:"weekly_limit,omitempty"`
	CheckedAt   time.Time `json:"checked_at,omitempty"`
	Error       string    `json:"error,omitempty"`
}

type Report struct {
	UpdatedAt time.Time      `json:"updated_at"`
	Accounts  []AccountQuota `json:"accounts"`
}

// Fetcher collects official provider subscription limits (Codex `account/rateLimits/read`,
// DeepSeek `/user/balance`, OpenRouter `/auth/key`, Grok `grok usage --json`, Claude local
// JSONL 5h/7d blocks, and live HTTP rate-limit headers) and merges them with local usage.
type Fetcher struct {
	mu          sync.RWMutex
	dataDir     string
	getAccounts func() []config.AccountConfig
	getUsage    func() usage.Summary
	getSecret   func(accountID string) string
	cache       map[string]AccountQuota
	headerCache map[string]AccountQuota
	updatedAt   time.Time
	stopCh      chan struct{}
	stopOnce    sync.Once
	running     bool
}

func New(dataDir string, getAccounts func() []config.AccountConfig, getUsage func() usage.Summary, getSecret func(accountID string) string) *Fetcher {
	return &Fetcher{
		dataDir:     dataDir,
		getAccounts: getAccounts,
		getUsage:    getUsage,
		getSecret:   getSecret,
		cache:       make(map[string]AccountQuota),
		headerCache: make(map[string]AccountQuota),
		stopCh:      make(chan struct{}),
	}
}

// RecordHTTPHeaders updates live rate-limit windows captured from upstream HTTP API responses.
func (f *Fetcher) RecordHTTPHeaders(acct config.AccountConfig, h http.Header) {
	prim, sec, ok := ParseHTTPHeaders(h, time.Now())
	if !ok {
		return
	}
	f.mu.Lock()
	f.headerCache[acct.ID] = AccountQuota{
		AccountID:   acct.ID,
		AccountName: acct.Backend + "/" + acct.Name,
		Backend:     acct.Backend,
		Kind:        acct.Kind,
		Source:      "headers-live",
		PlanType:    "api",
		Primary:     prim,
		Secondary:   sec,
		CheckedAt:   time.Now(),
	}
	f.mu.Unlock()
}

func (f *Fetcher) Start() {
	f.mu.Lock()
	if f.running {
		f.mu.Unlock()
		return
	}
	f.running = true
	f.mu.Unlock()

	go func() {
		select {
		case <-time.After(12 * time.Second):
		case <-f.stopCh:
			return
		}
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			f.RefreshAll(ctx)
			cancel()

			select {
			case <-time.After(5 * time.Minute):
			case <-f.stopCh:
				return
			}
		}
	}()
}

func (f *Fetcher) Stop() {
	f.stopOnce.Do(func() {
		close(f.stopCh)
	})
}

// RefreshAll queries live subscription limits across enabled accounts concurrently (bounded to 4 workers).
func (f *Fetcher) RefreshAll(ctx context.Context) Report {
	accts := f.getAccounts()
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for _, acct := range accts {
		if !acct.Enabled {
			continue
		}
		wg.Add(1)
		go func(a config.AccountConfig) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			f.RefreshOne(ctx, a)
		}(acct)
	}
	wg.Wait()

	f.mu.Lock()
	f.updatedAt = time.Now()
	f.mu.Unlock()
	return f.Report()
}

// RefreshOne queries live subscription quota for a single account if its CLI supports it.
func (f *Fetcher) RefreshOne(ctx context.Context, acct config.AccountConfig) AccountQuota {
	q := AccountQuota{
		AccountID:   acct.ID,
		AccountName: acct.Backend + "/" + acct.Name,
		Backend:     acct.Backend,
		Kind:        acct.Kind,
		CheckedAt:   time.Now(),
	}

	secretKey := ""
	if f.getSecret != nil {
		secretKey = f.getSecret(acct.ID)
	}

	switch acct.Backend {
	case "codex":
		if acct.Kind == config.KindACP || acct.Kind == config.KindCLI {
			live, err := fetchCodexQuota(ctx, acct, f.dataDir)
			if err != nil {
				q.Source = "unavailable"
				q.Error = err.Error()
			} else {
				q = live
				q.AccountID = acct.ID
				q.AccountName = acct.Backend + "/" + acct.Name
				q.Backend = acct.Backend
				q.Kind = acct.Kind
				q.CheckedAt = time.Now()
			}
		}
	case "grok":
		if acct.Kind == config.KindACP || acct.Kind == config.KindCLI {
			live, err := fetchGrokQuota(ctx, acct, f.dataDir)
			if err != nil {
				q.Source = "local-tracked"
				q.Error = err.Error()
			} else {
				q = live
				q.AccountID = acct.ID
				q.AccountName = acct.Backend + "/" + acct.Name
				q.Backend = acct.Backend
				q.Kind = acct.Kind
				q.CheckedAt = time.Now()
			}
		}
	case "claude":
		if acct.Kind == config.KindACP || acct.Kind == config.KindCLI {
			cl := scanClaudeLocalUsage(acct, f.dataDir, time.Now())
			if cl.ScannedOK {
				q.Source = "claude-logs"
				q.PlanType = "claude"
				q.Local5h = cl.Msgs5h
				q.Local7d = cl.Msgs7d
				q.Tokens5h = cl.Tokens5h
				q.Tokens7d = cl.Tokens7d
				q.Credits = fmt.Sprintf("5h: %s tok · 7d: %s tok", FormatCompactTokens(cl.Tokens5h), FormatCompactTokens(cl.Tokens7d))
			} else {
				q.Source = "local-tracked"
			}
		}
	case "deepseek":
		if acct.Kind == config.KindAPIKey && secretKey != "" {
			live, err := fetchDeepSeekBalance(ctx, acct, secretKey)
			if err != nil {
				q.Source = "local-tracked"
				q.Error = err.Error()
			} else {
				q = live
				q.AccountID = acct.ID
				q.AccountName = acct.Backend + "/" + acct.Name
				q.Backend = acct.Backend
				q.Kind = acct.Kind
				q.CheckedAt = time.Now()
			}
		} else {
			q.Source = "local-tracked"
		}
	case "openrouter":
		if acct.Kind == config.KindAPIKey && secretKey != "" {
			live, err := fetchOpenRouterQuota(ctx, acct, secretKey)
			if err != nil {
				q.Source = "local-tracked"
				q.Error = err.Error()
			} else {
				q = live
				q.AccountID = acct.ID
				q.AccountName = acct.Backend + "/" + acct.Name
				q.Backend = acct.Backend
				q.Kind = acct.Kind
				q.CheckedAt = time.Now()
			}
		} else {
			q.Source = "local-tracked"
		}
	default:
		q.Source = "local-tracked"
	}

	f.mu.Lock()
	f.cache[acct.ID] = q
	f.mu.Unlock()
	return q
}

// Report returns the merged subscription limits + local rolling usage for all configured accounts.
func (f *Fetcher) Report() Report {
	accts := f.getAccounts()
	var sum usage.Summary
	if f.getUsage != nil {
		sum = f.getUsage()
	}
	usageByKey := make(map[string]usage.AccountUsage, len(sum.ByAccount))
	for _, u := range sum.ByAccount {
		usageByKey[u.Account] = u
	}

	f.mu.RLock()
	updatedAt := f.updatedAt
	out := make([]AccountQuota, 0, len(accts))
	for _, a := range accts {
		key := a.Backend + "/" + a.Name
		q, ok := f.cache[a.ID]
		if !ok {
			q = AccountQuota{
				AccountID:   a.ID,
				AccountName: key,
				Backend:     a.Backend,
				Kind:        a.Kind,
				Source:      "local-tracked",
			}
		} else {
			q.AccountName = key
		}
		if u, okU := usageByKey[key]; okU {
			q.Local1h = max(q.Local1h, u.Reqs1h)
			q.Local5h = max(q.Local5h, u.Reqs5h)
			q.Local24h = max(q.Local24h, u.Reqs24h)
			q.Local7d = max(q.Local7d, u.Reqs7d)
			q.Tokens5h = max(q.Tokens5h, u.Tokens5h)
			q.Tokens7d = max(q.Tokens7d, u.Tokens7d)
		}
		// Merge live HTTP rate-limit headers (Anthropic 5h/7d, OpenAI/Groq/Mistral x-ratelimit-*) if captured.
		if hq, okH := f.headerCache[a.ID]; okH {
			if q.Primary == nil && hq.Primary != nil {
				q.Primary = hq.Primary
				if q.Source == "" || q.Source == "local-tracked" {
					q.Source = hq.Source
				}
			}
			if q.Secondary == nil && hq.Secondary != nil {
				q.Secondary = hq.Secondary
			}
		}
		q.HourlyLimit = a.HourlyLimit
		q.DailyLimit = a.DailyLimit
		q.WeeklyLimit = a.WeeklyLimit

		// Populate local budget windows if the provider did not return live quota percentages.
		if q.Primary == nil && a.HourlyLimit > 0 {
			q.Primary = makeLocalWindow("Hourly", q.Local1h, a.HourlyLimit, 60)
			if q.Source == "" || q.Source == "local-tracked" {
				q.Source = "local-budget"
			}
		} else if q.Primary == nil && a.DailyLimit > 0 {
			q.Primary = makeLocalWindow("Daily", q.Local24h, a.DailyLimit, 1440)
			if q.Source == "" || q.Source == "local-tracked" {
				q.Source = "local-budget"
			}
		}
		if q.Secondary == nil && a.WeeklyLimit > 0 {
			q.Secondary = makeLocalWindow("Weekly", q.Local7d, a.WeeklyLimit, 10080)
			if q.Source == "" || q.Source == "local-tracked" {
				q.Source = "local-budget"
			}
		}
		out = append(out, q)
	}
	f.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		return out[i].AccountName < out[j].AccountName
	})

	return Report{
		UpdatedAt: updatedAt,
		Accounts:  out,
	}
}

func makeLocalWindow(label string, used, limit, mins int) *Window {
	pct := 0.0
	if limit > 0 {
		pct = math.Min(100, float64(used)*100.0/float64(limit))
	}
	return &Window{
		Label:       label,
		UsedPercent: math.Round(pct*10) / 10,
		LeftPercent: math.Round((100.0-pct)*10) / 10,
		WindowMins:  mins,
		UsedCount:   used,
		LimitCount:  limit,
	}
}

// --- Official Codex App-Server `account/rateLimits/read` ---

type codexRateLimitWindow struct {
	UsedPercent        float64 `json:"usedPercent"`
	WindowDurationMins int     `json:"windowDurationMins"`
	ResetsAt           int64   `json:"resetsAt"`
}

type codexRateLimitsPayload struct {
	RateLimits *struct {
		PlanType  string                `json:"planType"`
		Primary   *codexRateLimitWindow `json:"primary"`
		Secondary *codexRateLimitWindow `json:"secondary"`
		Credits   *struct {
			HasCredits bool    `json:"hasCredits"`
			Unlimited  bool    `json:"unlimited"`
			Balance    *string `json:"balance"`
		} `json:"credits"`
	} `json:"rateLimits"`
	RateLimitResetCredits *struct {
		AvailableCount int `json:"availableCount"`
	} `json:"rateLimitResetCredits"`
}

// ParseCodexRateLimits decodes the JSON-RPC result object from `codex app-server` `account/rateLimits/read`.
func ParseCodexRateLimits(raw []byte) (AccountQuota, error) {
	var p codexRateLimitsPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return AccountQuota{}, err
	}
	if p.RateLimits == nil {
		return AccountQuota{}, fmt.Errorf("codex returned empty rateLimits")
	}
	q := AccountQuota{
		Source:   "codex-live",
		PlanType: p.RateLimits.PlanType,
	}
	if p.RateLimits.Primary != nil {
		q.Primary = convertCodexWindow(p.RateLimits.Primary)
	}
	if p.RateLimits.Secondary != nil {
		q.Secondary = convertCodexWindow(p.RateLimits.Secondary)
	}
	var credParts []string
	if p.RateLimits.Credits != nil {
		if p.RateLimits.Credits.Unlimited {
			credParts = append(credParts, "unlimited")
		} else if p.RateLimits.Credits.Balance != nil && *p.RateLimits.Credits.Balance != "" {
			credParts = append(credParts, *p.RateLimits.Credits.Balance)
		}
	}
	if p.RateLimitResetCredits != nil && p.RateLimitResetCredits.AvailableCount > 0 {
		credParts = append(credParts, fmt.Sprintf("%d reset credit", p.RateLimitResetCredits.AvailableCount))
	}
	q.Credits = strings.Join(credParts, " · ")
	return q, nil
}

func convertCodexWindow(w *codexRateLimitWindow) *Window {
	used := math.Max(0, math.Min(100, w.UsedPercent))
	win := &Window{
		Label:       FormatWindowLabel(w.WindowDurationMins),
		UsedPercent: math.Round(used*10) / 10,
		LeftPercent: math.Round((100.0-used)*10) / 10,
		WindowMins:  w.WindowDurationMins,
	}
	if w.ResetsAt > 0 {
		t := time.Unix(w.ResetsAt, 0)
		win.ResetsAt = &t
	}
	return win
}

func fetchCodexQuota(ctx context.Context, acct config.AccountConfig, dataDir string) (AccountQuota, error) {
	bin, err := exec.LookPath("codex")
	if err != nil {
		return AccountQuota{}, fmt.Errorf("codex binary not on PATH")
	}
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(qctx, bin, "app-server")
	env := os.Environ()
	if !acct.UseHostProfile {
		pdir, err := config.ProfileDir(dataDir, acct.Backend, acct.Name)
		if err != nil {
			return AccountQuota{}, err
		}
		env = setEnvVar(env, "CODEX_HOME", pdir)
	}
	for k, v := range acct.CustomEnv {
		env = setEnvVar(env, k, v)
	}
	cmd.Env = env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return AccountQuota{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return AccountQuota{}, err
	}
	if err := cmd.Start(); err != nil {
		return AccountQuota{}, err
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	reqPayload := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"apx","version":"0.1.0"}}}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"account/rateLimits/read","params":{}}` + "\n"
	if _, err := stdin.Write([]byte(reqPayload)); err != nil {
		return AccountQuota{}, err
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 16*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var envMsg struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &envMsg); err != nil {
			continue
		}
		if strings.TrimSpace(string(envMsg.ID)) != "2" {
			continue
		}
		if envMsg.Error != nil {
			return AccountQuota{}, fmt.Errorf("%s", envMsg.Error.Message)
		}
		return ParseCodexRateLimits(envMsg.Result)
	}
	if err := sc.Err(); err != nil {
		return AccountQuota{}, err
	}
	return AccountQuota{}, fmt.Errorf("codex app-server closed before returning rateLimits")
}

// --- Official Grok CLI `grok usage --json` ---

func fetchGrokQuota(ctx context.Context, acct config.AccountConfig, dataDir string) (AccountQuota, error) {
	bin, err := exec.LookPath("grok")
	if err != nil {
		return AccountQuota{}, fmt.Errorf("grok binary not on PATH")
	}
	qctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	cmd := exec.CommandContext(qctx, bin, "usage", "--json")
	env := os.Environ()
	if !acct.UseHostProfile {
		pdir, err := config.ProfileDir(dataDir, acct.Backend, acct.Name)
		if err != nil {
			return AccountQuota{}, err
		}
		env = setEnvVar(env, "HOME", pdir)
		env = setEnvVar(env, "XDG_CONFIG_HOME", pdir+"/.config")
	}
	for k, v := range acct.CustomEnv {
		env = setEnvVar(env, k, v)
	}
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return AccountQuota{}, fmt.Errorf("%s", msg)
	}
	return ParseGrokUsageJSON(out)
}

// ParseGrokUsageJSON extracts primary/secondary usage windows from `grok usage --json` output.
func ParseGrokUsageJSON(raw []byte) (AccountQuota, error) {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return AccountQuota{}, err
	}
	q := AccountQuota{Source: "grok-live"}
	if pt, ok := obj["plan"].(string); ok {
		q.PlanType = pt
	} else if pt, ok := obj["planType"].(string); ok {
		q.PlanType = pt
	}
	if w := extractGenericWindow(obj, "hourly", "5h", "primary"); w != nil {
		q.Primary = w
	}
	if w := extractGenericWindow(obj, "weekly", "7d", "secondary"); w != nil {
		q.Secondary = w
	}
	if q.Primary == nil && q.Secondary == nil {
		if pct, ok := asFloat(obj["used_percent"]); ok {
			q.Primary = &Window{Label: "Plan", UsedPercent: pct, LeftPercent: math.Max(0, 100-pct)}
		}
	}
	return q, nil
}

func extractGenericWindow(root map[string]any, keys ...string) *Window {
	for _, k := range keys {
		sub, ok := root[k].(map[string]any)
		if !ok {
			continue
		}
		used, okU := asFloat(sub["used_percent"])
		if !okU {
			used, okU = asFloat(sub["usedPercent"])
		}
		if !okU {
			if rem, okR := asFloat(sub["remaining_percent"]); okR {
				used = 100 - rem
				okU = true
			}
		}
		if !okU {
			continue
		}
		used = math.Max(0, math.Min(100, used))
		lbl := strings.Title(k)
		if mins, okM := asFloat(sub["windowDurationMins"]); okM && mins > 0 {
			lbl = FormatWindowLabel(int(mins))
		}
		w := &Window{
			Label:       lbl,
			UsedPercent: math.Round(used*10) / 10,
			LeftPercent: math.Round((100-used)*10) / 10,
		}
		if ts, okT := asFloat(sub["resetsAt"]); okT && ts > 0 {
			t := time.Unix(int64(ts), 0)
			w.ResetsAt = &t
		} else if s, okS := sub["resets_at"].(string); okS {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				w.ResetsAt = &t
			}
		}
		return w
	}
	return nil
}

func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	default:
		return 0, false
	}
}

// FormatWindowLabel converts a window duration in minutes into a human label.
func FormatWindowLabel(mins int) string {
	switch {
	case mins <= 0:
		return "Limit"
	case mins <= 60:
		return "1h"
	case mins < 1440:
		if mins%60 == 0 {
			return fmt.Sprintf("%dh", mins/60)
		}
		return fmt.Sprintf("%dm", mins)
	case mins <= 1500:
		return "Daily"
	case mins <= 10500:
		return "Weekly"
	default:
		days := int(math.Round(float64(mins) / 1440.0))
		if days >= 28 && days <= 31 {
			return "Monthly"
		}
		return fmt.Sprintf("%dd", days)
	}
}

// FormatResetIn formats the time remaining until a quota window resets.
func FormatResetIn(resetsAt *time.Time, now time.Time) string {
	if resetsAt == nil || resetsAt.IsZero() {
		return ""
	}
	d := resetsAt.Sub(now)
	if d <= 0 {
		return "now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", max(int(d.Minutes()), 1))
	}
	if d < 48*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m > 0 {
			return fmt.Sprintf("%dh %dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
	days := int(d.Hours()) / 24
	hrs := int(d.Hours()) % 24
	if hrs > 0 && days < 7 {
		return fmt.Sprintf("%dd %dh", days, hrs)
	}
	return fmt.Sprintf("%dd", days)
}

func setEnvVar(env []string, key, val string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			out = append(out, prefix+val)
			replaced = true
		} else {
			out = append(out, kv)
		}
	}
	if !replaced {
		out = append(out, prefix+val)
	}
	return out
}
