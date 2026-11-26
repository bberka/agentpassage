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
	"slices"
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

// --- Claude Code Live ACP `/usage` + Local JSONL Usage Scanner ---

type claudeLogSummary struct {
	Msgs5h    int
	Tokens5h  int
	Msgs7d    int
	Tokens7d  int
	ScannedOK bool
}

func fetchClaudeQuota(ctx context.Context, acct config.AccountConfig, dataDir string) (AccountQuota, error) {
	bin, err := exec.LookPath("claude-agent-acp")
	if err != nil {
		return AccountQuota{}, fmt.Errorf("claude-agent-acp binary not on PATH")
	}
	qctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(qctx, bin)
	env := os.Environ()
	if !acct.UseHostProfile {
		pdir, err := config.ProfileDir(dataDir, acct.Backend, acct.Name)
		if err != nil {
			return AccountQuota{}, err
		}
		env = setEnvVar(env, "CLAUDE_CONFIG_DIR", pdir)
		env = setEnvVar(env, "HOME", pdir)
		env = setEnvVar(env, "XDG_CONFIG_HOME", filepath.Join(pdir, ".config"))
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

	writeMsg := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = stdin.Write(append(b, '\n'))
		return err
	}

	if err := writeMsg(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion":    1,
			"clientCapabilities": map[string]any{},
			"clientInfo":         map[string]any{"name": "apx", "version": "0.1.0"},
		},
	}); err != nil {
		return AccountQuota{}, err
	}

	var planHint string
	var textBuf strings.Builder

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 16*1024), 2*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var envMsg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(line), &envMsg) != nil {
			continue
		}

		// Capture plan name from official auth/status_update notifications.
		if envMsg.Method == "auth/status_update" || envMsg.Method == "_auth/status_update" {
			var st struct {
				AuthStatus *struct {
					Label   string `json:"label"`
					Account *struct {
						Plan string `json:"plan"`
					} `json:"account"`
				} `json:"authStatus"`
				Accounts []struct {
					Label  string `json:"label"`
					Active bool   `json:"active"`
				} `json:"accounts"`
			}
			if json.Unmarshal(envMsg.Params, &st) == nil {
				if st.AuthStatus != nil {
					if st.AuthStatus.Account != nil && st.AuthStatus.Account.Plan != "" {
						planHint = st.AuthStatus.Account.Plan
					} else if st.AuthStatus.Label != "" {
						planHint = st.AuthStatus.Label
					}
				}
				for _, a := range st.Accounts {
					if a.Active && a.Label != "" {
						planHint = a.Label
					}
				}
			}
			continue
		}

		// Collect streamed `/usage` markdown text chunks.
		if envMsg.Method == "session/update" {
			var su struct {
				Update struct {
					SessionUpdate string `json:"sessionUpdate"`
					Content       struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"update"`
			}
			if json.Unmarshal(envMsg.Params, &su) == nil && su.Update.SessionUpdate == "agent_message_chunk" {
				textBuf.WriteString(su.Update.Content.Text)
			}
			continue
		}

		idStr := strings.TrimSpace(string(envMsg.ID))
		switch idStr {
		case "1":
			if envMsg.Error != nil {
				return AccountQuota{}, fmt.Errorf("claude ACP init: %s", envMsg.Error.Message)
			}
			if err := writeMsg(map[string]any{
				"jsonrpc": "2.0",
				"id":      2,
				"method":  "session/new",
				"params":  map[string]any{"cwd": os.TempDir(), "mcpServers": []any{}},
			}); err != nil {
				return AccountQuota{}, err
			}
		case "2":
			if envMsg.Error != nil {
				return AccountQuota{}, fmt.Errorf("claude ACP session: %s", envMsg.Error.Message)
			}
			var res struct {
				SessionID string `json:"sessionId"`
			}
			if err := json.Unmarshal(envMsg.Result, &res); err != nil || res.SessionID == "" {
				return AccountQuota{}, fmt.Errorf("claude ACP returned empty sessionId")
			}
			if err := writeMsg(map[string]any{
				"jsonrpc": "2.0",
				"id":      3,
				"method":  "session/prompt",
				"params": map[string]any{
					"sessionId": res.SessionID,
					"prompt":    []map[string]string{{"type": "text", "text": "/usage"}},
				},
			}); err != nil {
				return AccountQuota{}, err
			}
		case "3":
			if envMsg.Error != nil {
				return AccountQuota{}, fmt.Errorf("claude /usage: %s", envMsg.Error.Message)
			}
			return ParseClaudeUsageMarkdown(textBuf.String(), planHint, time.Now())
		}
	}
	if err := sc.Err(); err != nil {
		return AccountQuota{}, err
	}
	if textBuf.Len() > 0 {
		return ParseClaudeUsageMarkdown(textBuf.String(), planHint, time.Now())
	}
	return AccountQuota{}, fmt.Errorf("claude-agent-acp closed before returning /usage")
}

var (
	claudeLimitLineRe = regexp.MustCompile(`\*\*([^*]+)\*\*\s*[—\-–:]+\s*\*\*(\d+(?:\.\d+)?)%\*\*(?:\s*[·•\-]\s*Resets?\s+(.+))?`)
	claudePlanLineRe  = regexp.MustCompile(`(?i)Claude\s+(ultra|max|pro|team|enterprise)`)
	claudeGMTOffsetRe = regexp.MustCompile(`GMT([+-])(\d{1,2})(?::(\d{2}))?$`)
)

// ParseClaudeUsageMarkdown parses the official Markdown output from Claude Code's `/usage` command.
func ParseClaudeUsageMarkdown(text, planHint string, now time.Time) (AccountQuota, error) {
	q := AccountQuota{
		Source:   "claude-live",
		PlanType: normalizeClaudePlan(planHint),
	}
	if q.PlanType == "" {
		if m := claudePlanLineRe.FindStringSubmatch(text); len(m) == 2 {
			q.PlanType = strings.ToLower(m[1])
		}
	}

	type parsedLimit struct {
		rawLabel string
		shortLbl string
		usedPct  float64
		resetsAt *time.Time
		is5h     bool
		isAllW   bool
	}
	var limits []parsedLimit

	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		m := claudeLimitLineRe.FindStringSubmatch(line)
		if len(m) < 3 {
			continue
		}
		rawLbl := strings.TrimSpace(m[1])
		pct, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		var resetsAt *time.Time
		if len(m) >= 4 && strings.TrimSpace(m[3]) != "" {
			resetsAt = parseClaudeResetTime(strings.TrimSpace(m[3]), now)
		}

		lower := strings.ToLower(rawLbl)
		is5h := strings.Contains(lower, "5h") || strings.Contains(lower, "5-hour") || strings.Contains(lower, "session") || strings.Contains(lower, "current")
		isAllW := strings.Contains(lower, "all models") || lower == "weekly" || lower == "7-day" || lower == "7d"

		shortLbl := shortClaudeLimitLabel(rawLbl, is5h, isAllW)
		limits = append(limits, parsedLimit{
			rawLabel: rawLbl,
			shortLbl: shortLbl,
			usedPct:  clampPct(pct),
			resetsAt: resetsAt,
			is5h:     is5h,
			isAllW:   isAllW,
		})
	}

	if len(limits) == 0 {
		return AccountQuota{}, fmt.Errorf("no limit entries found in claude /usage output")
	}

	makeWin := func(l parsedLimit) *Window {
		return &Window{
			Label:       l.shortLbl,
			UsedPercent: l.usedPct,
			LeftPercent: math.Round((100.0-l.usedPct)*10) / 10,
			ResetsAt:    l.resetsAt,
		}
	}

	usedIdx := make(map[int]bool)
	// Prefer 5h/session as Primary, Weekly (all models) as Secondary; if there is no 5h window
	// (e.g. Claude Max/Ultra weekly-only plans), promote Weekly all models to Primary and model-specific to Secondary.
	for i, l := range limits {
		if l.is5h && q.Primary == nil {
			q.Primary = makeWin(l)
			usedIdx[i] = true
		}
	}
	for i, l := range limits {
		if usedIdx[i] {
			continue
		}
		if l.isAllW {
			if q.Primary == nil {
				q.Primary = makeWin(l)
			} else if q.Secondary == nil {
				q.Secondary = makeWin(l)
			}
			usedIdx[i] = true
		}
	}
	for i, l := range limits {
		if usedIdx[i] {
			continue
		}
		if q.Primary == nil {
			q.Primary = makeWin(l)
			usedIdx[i] = true
		} else if q.Secondary == nil {
			q.Secondary = makeWin(l)
			usedIdx[i] = true
		}
	}

	if q.PlanType == "" {
		q.PlanType = "claude"
	}
	return q, nil
}

func normalizeClaudePlan(raw string) string {
	l := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case strings.Contains(l, "ultra"):
		return "ultra"
	case strings.Contains(l, "max_20x"):
		return "max 20x"
	case strings.Contains(l, "max_5x"):
		return "max 5x"
	case strings.Contains(l, "max"):
		return "max"
	case strings.Contains(l, "pro"):
		return "pro"
	case strings.Contains(l, "team"):
		return "team"
	case strings.Contains(l, "enterprise"):
		return "ent"
	default:
		return ""
	}
}

func shortClaudeLimitLabel(raw string, is5h, isAllW bool) string {
	if is5h {
		return "5h"
	}
	if isAllW {
		return "Weekly"
	}
	// E.g. "Weekly · Fable" -> "Fable", "Weekly · Sonnet" -> "Sonnet"
	for _, sep := range []string{"·", "•", "-", ":"} {
		if parts := strings.SplitN(raw, sep, 2); len(parts) == 2 {
			sub := strings.TrimSpace(parts[1])
			if sub != "" {
				if len(sub) > 12 {
					return sub[:12]
				}
				return sub
			}
		}
	}
	if len(raw) > 12 {
		return raw[:12]
	}
	return raw
}

func parseClaudeResetTime(raw string, now time.Time) *time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil
	}
	if secs := backend.ParseResetDurationSeconds(s); secs > 0 {
		t := now.Add(time.Duration(secs) * time.Second)
		return &t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t
	}

	// Parse human strings like "Nov 27, 4:00 PM GMT+3", "Nov 27, 4pm UTC", "4:00 PM GMT+3"
	loc := now.Location()
	if m := claudeGMTOffsetRe.FindStringSubmatch(s); len(m) >= 3 {
		hrs, _ := strconv.Atoi(m[2])
		mins := 0
		if len(m) >= 4 && m[3] != "" {
			mins, _ = strconv.Atoi(m[3])
		}
		off := hrs*3600 + mins*60
		if m[1] == "-" {
			off = -off
		}
		loc = time.FixedZone("GMT"+m[1]+m[2], off)
		s = strings.TrimSpace(s[:len(s)-len(m[0])])
	} else if strings.HasSuffix(s, " UTC") {
		loc = time.UTC
		s = strings.TrimSpace(strings.TrimSuffix(s, " UTC"))
	}

	layoutsWithDate := []string{
		"Jan 2, 3:04 PM",
		"Jan 2, 3:04PM",
		"Jan 2, 3 PM",
		"Jan 2, 3PM",
		"Jan 2 3:04 PM",
		"January 2, 3:04 PM",
	}
	for _, layout := range layoutsWithDate {
		if parsed, err := time.ParseInLocation(layout, s, loc); err == nil {
			cand := time.Date(now.Year(), parsed.Month(), parsed.Day(), parsed.Hour(), parsed.Minute(), 0, 0, loc)
			if cand.Before(now.Add(-24 * time.Hour)) {
				cand = cand.AddDate(1, 0, 0)
			}
			return &cand
		}
	}
	layoutsTimeOnly := []string{"3:04 PM", "3:04PM", "3 PM", "3PM"}
	for _, layout := range layoutsTimeOnly {
		if parsed, err := time.ParseInLocation(layout, s, loc); err == nil {
			nInLoc := now.In(loc)
			cand := time.Date(nInLoc.Year(), nInLoc.Month(), nInLoc.Day(), parsed.Hour(), parsed.Minute(), 0, 0, loc)
			if cand.Before(now) {
				cand = cand.Add(24 * time.Hour)
			}
			return &cand
		}
	}
	return nil
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
		env = setEnvVar(env, "AGY_ACP_FORCE_FILE_STORAGE", "1")
		env = setEnvVar(env, "DBUS_SESSION_BUS_ADDRESS", "disabled:")
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

type agyUsageGroup struct {
	ModelGroup  string `json:"model_group"`
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Buckets     []struct {
		BucketID          string   `json:"bucket_id"`
		DisplayName       string   `json:"display_name"`
		Window            string   `json:"window"`
		Label             string   `json:"label"`
		UsageFraction     *float64 `json:"usage_fraction"`
		RemainingFraction *float64 `json:"remaining_fraction"`
		UsedPercent       *float64 `json:"used_percent"`
		RemainingPercent  *float64 `json:"remaining_percent"`
		ResetTime         string   `json:"reset_time"`
		ResetInSeconds    int      `json:"reset_in_seconds"`
	} `json:"buckets"`
}

type agyUsageEnvelope struct {
	Status       string `json:"status"`
	Response     string `json:"response"`
	Error        string `json:"error"`
	PlanTier     string `json:"plan_tier"`
	Plan         string `json:"plan"`
	QuotaSummary *struct {
		PlanTier string          `json:"plan_tier"`
		Groups   []agyUsageGroup `json:"groups"`
	} `json:"quota_summary"`
	Command *struct {
		Name string `json:"name"`
		Data *struct {
			Groups []agyUsageGroup `json:"groups"`
		} `json:"data"`
	} `json:"command"`
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
	if err := json.Unmarshal(jsonSlice, &env); err == nil && (env.Status != "" || env.Response != "" || env.Error != "" || env.QuotaSummary != nil || env.Command != nil || len(env.Quota) > 0) {
		q := AccountQuota{
			Source:   "agy-live",
			PlanType: firstNonEmpty(env.PlanTier, env.Plan),
		}
		if env.QuotaSummary != nil && q.PlanType == "" {
			q.PlanType = env.QuotaSummary.PlanTier
		}

		// 1. Structured groups[] from command.data and quota_summary
		var groups []agyUsageGroup
		if env.Command != nil && env.Command.Data != nil {
			groups = append(groups, env.Command.Data.Groups...)
		}
		if env.QuotaSummary != nil {
			groups = append(groups, env.QuotaSummary.Groups...)
		}
		applyAgyGroups(&q, groups, now)

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

// applyAgyGroups fills q.Groups per model family, keeps the account-level
// windows at the most constrained bucket, and summarizes remaining % in Credits.
func applyAgyGroups(q *AccountQuota, groups []agyUsageGroup, now time.Time) {
	var names []string
	minLeft := make(map[string]float64)
	for _, g := range groups {
		gName := firstNonEmpty(g.ModelGroup, g.Name, g.DisplayName)
		id, title := agyGroupIdentity(gName)
		for _, b := range g.Buckets {
			left, ok := agyBucketLeftPercent(b.RemainingFraction, b.UsageFraction, b.RemainingPercent, b.UsedPercent)
			if !ok {
				continue
			}
			lbl := normalizeAgyWindowLabel(firstNonEmpty(b.Window, b.Label, b.DisplayName, b.BucketID))
			w := makeAgyWindow(lbl, left, b.ResetTime, b.ResetInSeconds, now)
			assignAgyWindow(q, w, lbl)

			if id != "" {
				i := slices.IndexFunc(q.Groups, func(qg QuotaGroup) bool { return qg.ID == id })
				if i < 0 {
					q.Groups = append(q.Groups, QuotaGroup{ID: id, Name: title})
					i = len(q.Groups) - 1
				}
				scoped := AccountQuota{Primary: q.Groups[i].Primary, Secondary: q.Groups[i].Secondary}
				assignAgyWindow(&scoped, w, lbl)
				q.Groups[i].Primary, q.Groups[i].Secondary = scoped.Primary, scoped.Secondary
			}

			if gName == "" {
				continue
			}
			short := shortAgyGroup(gName)
			if cur, seen := minLeft[short]; !seen {
				names = append(names, short)
				minLeft[short] = left
			} else if left < cur {
				minLeft[short] = left
			}
		}
	}
	notes := make([]string, 0, len(names))
	for _, n := range names {
		notes = append(notes, fmt.Sprintf("%s %d%%", n, int(math.Round(minLeft[n]))))
	}
	q.Credits = strings.Join(notes, " · ")
}

// agyGroupIdentity maps a provider group name to a stable group ID and title.
func agyGroupIdentity(name string) (id, title string) {
	l := strings.ToLower(name)
	switch {
	case strings.Contains(l, "gemini"):
		return GroupGemini, "Gemini"
	case strings.Contains(l, "claude") || strings.Contains(l, "gpt"):
		return GroupClaudeGPT, "Claude & GPT"
	default:
		return "", ""
	}
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
