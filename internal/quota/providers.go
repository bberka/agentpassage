package quota

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
)

// --- DeepSeek Official Balance API: GET /user/balance ---

type deepSeekBalanceResp struct {
	IsAvailable  bool `json:"is_available"`
	BalanceInfos []struct {
		Currency        string `json:"currency"`
		TotalBalance    string `json:"total_balance"`
		GrantedBalance  string `json:"granted_balance"`
		ToppedUpBalance string `json:"topped_up_balance"`
	} `json:"balance_infos"`
}

func fetchDeepSeekBalance(ctx context.Context, acct config.AccountConfig, apiKey string) (AccountQuota, error) {
	if strings.TrimSpace(apiKey) == "" {
		return AccountQuota{}, fmt.Errorf("missing API key")
	}
	base := strings.TrimRight(strings.TrimSpace(acct.BaseURL), "/")
	if base == "" {
		base = "https://api.deepseek.com"
	}
	// DeepSeek balance lives at /user/balance (strip trailing /v1 if present).
	base = strings.TrimSuffix(base, "/v1")

	qctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(qctx, http.MethodGet, base+"/user/balance", nil)
	if err != nil {
		return AccountQuota{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return AccountQuota{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return AccountQuota{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return ParseDeepSeekBalance(body)
}

// ParseDeepSeekBalance parses GET /user/balance JSON into an AccountQuota.
func ParseDeepSeekBalance(raw []byte) (AccountQuota, error) {
	var p deepSeekBalanceResp
	if err := json.Unmarshal(raw, &p); err != nil {
		return AccountQuota{}, err
	}
	q := AccountQuota{
		Source:   "deepseek-live",
		PlanType: "api",
	}
	var parts []string
	for _, b := range p.BalanceInfos {
		if b.TotalBalance != "" {
			parts = append(parts, fmt.Sprintf("%s %s", b.TotalBalance, b.Currency))
		}
	}
	if len(parts) > 0 {
		q.Credits = "bal: " + strings.Join(parts, " / ")
	} else if !p.IsAvailable {
		q.Credits = "exhausted"
	}
	if !p.IsAvailable {
		q.Primary = &Window{Label: "Balance", UsedPercent: 100, LeftPercent: 0}
	}
	return q, nil
}

// --- OpenRouter Official Key/Credits API: GET /api/v1/auth/key ---

type openRouterKeyResp struct {
	Data struct {
		Usage          float64  `json:"usage"`
		UsageDaily     float64  `json:"usage_daily"`
		UsageWeekly    float64  `json:"usage_weekly"`
		UsageMonthly   float64  `json:"usage_monthly"`
		Limit          *float64 `json:"limit"`
		LimitRemaining *float64 `json:"limit_remaining"`
		LimitReset     string   `json:"limit_reset"`
		IsFreeTier     bool     `json:"is_free_tier"`
		FreeDaily      *struct {
			Limit     int `json:"limit"`
			Remaining int `json:"remaining"`
			Used      int `json:"used"`
		} `json:"free_model_daily_requests"`
	} `json:"data"`
}

func fetchOpenRouterQuota(ctx context.Context, acct config.AccountConfig, apiKey string) (AccountQuota, error) {
	if strings.TrimSpace(apiKey) == "" {
		return AccountQuota{}, fmt.Errorf("missing API key")
	}
	base := strings.TrimRight(strings.TrimSpace(acct.BaseURL), "/")
	if base == "" {
		base = "https://openrouter.ai/api/v1"
	}
	qctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(qctx, http.MethodGet, base+"/auth/key", nil)
	if err != nil {
		return AccountQuota{}, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return AccountQuota{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return AccountQuota{}, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return ParseOpenRouterKey(body)
}

// ParseOpenRouterKey parses GET /api/v1/auth/key JSON into an AccountQuota.
func ParseOpenRouterKey(raw []byte) (AccountQuota, error) {
	var p openRouterKeyResp
	if err := json.Unmarshal(raw, &p); err != nil {
		return AccountQuota{}, err
	}
	d := p.Data
	plan := "paid"
	if d.IsFreeTier {
		plan = "free"
	}
	q := AccountQuota{
		Source:   "openrouter-live",
		PlanType: plan,
	}
	if d.Limit != nil && *d.Limit > 0 {
		rem := 0.0
		if d.LimitRemaining != nil {
			rem = *d.LimitRemaining
		} else {
			rem = math.Max(0, *d.Limit-d.Usage)
		}
		leftPct := math.Max(0, math.Min(100, (rem / *d.Limit)*100.0))
		usedPct := 100.0 - leftPct
		lbl := "Budget"
		if d.LimitReset != "" {
			lbl = strings.ToUpper(d.LimitReset[:1]) + d.LimitReset[1:]
		}
		q.Primary = &Window{
			Label:       lbl,
			UsedPercent: math.Round(usedPct*10) / 10,
			LeftPercent: math.Round(leftPct*10) / 10,
		}
		q.Credits = fmt.Sprintf("$%.2f / $%.2f left (7d: $%.2f)", rem, *d.Limit, d.UsageWeekly)
	} else {
		q.Credits = fmt.Sprintf("24h: $%.2f · 7d: $%.2f · total: $%.2f", d.UsageDaily, d.UsageWeekly, d.Usage)
	}
	if d.FreeDaily != nil && d.FreeDaily.Limit > 0 {
		w := makeLocalWindow("Free/24h", d.FreeDaily.Used, d.FreeDaily.Limit, 1440)
		if q.Primary == nil {
			q.Primary = w
		} else {
			q.Secondary = w
		}
	}
	return q, nil
}

// --- Claude Code Local JSONL Usage Scanner (5h block + 7d weekly, read-only) ---

type claudeLogSummary struct {
	Msgs5h    int
	Tokens5h  int
	Msgs7d    int
	Tokens7d  int
	ScannedOK bool
}

func scanClaudeLocalUsage(acct config.AccountConfig, dataDir string, now time.Time) claudeLogSummary {
	var configDir string
	if acct.UseHostProfile {
		if envDir := strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")); envDir != "" {
			configDir = envDir
		} else if home, err := os.UserHomeDir(); err == nil {
			configDir = filepath.Join(home, ".claude")
		}
	} else {
		if pdir, err := config.ProfileDir(dataDir, acct.Backend, acct.Name); err == nil {
			configDir = pdir
		}
	}
	if configDir == "" {
		return claudeLogSummary{}
	}
	return ScanClaudeProjectsDir(filepath.Join(configDir, "projects"), now)
}

// ScanClaudeProjectsDir reads Claude Code's local JSONL transcripts (last 7 days) to aggregate
// 5-hour rolling block and 7-day weekly token & message usage.
func ScanClaudeProjectsDir(projectsDir string, now time.Time) claudeLogSummary {
	var out claudeLogSummary
	info, err := os.Stat(projectsDir)
	if err != nil || !info.IsDir() {
		return out
	}
	out.ScannedOK = true
	cutoff5h := now.Add(-5 * time.Hour)
	cutoff7d := now.Add(-7 * 24 * time.Hour)

	_ = filepath.WalkDir(projectsDir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		fi, err := d.Info()
		if err != nil || fi.ModTime().Before(cutoff7d) {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()

		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 16*1024), 4*1024*1024)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 || !strings.Contains(string(line), `"usage"`) {
				continue
			}
			var entry struct {
				Timestamp string `json:"timestamp"`
				Message   struct {
					Usage struct {
						InputTokens              int `json:"input_tokens"`
						OutputTokens             int `json:"output_tokens"`
						CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
						CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &entry) != nil {
				continue
			}
			u := entry.Message.Usage
			toks := u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
			if toks == 0 {
				continue
			}
			ts, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
			if err != nil {
				ts, err = time.Parse(time.RFC3339, entry.Timestamp)
				if err != nil {
					continue
				}
			}
			if ts.Before(cutoff7d) {
				continue
			}
			out.Msgs7d++
			out.Tokens7d += toks
			if !ts.Before(cutoff5h) {
				out.Msgs5h++
				out.Tokens5h += toks
			}
		}
		return nil
	})
	return out
}

// --- Live HTTP Rate-Limit Header Capture (Anthropic, OpenAI, Groq, Mistral, xAI, etc.) ---

// ParseHTTPHeaders extracts official rate-limit windows from Anthropic and OpenAI-style HTTP response headers.
func ParseHTTPHeaders(h http.Header, now time.Time) (primary *Window, secondary *Window, found bool) {
	if h == nil {
		return nil, nil, false
	}

	// 1. Anthropic unified 5h & 7d subscription/workspace rate-limit headers (utilization is 0.0..1.0 or 0..100).
	if u5 := strings.TrimSpace(h.Get("anthropic-ratelimit-unified-5h-utilization")); u5 != "" {
		if v, err := strconv.ParseFloat(u5, 64); err == nil {
			if v <= 1.0 {
				v *= 100.0
			}
			v = math.Max(0, math.Min(100, v))
			primary = &Window{
				Label:       "5h",
				UsedPercent: math.Round(v*10) / 10,
				LeftPercent: math.Round((100-v)*10) / 10,
				WindowMins:  300,
				ResetsAt:    parseHeaderTime(h.Get("anthropic-ratelimit-unified-5h-reset"), now),
			}
			found = true
		}
	}
	if u7 := strings.TrimSpace(h.Get("anthropic-ratelimit-unified-7d-utilization")); u7 != "" {
		if v, err := strconv.ParseFloat(u7, 64); err == nil {
			if v <= 1.0 {
				v *= 100.0
			}
			v = math.Max(0, math.Min(100, v))
			secondary = &Window{
				Label:       "Weekly",
				UsedPercent: math.Round(v*10) / 10,
				LeftPercent: math.Round((100-v)*10) / 10,
				WindowMins:  10080,
				ResetsAt:    parseHeaderTime(h.Get("anthropic-ratelimit-unified-7d-reset"), now),
			}
			found = true
		}
	}
	if found {
		return primary, secondary, true
	}

	// 2. Standard OpenAI / Anthropic / Groq / Mistral x-ratelimit-* request & token windows.
	remReq, okRR := parseHeaderInt(h, "x-ratelimit-remaining-requests", "anthropic-ratelimit-requests-remaining")
	limReq, okLR := parseHeaderInt(h, "x-ratelimit-limit-requests", "anthropic-ratelimit-requests-limit")
	if okRR && okLR && limReq > 0 {
		used := max(limReq-remReq, 0)
		w := makeLocalWindow("Requests", used, limReq, 0)
		w.ResetsAt = parseHeaderTime(firstHeader(h, "x-ratelimit-reset-requests", "anthropic-ratelimit-requests-reset"), now)
		primary = w
		found = true
	}

	remTok, okRT := parseHeaderInt(h, "x-ratelimit-remaining-tokens", "anthropic-ratelimit-tokens-remaining")
	limTok, okLT := parseHeaderInt(h, "x-ratelimit-limit-tokens", "anthropic-ratelimit-tokens-limit")
	if okRT && okLT && limTok > 0 {
		used := max(limTok-remTok, 0)
		w := makeLocalWindow("Tokens", used, limTok, 0)
		w.ResetsAt = parseHeaderTime(firstHeader(h, "x-ratelimit-reset-tokens", "anthropic-ratelimit-tokens-reset"), now)
		if primary == nil {
			primary = w
		} else {
			secondary = w
		}
		found = true
	}
	return primary, secondary, found
}

func firstHeader(h http.Header, names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(h.Get(n)); v != "" {
			return v
		}
	}
	return ""
}

func parseHeaderInt(h http.Header, names ...string) (int, bool) {
	raw := firstHeader(h, names...)
	if raw == "" {
		return 0, false
	}
	v, err := strconv.Atoi(raw)
	return v, err == nil
}

func parseHeaderTime(raw string, now time.Time) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if sec, err := strconv.ParseInt(raw, 10, 64); err == nil && sec > 1_000_000_000 {
		t := time.Unix(sec, 0)
		return &t
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return &t
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		t := now.Add(d)
		return &t
	}
	return nil
}

// FormatCompactTokens formats token counts cleanly (e.g. 5800 -> "5.8k", 1250000 -> "1.2M").
func FormatCompactTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000.0)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000.0)
	default:
		return strconv.Itoa(n)
	}
}

// --- Official Antigravity CLI `agy --print /usage --output-format json` ---

func fetchAgyQuota(ctx context.Context, acct config.AccountConfig, dataDir string) (AccountQuota, error) {
	bin, err := exec.LookPath("agy")
	if err != nil {
		return AccountQuota{}, fmt.Errorf("agy binary not on PATH")
	}
	qctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	cmd := exec.CommandContext(qctx, bin, "--print", "/usage", "--output-format", "json")
	cmd.Stdin = bytes.NewReader(nil) // never hang waiting for interactive auth code
	env := os.Environ()
	if !acct.UseHostProfile {
		pdir, err := config.ProfileDir(dataDir, acct.Backend, acct.Name)
		if err != nil {
			return AccountQuota{}, err
		}
		env = setEnvVar(env, "HOME", pdir)
		env = setEnvVar(env, "XDG_CONFIG_HOME", filepath.Join(pdir, ".config"))
	}
	for k, v := range acct.CustomEnv {
		env = setEnvVar(env, k, v)
	}
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	raw := bytes.TrimSpace(stdout.Bytes())
	if len(raw) == 0 {
		raw = bytes.TrimSpace(stderr.Bytes())
	}
	if len(raw) == 0 && runErr != nil {
		return AccountQuota{}, runErr
	}
	return ParseAgyUsageOutput(raw, time.Now())
}

type agyUsageEnvelope struct {
	Status       string `json:"status"`
	Response     string `json:"response"`
	Error        string `json:"error"`
	PlanTier     string `json:"plan_tier"`
	Plan         string `json:"plan"`
	QuotaSummary *struct {
		PlanTier string `json:"plan_tier"`
		Groups   []struct {
			ModelGroup string `json:"model_group"`
			Name       string `json:"name"`
			Buckets    []struct {
				Window            string   `json:"window"`
				Label             string   `json:"label"`
				UsageFraction     *float64 `json:"usage_fraction"`
				RemainingFraction *float64 `json:"remaining_fraction"`
				UsedPercent       *float64 `json:"used_percent"`
				RemainingPercent  *float64 `json:"remaining_percent"`
				ResetTime         string   `json:"reset_time"`
				ResetInSeconds    int      `json:"reset_in_seconds"`
			} `json:"buckets"`
		} `json:"groups"`
	} `json:"quota_summary"`
	Quota map[string]struct {
		RemainingFraction *float64 `json:"remaining_fraction"`
		UsageFraction     *float64 `json:"usage_fraction"`
		UsedPercent       *float64 `json:"used_percent"`
		RemainingPercent  *float64 `json:"remaining_percent"`
		ResetTime         string   `json:"reset_time"`
		ResetInSeconds    int      `json:"reset_in_seconds"`
	} `json:"quota"`
}

var (
	agyPctLeftRe = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*%\s*(?:left|remaining|avail(?:able)?)`)
	agyPctUsedRe = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*%\s*used`)
	agyPctBareRe = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*%`)
)

// ParseAgyUsageOutput parses JSON or text output from `agy --print /usage --output-format json`.
func ParseAgyUsageOutput(raw []byte, now time.Time) (AccountQuota, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return AccountQuota{}, fmt.Errorf("empty agy /usage output")
	}

	// Extract JSON object even if diagnostic text appeared before it.
	jsonSlice := trimmed
	if idx := bytes.IndexByte(trimmed, '{'); idx >= 0 {
		if end := bytes.LastIndexByte(trimmed, '}'); end > idx {
			jsonSlice = trimmed[idx : end+1]
		}
	}

	var env agyUsageEnvelope
	if err := json.Unmarshal(jsonSlice, &env); err == nil && (env.Status != "" || env.Response != "" || env.Error != "" || env.QuotaSummary != nil || len(env.Quota) > 0) {
		q := AccountQuota{
			Source:   "agy-live",
			PlanType: firstNonEmpty(env.PlanTier, env.Plan),
		}
		if env.QuotaSummary != nil && q.PlanType == "" {
			q.PlanType = env.QuotaSummary.PlanTier
		}

		// 1. Structured quota_summary.groups[]
		if env.QuotaSummary != nil {
			var groupNotes []string
			for _, g := range env.QuotaSummary.Groups {
				gName := firstNonEmpty(g.ModelGroup, g.Name)
				var gMinLeft *float64
				for _, b := range g.Buckets {
					left, ok := agyBucketLeftPercent(b.RemainingFraction, b.UsageFraction, b.RemainingPercent, b.UsedPercent)
					if !ok {
						continue
					}
					if gMinLeft == nil || left < *gMinLeft {
						v := left
						gMinLeft = &v
					}
					lbl := normalizeAgyWindowLabel(firstNonEmpty(b.Window, b.Label))
					w := makeAgyWindow(lbl, left, b.ResetTime, b.ResetInSeconds, now)
					assignAgyWindow(&q, w, lbl)
				}
				if gName != "" && gMinLeft != nil {
					groupNotes = append(groupNotes, fmt.Sprintf("%s %d%%", shortAgyGroup(gName), int(math.Round(*gMinLeft))))
				}
			}
			if len(groupNotes) > 1 {
				q.Credits = strings.Join(groupNotes, " · ")
			}
		}

		// 2. Structured quota map (statusline schema: {"gemini-weekly": {...}, "gemini-5h": {...}})
		if len(env.Quota) > 0 {
			for k, b := range env.Quota {
				left, ok := agyBucketLeftPercent(b.RemainingFraction, b.UsageFraction, b.RemainingPercent, b.UsedPercent)
				if !ok {
					continue
				}
				lbl := normalizeAgyWindowLabel(k)
				w := makeAgyWindow(lbl, left, b.ResetTime, b.ResetInSeconds, now)
				assignAgyWindow(&q, w, k)
			}
		}

		// 3. Text report inside `response` field
		if q.Primary == nil && q.Secondary == nil && strings.TrimSpace(env.Response) != "" {
			parseAgyUsageText(&q, env.Response, now)
		}

		if q.Primary != nil || q.Secondary != nil {
			if q.PlanType == "" {
				q.PlanType = "agy"
			}
			return q, nil
		}

		if strings.EqualFold(env.Status, "ERROR") || env.Error != "" {
			return classifyAgyUsageError(firstNonEmpty(env.Error, env.Response)), nil
		}
		return AccountQuota{}, fmt.Errorf("agy /usage returned no quota windows")
	}

	// Plain-text fallback
	q := AccountQuota{Source: "agy-live", PlanType: "agy"}
	parseAgyUsageText(&q, string(trimmed), now)
	if q.Primary != nil || q.Secondary != nil {
		return q, nil
	}
	return classifyAgyUsageError(string(trimmed)), nil
}

func classifyAgyUsageError(raw string) AccountQuota {
	lower := strings.ToLower(raw)
	q := AccountQuota{
		Source:   "unavailable",
		PlanType: "agy",
	}
	switch {
	case strings.Contains(lower, "verify your account") || strings.Contains(lower, "validation_required") || strings.Contains(lower, "not eligible"):
		q.Credits = "verify account"
		q.Error = "Google account verification required"
	case strings.Contains(lower, "authentication") || strings.Contains(lower, "sign in") || strings.Contains(lower, "not logged in"):
		q.Credits = "login needed"
		q.Error = "Authentication required"
	default:
		firstLine := strings.TrimSpace(strings.SplitN(raw, "\n", 2)[0])
		if len(firstLine) > 120 {
			firstLine = firstLine[:120] + "..."
		}
		q.Error = firstLine
	}
	return q
}

func agyBucketLeftPercent(remFrac, usedFrac, remPct, usedPct *float64) (float64, bool) {
	switch {
	case remFrac != nil:
		v := *remFrac
		if v <= 1.0 {
			v *= 100.0
		}
		return clampPct(v), true
	case remPct != nil:
		return clampPct(*remPct), true
	case usedFrac != nil:
		v := *usedFrac
		if v <= 1.0 {
			v *= 100.0
		}
		return clampPct(100.0 - v), true
	case usedPct != nil:
		return clampPct(100.0 - *usedPct), true
	default:
		return 0, false
	}
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return math.Round(v*10) / 10
}

func normalizeAgyWindowLabel(raw string) string {
	l := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.Contains(l, "5h") || strings.Contains(l, "5-hour") || strings.Contains(l, "five") || strings.Contains(l, "hour"):
		return "5h"
	case strings.Contains(l, "7d") || strings.Contains(l, "week"):
		return "Weekly"
	case strings.Contains(l, "24h") || strings.Contains(l, "day") || strings.Contains(l, "daily"):
		return "Daily"
	case strings.Contains(l, "month") || strings.Contains(l, "30d"):
		return "Monthly"
	default:
		return "Quota"
	}
}

func shortAgyGroup(name string) string {
	l := strings.ToLower(name)
	switch {
	case strings.Contains(l, "gemini"):
		return "Gemini"
	case strings.Contains(l, "claude") || strings.Contains(l, "gpt"):
		return "Claude/GPT"
	default:
		if len(name) > 12 {
			return name[:12]
		}
		return name
	}
}

func makeAgyWindow(label string, leftPct float64, resetTime string, resetSecs int, now time.Time) *Window {
	w := &Window{
		Label:       label,
		LeftPercent: leftPct,
		UsedPercent: math.Round((100.0-leftPct)*10) / 10,
	}
	if resetTime != "" {
		if t, err := time.Parse(time.RFC3339, resetTime); err == nil {
			w.ResetsAt = &t
		}
	}
	if w.ResetsAt == nil && resetSecs > 0 {
		t := now.Add(time.Duration(resetSecs) * time.Second)
		w.ResetsAt = &t
	}
	return w
}

func assignAgyWindow(q *AccountQuota, w *Window, hint string) {
	l := strings.ToLower(hint + " " + w.Label)
	isSecondary := strings.Contains(l, "week") || strings.Contains(l, "7d") || strings.Contains(l, "month")
	if isSecondary {
		if q.Secondary == nil || w.LeftPercent < q.Secondary.LeftPercent {
			q.Secondary = w
		}
		return
	}
	if q.Primary == nil || w.LeftPercent < q.Primary.LeftPercent {
		q.Primary = w
	}
}

func parseAgyUsageText(q *AccountQuota, text string, now time.Time) {
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		lower := strings.ToLower(line)
		if q.PlanType == "" || q.PlanType == "agy" {
			switch {
			case strings.Contains(lower, "ultra"):
				q.PlanType = "ultra"
			case strings.Contains(lower, "pro") && (strings.Contains(lower, "plan") || strings.Contains(lower, "tier") || strings.Contains(lower, "ai pro")):
				q.PlanType = "pro"
			case strings.Contains(lower, "free") && (strings.Contains(lower, "plan") || strings.Contains(lower, "tier")):
				q.PlanType = "free"
			}
		}

		var left float64
		foundPct := false
		if m := agyPctLeftRe.FindStringSubmatch(line); len(m) == 2 {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				left = clampPct(v)
				foundPct = true
			}
		} else if m := agyPctUsedRe.FindStringSubmatch(line); len(m) == 2 {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				left = clampPct(100.0 - v)
				foundPct = true
			}
		} else if m := agyPctBareRe.FindStringSubmatch(line); len(m) == 2 {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				left = clampPct(v)
				foundPct = true
			}
		}
		if !foundPct {
			continue
		}

		lbl := normalizeAgyWindowLabel(line)
		resetSecs := backend.ParseResetDurationSeconds(line)
		w := makeAgyWindow(lbl, left, "", resetSecs, now)
		assignAgyWindow(q, w, line)
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
