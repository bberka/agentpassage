package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/quota"
)

// registerCLIProxyCompatRoutes mounts CLIProxyAPI-compatible Management & Usage endpoints
// (/v0/management/* and /v8/management/*) backed by apx's own LiveQuota() and LiveUsage()
// data so external control surfaces (such as T3 Code's Usage Providers -> Add hub) can
// display all pooled apx accounts in one place without extra upstream calls or token exposure.
func (s *Server) registerCLIProxyCompatRoutes(mux *http.ServeMux) {
	for _, prefix := range []string{"/v0/management", "/v8/management"} {
		mux.HandleFunc("GET "+prefix+"/auth-files", s.withAdminAuth(s.handleCLIProxyAuthFiles))
		mux.HandleFunc("GET "+prefix+"/auth-files/models", s.withAdminAuth(s.handleCLIProxyAuthFileModels))
		mux.HandleFunc("POST "+prefix+"/api-call", s.withAdminAuth(s.handleCLIProxyAPICall))
		mux.HandleFunc("GET "+prefix+"/usage", s.withAdminAuth(s.handleCLIProxyUsage))
		mux.HandleFunc("GET "+prefix+"/usage-queue", s.withAdminAuth(s.handleCLIProxyUsageQueue))
		mux.HandleFunc("GET "+prefix+"/config", s.withAdminAuth(s.handleCLIProxyConfig))
		mux.HandleFunc("GET "+prefix+"/latest-version", s.withAdminAuth(s.handleCLIProxyLatestVersion))
		mux.HandleFunc(prefix+"/", s.withAdminAuth(s.handleCLIProxyUnknown))
	}
	mux.HandleFunc("GET /usage-queue", s.withAdminAuth(s.handleCLIProxyUsageQueue))
}

func cliProxyProviderName(backend string) string {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "agy":
		return "antigravity"
	case "gemini":
		return "gemini-cli"
	default:
		return strings.ToLower(strings.TrimSpace(backend))
	}
}

func (s *Server) handleCLIProxyConfig(w http.ResponseWriter, r *http.Request) {
	cfg := s.Config()
	writeJSON(w, http.StatusOK, map[string]any{
		"port":                     cfg.Server.Port,
		"usage-statistics-enabled": true,
		"source":                   "agentpassage",
		"version":                  Version,
	})
}

func (s *Server) handleCLIProxyLatestVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"latest-version": Version,
	})
}

func (s *Server) handleCLIProxyAuthFileModels(w http.ResponseWriter, r *http.Request) {
	cfg := s.Config()
	target := strings.TrimSpace(r.URL.Query().Get("name"))
	if target == "" {
		target = strings.TrimSpace(r.URL.Query().Get("auth_index"))
	}
	models := make([]map[string]any, 0)
	for _, acct := range cfg.Accounts {
		prov := cliProxyProviderName(acct.Backend)
		fileName := fmt.Sprintf("%s-%s.json", prov, acct.Name)
		if target != "" && target != fileName && target != acct.ID && target != acct.Backend+"/"+acct.Name {
			continue
		}
		for _, m := range acct.Models {
			disp := acct.ModelLabels[m]
			if disp == "" {
				disp = m
			}
			models = append(models, map[string]any{
				"id":           m,
				"display_name": disp,
				"type":         prov,
				"owned_by":     prov,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"models": models,
	})
}

func (s *Server) handleCLIProxyAuthFiles(w http.ResponseWriter, r *http.Request) {
	cfg := s.Config()
	qRep := s.LiveQuota()
	qByID := make(map[string]quota.AccountQuota, len(qRep.Accounts))
	for _, q := range qRep.Accounts {
		qByID[q.AccountID] = q
	}

	now := time.Now().UTC().Format(time.RFC3339)
	filterName := strings.TrimSpace(r.URL.Query().Get("name"))
	filterIdx := strings.TrimSpace(r.URL.Query().Get("auth_index"))

	files := make([]map[string]any, 0, len(cfg.Accounts))
	for _, acct := range cfg.Accounts {
		prov := cliProxyProviderName(acct.Backend)
		fileName := fmt.Sprintf("%s-%s.json", prov, acct.Name)
		if filterName != "" && filterName != fileName && filterName != acct.ID {
			continue
		}
		if filterIdx != "" && filterIdx != acct.ID {
			continue
		}

		authKind := "oauth"
		if acct.Kind == config.KindAPIKey {
			authKind = "api_key"
		}
		status := "active"
		statusMsg := ""
		if !acct.Enabled {
			status = "disabled"
			statusMsg = acct.DisabledReason
		}

		q := qByID[acct.ID]
		plan := q.PlanType
		if plan == "" {
			plan = prov
		}
		updatedAt := now
		if !q.CheckedAt.IsZero() {
			updatedAt = q.CheckedAt.UTC().Format(time.RFC3339)
		}

		entry := map[string]any{
			"id":             acct.ID,
			"auth_index":     acct.ID,
			"name":           fileName,
			"provider":       prov,
			"type":           prov,
			"auth_kind":      authKind,
			"label":          acct.Backend + "/" + acct.Name,
			"account":        acct.Backend + "/" + acct.Name,
			"email":          acct.Backend + "/" + acct.Name,
			"account_id":     acct.ID,
			"account_type":   plan,
			"disabled":       !acct.Enabled,
			"unavailable":    !acct.Enabled,
			"status":         status,
			"status_message": statusMsg,
			"source":         "apx",
			"created_at":     now,
			"updated_at":     updatedAt,
			"modtime":        updatedAt,
			"metadata": map[string]any{
				"account_id": acct.ID,
				"email":      acct.Backend + "/" + acct.Name,
				"plan_type":  plan,
				"backend":    acct.Backend,
			},
			"id_token": map[string]any{
				"chatgpt_account_id": acct.ID,
				"email":              acct.Backend + "/" + acct.Name,
				"plan_type":          plan,
			},
		}
		files = append(files, entry)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"observed_at": now,
		"files":       files,
		"total":       len(files),
	})
}

type cliProxyAPICallReq struct {
	AuthIndex string            `json:"auth_index"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Header    map[string]string `json:"header"`
	Data      any               `json:"data"`
}

func (s *Server) handleCLIProxyAPICall(w http.ResponseWriter, r *http.Request) {
	var req cliProxyAPICallReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "Invalid JSON body: "+err.Error())
		return
	}

	cfg := s.Config()
	acct := cfg.FindAccount(req.AuthIndex)
	if acct == nil {
		// Also match by "<provider>-<name>.json" or "<backend>/<name>"
		for i := range cfg.Accounts {
			a := &cfg.Accounts[i]
			prov := cliProxyProviderName(a.Backend)
			if req.AuthIndex == fmt.Sprintf("%s-%s.json", prov, a.Name) || req.AuthIndex == a.Backend+"/"+a.Name {
				acct = a
				break
			}
		}
	}
	if acct == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"status_code": http.StatusNotFound,
			"header":      map[string][]string{"Content-Type": {"application/json"}},
			"body":        `{"error":"account not found"}`,
		})
		return
	}

	qRep := s.LiveQuota()
	var aq *quota.AccountQuota
	for i := range qRep.Accounts {
		if qRep.Accounts[i].AccountID == acct.ID {
			cp := qRep.Accounts[i]
			aq = &cp
			break
		}
	}
	// If quota hasn't been fetched yet on a live daemon, refresh this single account on demand
	// with a bounded timeout so external hub polls never stall.
	if s.Running() && (aq == nil || (aq.CheckedAt.IsZero() && aq.Primary == nil && aq.Secondary == nil && aq.Credits == "")) {
		qCtx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		refreshed, err := s.RefreshQuotaNow(qCtx, acct.ID)
		cancel()
		if err == nil {
			for i := range refreshed.Accounts {
				if refreshed.Accounts[i].AccountID == acct.ID {
					cp := refreshed.Accounts[i]
					aq = &cp
					break
				}
			}
		}
	}
	if aq == nil {
		aq = &quota.AccountQuota{
			AccountID:   acct.ID,
			AccountName: acct.Backend + "/" + acct.Name,
			Backend:     acct.Backend,
			Kind:        acct.Kind,
			Source:      "local-tracked",
		}
	}

	statusCode, payload := synthesizeCLIProxyUpstreamResponse(*acct, *aq, req.URL, time.Now())
	bodyBytes, _ := json.Marshal(payload)

	writeJSON(w, http.StatusOK, map[string]any{
		"status_code": statusCode,
		"header": map[string][]string{
			"Content-Type": {"application/json"},
		},
		"body": string(bodyBytes),
	})
}

func synthesizeCLIProxyUpstreamResponse(acct config.AccountConfig, aq quota.AccountQuota, reqURL string, now time.Time) (int, any) {
	lowerURL := strings.ToLower(reqURL)
	backend := strings.ToLower(acct.Backend)

	switch {
	case strings.Contains(lowerURL, "wham/usage") || backend == "codex":
		return http.StatusOK, buildCodexWhamPayload(aq, now)

	case strings.Contains(lowerURL, "oauth/usage") || backend == "claude":
		return http.StatusOK, buildClaudeOAuthUsagePayload(aq, now)

	case strings.Contains(lowerURL, "loadcodeassist"):
		tierID := "g1-pro-tier"
		tierName := "Antigravity"
		if aq.PlanType != "" {
			tierName = aq.PlanType
		}
		return http.StatusOK, map[string]any{
			"currentTier":            map[string]any{"id": tierID, "name": tierName},
			"paidTier":               map[string]any{"id": tierID, "name": tierName},
			"cloudaicompanionProject": "apx-" + acct.Name,
		}

	case strings.Contains(lowerURL, "fetchavailablemodels") || strings.Contains(lowerURL, "retrieveuserquotasummary") || backend == "agy":
		return http.StatusOK, buildAntigravityQuotaPayload(acct, aq, now)

	case strings.Contains(lowerURL, "user/balance") || backend == "deepseek":
		bal := strings.TrimPrefix(strings.TrimSpace(aq.Credits), "bal:")
		bal = strings.TrimSpace(bal)
		fields := strings.Fields(bal)
		amount := "0.00"
		currency := "USD"
		if len(fields) >= 1 {
			amount = fields[0]
		}
		if len(fields) >= 2 {
			currency = fields[1]
		}
		avail := aq.Primary == nil || aq.Primary.LeftPercent > 0
		return http.StatusOK, map[string]any{
			"is_available": avail,
			"balance_infos": []map[string]any{
				{
					"currency":          currency,
					"total_balance":     amount,
					"granted_balance":   "0.00",
					"topped_up_balance": amount,
				},
			},
		}

	default:
		return http.StatusOK, aq
	}
}

func buildCodexWhamPayload(aq quota.AccountQuota, now time.Time) map[string]any {
	toWhamWin := func(w *quota.Window) map[string]any {
		if w == nil {
			return nil
		}
		winSecs := w.WindowMins * 60
		if winSecs <= 0 {
			winSecs = 18000
		}
		var resetAt int64
		var resetAfter int64
		if w.ResetsAt != nil && !w.ResetsAt.IsZero() {
			resetAt = w.ResetsAt.Unix()
			if d := int64(w.ResetsAt.Sub(now).Seconds()); d > 0 {
				resetAfter = d
			}
		}
		return map[string]any{
			"used_percent":         int(math.Round(w.UsedPercent)),
			"limit_window_seconds": winSecs,
			"reset_after_seconds":  resetAfter,
			"reset_at":             resetAt,
		}
	}

	rateLimit := map[string]any{
		"allowed":       true,
		"limit_reached": false,
	}
	if pw := toWhamWin(aq.Primary); pw != nil {
		rateLimit["primary_window"] = pw
		if aq.Primary.LeftPercent <= 0 {
			rateLimit["limit_reached"] = true
		}
	}
	if sw := toWhamWin(aq.Secondary); sw != nil {
		rateLimit["secondary_window"] = sw
		if aq.Secondary.LeftPercent <= 0 {
			rateLimit["limit_reached"] = true
		}
	}
	plan := aq.PlanType
	if plan == "" {
		plan = "free"
	}
	return map[string]any{
		"plan_type":  plan,
		"rate_limit": rateLimit,
		"credits": map[string]any{
			"has_credits": aq.Credits != "",
			"unlimited":   strings.Contains(strings.ToLower(aq.Credits), "unlimited"),
			"balance":     aq.Credits,
		},
	}
}

func buildClaudeOAuthUsagePayload(aq quota.AccountQuota, now time.Time) map[string]any {
	toBucket := func(w *quota.Window) map[string]any {
		if w == nil {
			return nil
		}
		out := map[string]any{
			"utilization": w.UsedPercent,
		}
		if w.ResetsAt != nil && !w.ResetsAt.IsZero() {
			out["resets_at"] = w.ResetsAt.UTC().Format(time.RFC3339)
		} else {
			out["resets_at"] = nil
		}
		return out
	}

	res := map[string]any{
		"five_hour":        nil,
		"seven_day":        nil,
		"seven_day_opus":   nil,
		"seven_day_sonnet": nil,
		"extra_usage": map[string]any{
			"is_enabled":    false,
			"monthly_limit": nil,
			"used_credits":  nil,
			"utilization":   nil,
		},
	}

	assign := func(w *quota.Window) {
		if w == nil {
			return
		}
		l := strings.ToLower(w.Label)
		switch {
		case strings.Contains(l, "5h") || strings.Contains(l, "session") || strings.Contains(l, "hour"):
			res["five_hour"] = toBucket(w)
		case strings.Contains(l, "sonnet"):
			res["seven_day_sonnet"] = toBucket(w)
		case strings.Contains(l, "opus") || strings.Contains(l, "fable"):
			res["seven_day_opus"] = toBucket(w)
		default:
			if res["seven_day"] == nil {
				res["seven_day"] = toBucket(w)
			} else if res["seven_day_opus"] == nil {
				res["seven_day_opus"] = toBucket(w)
			}
		}
	}
	assign(aq.Primary)
	assign(aq.Secondary)
	return res
}

func buildAntigravityQuotaPayload(acct config.AccountConfig, aq quota.AccountQuota, now time.Time) map[string]any {
	// Choose the most constrained remaining fraction across Primary (5h) and Secondary (Weekly).
	remFrac := 1.0
	var resetStr string
	pick := func(w *quota.Window) {
		if w == nil {
			return
		}
		f := math.Max(0, math.Min(1, w.LeftPercent/100.0))
		if f < remFrac {
			remFrac = f
		}
		if resetStr == "" && w.ResetsAt != nil && !w.ResetsAt.IsZero() {
			resetStr = w.ResetsAt.UTC().Format(time.RFC3339)
		}
	}
	pick(aq.Primary)
	pick(aq.Secondary)

	modelsMap := make(map[string]any, max(len(acct.Models), 1))
	modelList := acct.Models
	if len(modelList) == 0 {
		modelList = []string{"gemini-4-hydrogen", "gemini-3.8-flash"}
	}
	for _, m := range modelList {
		qi := map[string]any{
			"remainingFraction": math.Round(remFrac*1000) / 1000,
		}
		if resetStr != "" {
			qi["resetTime"] = resetStr
		}
		display := acct.ModelLabels[m]
		if display == "" {
			display = m
		}
		modelsMap[m] = map[string]any{
			"displayName": display,
			"quotaInfo":   qi,
		}
	}

	var buckets []map[string]any
	addBucket := func(winName string, w *quota.Window) {
		if w == nil {
			return
		}
		b := map[string]any{
			"window":             winName,
			"usage_fraction":     math.Round((w.UsedPercent/100.0)*1000) / 1000,
			"remaining_fraction": math.Round((w.LeftPercent/100.0)*1000) / 1000,
		}
		if w.ResetsAt != nil && !w.ResetsAt.IsZero() {
			b["reset_time"] = w.ResetsAt.UTC().Format(time.RFC3339)
		}
		buckets = append(buckets, b)
	}
	addBucket("5h", aq.Primary)
	addBucket("7d", aq.Secondary)

	return map[string]any{
		"models": modelsMap,
		"quota_summary": map[string]any{
			"plan_tier": aq.PlanType,
			"groups": []map[string]any{
				{
					"model_group": "Gemini Models",
					"buckets":     buckets,
				},
			},
		},
	}
}

func (s *Server) handleCLIProxyUsage(w http.ResponseWriter, r *http.Request) {
	u := s.LiveUsage()

	reqByDay := make(map[string]int, len(u.Daily14d))
	tokByDay := make(map[string]int, len(u.Daily14d))
	for _, b := range u.Daily14d {
		reqByDay[b.Label] = b.Requests
		tokByDay[b.Label] = b.Tokens
	}
	reqByHour := make(map[string]int, len(u.Hourly24h))
	tokByHour := make(map[string]int, len(u.Hourly24h))
	for _, b := range u.Hourly24h {
		reqByHour[b.Label] = b.Requests
		tokByHour[b.Label] = b.Tokens
	}

	apis := make(map[string]any, len(u.ByAccount))
	for _, ac := range u.ByAccount {
		apis[ac.Account] = map[string]any{
			"total_requests": ac.Requests,
			"total_tokens":   ac.TotalTokens,
			"error_requests": ac.Errors,
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"failed_requests": u.ErrorRequests,
		"usage": map[string]any{
			"total_requests":   u.TotalRequests,
			"success_count":    u.SuccessRequests,
			"failure_count":    u.ErrorRequests,
			"total_tokens":     u.TotalTokens,
			"requests_by_day":  reqByDay,
			"requests_by_hour": reqByHour,
			"tokens_by_day":    tokByDay,
			"tokens_by_hour":   tokByHour,
			"apis":             apis,
		},
	})
}

func (s *Server) handleCLIProxyUsageQueue(w http.ResponseWriter, r *http.Request) {
	u := s.LiveUsage()
	limit := len(u.Recent)
	if raw := strings.TrimSpace(r.URL.Query().Get("count")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n < limit {
			limit = n
		}
	}
	items := make([]map[string]any, 0, limit)
	for i := 0; i < limit; i++ {
		rec := u.Recent[i]
		items = append(items, map[string]any{
			"timestamp":         rec.Time.UTC().Format(time.RFC3339),
			"latency_ms":        rec.DurationMs,
			"source":            rec.Account,
			"api_key":           rec.APIKey,
			"provider":          cliProxyProviderName(rec.Backend),
			"model":             rec.Model,
			"prompt_tokens":     rec.PromptTokens,
			"completion_tokens": rec.CompletionTokens,
			"total_tokens":      rec.TotalTokens,
			"status_code":       rec.Status,
		})
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleCLIProxyUnknown(w http.ResponseWriter, r *http.Request) {
	if s.logs != nil {
		s.logs.Warn("compat", fmt.Sprintf("unimplemented CLIProxyAPI management path: %s %s", r.Method, r.URL.Path))
	}
	writeOpenAIError(w, http.StatusNotFound, "not_found", fmt.Sprintf("management endpoint %s not implemented", r.URL.Path))
}
