package quota

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bberka/acpx/internal/config"
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
