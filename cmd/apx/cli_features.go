package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/gateway"
	"github.com/bberka/agentpassage/internal/privacy"
	"github.com/bberka/agentpassage/internal/router"
	"github.com/bberka/agentpassage/internal/service"
)

func cmdStatus(args []string) error {
	asJSON := false
	for _, a := range args {
		if a == "--json" {
			asJSON = true
		}
	}
	dataDir, cfg, sec, reg, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	srv := gateway.New(dataDir, cfg, sec, reg, logs)
	attached, _ := srv.AttachIfRunning()
	svc := service.Status()
	models := router.New(cfg).ListModels(false)

	payload := map[string]any{
		"version":         gateway.Version,
		"data_dir":        dataDir,
		"base_url":        cfg.BaseURL(),
		"daemon_running":  attached,
		"service":         svc,
		"accounts_count":  len(cfg.Accounts),
		"models_count":    len(models),
		"routing_rules":   len(cfg.Routing),
		"api_keys_count":  1 + len(cfg.Server.APIKeys),
		"privacy_enabled": cfg.Privacy.Enabled,
		"secrets_backend": sec.Backend(),
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(payload)
	}
	state := "stopped"
	if attached {
		state = "running (" + cfg.BaseURL() + ")"
	}
	fmt.Printf("AgentPassage (apx) %s\n", gateway.Version)
	fmt.Printf("  Gateway:  %s\n", state)
	fmt.Printf("  Service:  %s (%s)\n", svc.Manager, svc.Detail)
	fmt.Printf("  Data Dir: %s (secrets: %s)\n", dataDir, sec.Backend())
	fmt.Printf("  Catalog:  %d account(s), %d exposed model(s), %d routing rule(s), %d API key(s)\n",
		len(cfg.Accounts), len(models), len(cfg.Routing), 1+len(cfg.Server.APIKeys))
	fmt.Printf("  Privacy:  enabled=%v (api_keys=%v emails=%v phones=%v custom=%d)\n",
		cfg.Privacy.Enabled, cfg.Privacy.RedactAPIKeys, cfg.Privacy.RedactEmails, cfg.Privacy.RedactPhones, len(cfg.Privacy.CustomPatterns))
	return nil
}

func cmdAccounts(args []string) error {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "list", "ls":
		return cmdList(args)
	case "add":
		return cmdAdd(args)
	case "enable", "disable":
		if len(args) == 0 {
			return fmt.Errorf("usage: apx accounts %s <id|name>", sub)
		}
		dataDir, cfg, _, _, logs, err := loadRuntime(0, "", false)
		if err != nil {
			return err
		}
		defer logs.Close()
		acct := cfg.FindAccount(args[0])
		if acct == nil {
			return fmt.Errorf("account %q not found", args[0])
		}
		acct.Enabled = sub == "enable"
		if acct.Enabled {
			acct.DisabledReason = ""
		}
		if err := config.Save(dataDir, cfg); err != nil {
			return err
		}
		gateway.NotifyRunningDaemon(cfg)
		fmt.Printf("Account %s/%s %sd.\n", acct.Backend, acct.Name, sub)
		return nil

	case "edit":
		if len(args) == 0 {
			return fmt.Errorf("usage: apx accounts edit <id|name> [--expose pool|prefix|both] [--prefix p-] [--hourly-limit N] [--daily-limit N] [--weekly-limit N] [--concurrency N] [--permission deny|auto-approve|ask]")
		}
		target := args[0]
		dataDir, cfg, _, _, logs, err := loadRuntime(0, "", false)
		if err != nil {
			return err
		}
		defer logs.Close()
		acct := cfg.FindAccount(target)
		if acct == nil {
			return fmt.Errorf("account %q not found", target)
		}
		for i := 1; i < len(args); i++ {
			val := ""
			if i+1 < len(args) {
				val = args[i+1]
			}
			switch args[i] {
			case "--expose":
				i++
				acct.ExposeMode = val
			case "--prefix":
				i++
				acct.ModelPrefix = val
			case "--hourly-limit":
				i++
				n, err := strconv.Atoi(val)
				if err != nil {
					return fmt.Errorf("invalid --hourly-limit %q", val)
				}
				acct.HourlyLimit = n
			case "--daily-limit":
				i++
				n, err := strconv.Atoi(val)
				if err != nil {
					return fmt.Errorf("invalid --daily-limit %q", val)
				}
				acct.DailyLimit = n
			case "--weekly-limit":
				i++
				n, err := strconv.Atoi(val)
				if err != nil {
					return fmt.Errorf("invalid --weekly-limit %q", val)
				}
				acct.WeeklyLimit = n
			case "--concurrency":
				i++
				n, err := strconv.Atoi(val)
				if err != nil || n < 1 {
					return fmt.Errorf("invalid --concurrency %q", val)
				}
				acct.MaxConcurrency = n
			case "--permission":
				i++
				acct.PermissionMode = val
			case "--daily-budget-usd":
				i++
				v, err := strconv.ParseFloat(val, 64)
				if err != nil || v < 0 {
					return fmt.Errorf("invalid --daily-budget-usd %q", val)
				}
				acct.DailyBudgetUSD = v
			case "--monthly-budget-usd":
				i++
				v, err := strconv.ParseFloat(val, 64)
				if err != nil || v < 0 {
					return fmt.Errorf("invalid --monthly-budget-usd %q", val)
				}
				acct.MonthlyBudgetUSD = v
			}
		}
		if err := config.Save(dataDir, cfg); err != nil {
			return err
		}
		gateway.NotifyRunningDaemon(cfg)
		fmt.Printf("Updated account %s/%s.\n", acct.Backend, acct.Name)
		return nil

	case "remove", "rm", "delete":
		target := ""
		yes := false
		for _, a := range args {
			if a == "--yes" || a == "-y" {
				yes = true
			} else if !strings.HasPrefix(a, "-") && target == "" {
				target = a
			}
		}
		if target == "" {
			return fmt.Errorf("usage: apx accounts remove <id|name> [--yes]")
		}
		dataDir, cfg, sec, _, logs, err := loadRuntime(0, "", false)
		if err != nil {
			return err
		}
		defer logs.Close()
		acct := cfg.FindAccount(target)
		if acct == nil {
			return fmt.Errorf("account %q not found", target)
		}
		if !yes {
			if !confirmAction(fmt.Sprintf("Permanently remove account %s/%s (and its isolated profile)?", acct.Backend, acct.Name)) {
				fmt.Println("Cancelled.")
				return nil
			}
		}
		if acct.Kind == config.KindACP || acct.Kind == config.KindCLI {
			_ = config.DeleteProfileDir(dataDir, acct.Backend, acct.Name)
		}
		_ = sec.Delete(acct.ID)
		kept := make([]config.AccountConfig, 0, len(cfg.Accounts)-1)
		for _, a := range cfg.Accounts {
			if a.ID != acct.ID {
				kept = append(kept, a)
			}
		}
		cfg.Accounts = kept
		if err := config.Save(dataDir, cfg); err != nil {
			return err
		}
		gateway.NotifyRunningDaemon(cfg)
		fmt.Printf("Removed account %s/%s.\n", acct.Backend, acct.Name)
		return nil

	default:
		return fmt.Errorf("usage: apx accounts [list|add|enable|disable|edit|remove]")
	}
}

func cmdRouting(args []string) error {
	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}
	if sub == "explain" || sub == "why" {
		return cmdRoutingExplain(args)
	}
	dataDir, cfg, _, _, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	switch sub {
	case "list", "ls":
		asJSON := false
		for _, a := range args {
			if a == "--json" {
				asJSON = true
			}
		}
		if asJSON {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{
				"default_strategy": config.StrategyFailover,
				"rules":            cfg.Routing,
			})
		}
		fmt.Printf("Routing Rules (%d) — default without a rule: primary-first failover (429 retry on)\n", len(cfg.Routing))
		if len(cfg.Routing) == 0 {
			fmt.Println("  (no custom rules)")
			return nil
		}
		fmt.Printf("  %-20s %-12s %-12s %-8s %-22s %s\n", "PATTERN", "PROJECT", "STRATEGY", "429", "ACCOUNTS", "FALLBACK MODELS")
		for _, r := range cfg.Routing {
			proj := r.Project
			if proj == "" {
				proj = "*"
			}
			accts := "(all)"
			if len(r.Accounts) > 0 {
				accts = strings.Join(r.Accounts, ",")
			}
			fbs := "—"
			if len(r.FallbackModels) > 0 {
				fbs = strings.Join(r.FallbackModels, " -> ")
			}
			r429 := "off"
			if r.AllowCrossAccountRetryOn429 {
				r429 = "on"
			}
			fmt.Printf("  %-20s %-12s %-12s %-8s %-22s %s\n", r.Pattern, proj, r.Strategy, r429, accts, fbs)
		}
		return nil

	case "add", "set", "edit":
		rule := config.RoutingRule{
			Strategy:                    config.StrategyFailover,
			AllowCrossAccountRetryOn429: true,
		}
		acceptSubRisk := false
		asJSON := false
		for i := 0; i < len(args); i++ {
			nextVal := func() string {
				if i+1 < len(args) {
					i++
					return args[i]
				}
				return ""
			}
			switch args[i] {
			case "--pattern":
				rule.Pattern = nextVal()
			case "--project":
				rule.Project = nextVal()
			case "--strategy":
				rule.Strategy = nextVal()
			case "--accounts":
				rule.Accounts = splitCSV(nextVal())
			case "--fallback", "--fallback-models":
				rule.FallbackModels = splitCSV(nextVal())
			case "--fallback-on":
				rule.FallbackOn = splitCSV(nextVal())
			case "--retry-429":
				v := strings.ToLower(nextVal())
				rule.AllowCrossAccountRetryOn429 = v == "true" || v == "1" || v == "yes" || v == "on"
			case "--quota-threshold":
				n, err := strconv.Atoi(nextVal())
				if err != nil || n < 0 || n > 100 {
					return fmt.Errorf("invalid --quota-threshold (0-100)")
				}
				rule.QuotaThresholdPct = n
			case "--default-effort":
				rule.DefaultEffort = nextVal()
			case "--system-prompt":
				rule.SystemPrompt = nextVal()
			case "--accept-sub-risk":
				acceptSubRisk = true
			case "--json":
				asJSON = true
			default:
				if !strings.HasPrefix(args[i], "-") && rule.Pattern == "" {
					rule.Pattern = args[i]
				}
			}
		}
		if rule.Pattern == "" {
			return fmt.Errorf("usage: apx routing add <pattern> [--project p] [--strategy failover|sticky|manual|round-robin|least-busy|most-quota] [--accounts a,b] [--quota-threshold 90] [--default-effort off|low|medium|high|xhigh|max] [--system-prompt text] [--fallback m1,m2] [--retry-429 true|false] [--accept-sub-risk]")
		}
		if rule.Strategy == config.StrategyRoundRobin || rule.Strategy == config.StrategyLeastBusy {
			if routingRuleTouchesSub(cfg, rule) {
				if !acceptSubRisk {
					return fmt.Errorf("refusing to enable %q on subscription (ACP/CLI) accounts without --accept-sub-risk (spreading subscription traffic can cause provider bans; use --strategy failover instead)", rule.Strategy)
				}
				rule.SubscriptionWarningAcked = true
			}
		}
		updated := false
		for i := range cfg.Routing {
			if cfg.Routing[i].Pattern == rule.Pattern && cfg.Routing[i].Project == rule.Project {
				cfg.Routing[i] = rule
				updated = true
				break
			}
		}
		if !updated {
			cfg.Routing = append(cfg.Routing, rule)
		}
		if err := config.Save(dataDir, cfg); err != nil {
			return err
		}
		gateway.NotifyRunningDaemon(cfg)
		if asJSON {
			return json.NewEncoder(os.Stdout).Encode(rule)
		}
		action := "Added"
		if updated {
			action = "Updated"
		}
		fmt.Printf("%s routing rule pattern=%q project=%q strategy=%s fallback=%v\n", action, rule.Pattern, rule.Project, rule.Strategy, rule.FallbackModels)
		return nil

	case "remove", "rm", "delete":
		pattern := ""
		project := ""
		for i := 0; i < len(args); i++ {
			if args[i] == "--project" && i+1 < len(args) {
				i++
				project = args[i]
			} else if !strings.HasPrefix(args[i], "-") && pattern == "" {
				pattern = args[i]
			}
		}
		if pattern == "" {
			return fmt.Errorf("usage: apx routing remove <pattern> [--project name]")
		}
		var kept []config.RoutingRule
		removed := 0
		for _, r := range cfg.Routing {
			if r.Pattern == pattern && (project == "" || r.Project == project) {
				removed++
				continue
			}
			kept = append(kept, r)
		}
		if removed == 0 {
			return fmt.Errorf("routing rule %q not found", pattern)
		}
		cfg.Routing = kept
		if err := config.Save(dataDir, cfg); err != nil {
			return err
		}
		gateway.NotifyRunningDaemon(cfg)
		fmt.Printf("Removed %d routing rule(s) matching %q.\n", removed, pattern)
		return nil

	default:
		return fmt.Errorf("usage: apx routing [list|add|edit|remove]")
	}
}

func routingRuleTouchesSub(cfg *config.Config, rule config.RoutingRule) bool {
	isSub := func(a config.AccountConfig) bool { return a.Kind == config.KindACP || a.Kind == config.KindCLI }
	if len(rule.Accounts) > 0 {
		for _, id := range rule.Accounts {
			if a := cfg.FindAccount(id); a != nil && isSub(*a) {
				return true
			}
		}
		return false
	}
	for _, a := range cfg.Accounts {
		if !isSub(a) {
			continue
		}
		if rule.Pattern == "*" || strings.HasPrefix(rule.Pattern, a.Backend+"/") {
			return true
		}
		for _, m := range a.Models {
			if m == rule.Pattern {
				return true
			}
		}
	}
	return false
}

func cmdPrivacy(args []string) error {
	sub := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}
	dataDir, cfg, _, _, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	switch sub {
	case "status":
		asJSON := false
		for _, a := range args {
			if a == "--json" {
				asJSON = true
			}
		}
		if asJSON {
			return json.NewEncoder(os.Stdout).Encode(cfg.Privacy)
		}
		fmt.Printf("Privacy Filter: enabled=%v\n", cfg.Privacy.Enabled)
		fmt.Printf("  API keys:        %v\n", cfg.Privacy.RedactAPIKeys)
		fmt.Printf("  Emails:          %v\n", cfg.Privacy.RedactEmails)
		fmt.Printf("  Phone numbers:   %v\n", cfg.Privacy.RedactPhones)
		fmt.Printf("  Custom patterns: %d\n", len(cfg.Privacy.CustomPatterns))
		for _, cp := range cfg.Privacy.CustomPatterns {
			fmt.Printf("    - %s: %s\n", cp.Name, cp.Pattern)
		}
		return nil

	case "enable", "disable":
		cfg.Privacy.Enabled = sub == "enable"
		if err := config.Save(dataDir, cfg); err != nil {
			return err
		}
		gateway.NotifyRunningDaemon(cfg)
		fmt.Printf("Privacy filter %sd.\n", sub)
		return nil

	case "config", "set":
		parseBool := func(s string) bool {
			v := strings.ToLower(strings.TrimSpace(s))
			return v == "true" || v == "1" || v == "yes" || v == "on"
		}
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "--enabled":
				if i+1 < len(args) {
					i++
					cfg.Privacy.Enabled = parseBool(args[i])
				}
			case "--api-keys":
				if i+1 < len(args) {
					i++
					cfg.Privacy.RedactAPIKeys = parseBool(args[i])
				}
			case "--emails":
				if i+1 < len(args) {
					i++
					cfg.Privacy.RedactEmails = parseBool(args[i])
				}
			case "--phones":
				if i+1 < len(args) {
					i++
					cfg.Privacy.RedactPhones = parseBool(args[i])
				}
			case "--add-pattern":
				if i+1 < len(args) {
					i++
					name, pat, ok := strings.Cut(args[i], "=")
					if !ok || strings.TrimSpace(pat) == "" {
						return fmt.Errorf("--add-pattern expects name=regex")
					}
					cfg.Privacy.CustomPatterns = append(cfg.Privacy.CustomPatterns, config.PrivacyPattern{
						Name:    strings.TrimSpace(name),
						Pattern: strings.TrimSpace(pat),
					})
				}
			case "--clear-custom":
				cfg.Privacy.CustomPatterns = nil
			}
		}
		if err := config.Save(dataDir, cfg); err != nil {
			return err
		}
		gateway.NotifyRunningDaemon(cfg)
		fmt.Printf("Updated privacy filter config (enabled=%v api_keys=%v emails=%v phones=%v custom=%d).\n",
			cfg.Privacy.Enabled, cfg.Privacy.RedactAPIKeys, cfg.Privacy.RedactEmails, cfg.Privacy.RedactPhones, len(cfg.Privacy.CustomPatterns))
		return nil

	case "test":
		asJSON := false
		var textParts []string
		for _, a := range args {
			if a == "--json" {
				asJSON = true
			} else {
				textParts = append(textParts, a)
			}
		}
		sample := strings.Join(textParts, " ")
		if sample == "" {
			return fmt.Errorf("usage: apx privacy test [--json] <text>")
		}
		pCfg := cfg.Privacy
		pCfg.Enabled = true
		sess := privacy.NewFilter(pCfg).NewSession()
		redacted := sess.RedactText(sample)
		restored := sess.RestoreText(redacted)
		sum := sess.Summary()
		if asJSON {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{
				"redacted": redacted,
				"restored": restored,
				"summary":  sum,
			})
		}
		fmt.Printf("Redacted: %s\n", redacted)
		fmt.Printf("Restored: %s\n", restored)
		fmt.Printf("Items:    %d (%s)\n", sum.Total, sess.HeaderValue())
		for _, it := range sum.Items {
			fmt.Printf("  - %-10s %s (%s)\n", it.Category, it.Placeholder, it.MaskedPreview)
		}
		return nil

	default:
		return fmt.Errorf("usage: apx privacy [status|enable|disable|config|test <text>]")
	}
}

func cmdEmbed(args []string) error {
	model := ""
	project := ""
	var inputs []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model", "-m":
			if i+1 < len(args) {
				i++
				model = args[i]
			}
		case "--project", "-p":
			if i+1 < len(args) {
				i++
				project = args[i]
			}
		default:
			if !strings.HasPrefix(args[i], "-") {
				inputs = append(inputs, args[i])
			}
		}
	}
	if model == "" || len(inputs) == 0 {
		return fmt.Errorf("usage: apx embed --model <model> [--project <name>] <text...>")
	}
	return postGatewayJSON("/embeddings", project, map[string]any{
		"model": model,
		"input": inputs,
	})
}

func cmdRerank(args []string) error {
	model := ""
	query := ""
	project := ""
	topN := 0
	var docs []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--model", "-m":
			if i+1 < len(args) {
				i++
				model = args[i]
			}
		case "--query", "-q":
			if i+1 < len(args) {
				i++
				query = args[i]
			}
		case "--project", "-p":
			if i+1 < len(args) {
				i++
				project = args[i]
			}
		case "--top-n":
			if i+1 < len(args) {
				i++
				topN, _ = strconv.Atoi(args[i])
			}
		default:
			if !strings.HasPrefix(args[i], "-") {
				docs = append(docs, args[i])
			}
		}
	}
	if model == "" || query == "" || len(docs) == 0 {
		return fmt.Errorf("usage: apx rerank --model <model> --query <query> [--top-n N] [--project <name>] <doc1> <doc2>...")
	}
	payload := map[string]any{
		"model":     model,
		"query":     query,
		"documents": docs,
	}
	if topN > 0 {
		payload["top_n"] = topN
	}
	return postGatewayJSON("/rerank", project, payload)
}

func postGatewayJSON(subpath, project string, payload map[string]any) error {
	_, cfg, _, _, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, cfg.BaseURL()+subpath, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
	if project != "" {
		req.Header.Set("X-Apx-Project", project)
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("gateway request failed (is `apx service start` running?): %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	_, _ = os.Stdout.Write(body)
	if len(body) > 0 && body[len(body)-1] != '\n' {
		fmt.Println()
	}
	return nil
}

func cmdAgentGuide() error {
	fmt.Print(`# AgentPassage (apx) — Agent Automation Guide

All state-changing commands hot-reload the running apx service automatically.
Use --json for structured output and --yes to skip interactive prompts.

## Inspection
  apx status --json
  apx accounts list --json
  apx models list --json
  apx routing list --json
  apx keys list --json
  apx privacy status --json
  apx usage --period 24h --refresh --json
  apx health --json

## Accounts & Models
  apx add --kind acp --backend agy --name work
  apx add --kind api_key --backend deepseek --name main --api-key <key>
  apx accounts enable|disable <id|name>
  apx accounts edit <id|name> --expose pool|prefix|both --prefix p- --hourly-limit 50 --weekly-limit 500
  apx accounts remove <id|name> --yes
  apx models fetch <account>
  apx models enable|disable <account> <model|all>

## Routing, Project Scoping & Model Fallback
  apx routing add "agy/*" --strategy failover --retry-429 true --fallback "codex/gpt-5,deepseek/deepseek-chat"
  apx routing add "deepseek/*" --project work --accounts ds-work --strategy round-robin
  apx routing remove "agy/*" --project work

## Privacy Filter (Reversible Redaction)
  apx privacy enable
  apx privacy config --api-keys true --emails true --phones true --add-pattern ticket="PROJ-[0-9]+"
  apx privacy test --json "Contact alice@example.com with sk-proj-1234567890abcdef"

## Scoped API Keys & Remote Access
  apx keys create work-bot --role client --project work --models "deepseek/*,openai/*" --json
  apx keys disable|enable|revoke work-bot
  apx remote

## Embeddings & Reranking
  apx embed --model text-embedding-3-small --project search "hello world"
  apx rerank --model rerank-v3 --query "go proxy" --top-n 2 "doc one" "doc two"

## IDE Connections & Service
  apx connect t3code --yes
  apx disconnect t3code --yes
  apx service status|start|stop|restart|install|uninstall
`)
	return nil
}

func cmdExportConfig(args []string) error {
	outPath := ""
	for i := 0; i < len(args); i++ {
		if (args[i] == "--out" || args[i] == "-o") && i+1 < len(args) {
			i++
			outPath = args[i]
		}
	}
	dataDir, err := config.DataDir()
	if err != nil {
		return err
	}
	cfg, _, err := config.Load(dataDir)
	if err != nil {
		return err
	}
	// Strip all local/client API key secrets before exporting.
	sanitized := *cfg
	sanitized.Server.LocalKey = ""
	sanitized.Server.APIKeys = nil
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(&sanitized); err != nil {
		return err
	}
	if outPath != "" {
		if err := os.WriteFile(outPath, buf.Bytes(), 0o600); err != nil {
			return err
		}
		fmt.Printf("Exported secret-free config to %s\n", outPath)
		return nil
	}
	fmt.Print(buf.String())
	return nil
}

func cmdImportConfig(args []string) error {
	inPath := ""
	merge := false
	for _, a := range args {
		if a == "--merge" {
			merge = true
		} else if !strings.HasPrefix(a, "-") && inPath == "" {
			inPath = a
		}
	}
	if inPath == "" {
		return fmt.Errorf("usage: apx import-config <file.toml> [--merge]")
	}
	raw, err := os.ReadFile(inPath)
	if err != nil {
		return err
	}
	var incoming config.Config
	if _, err := toml.Decode(string(raw), &incoming); err != nil {
		return fmt.Errorf("parse %s: %w", inPath, err)
	}
	dataDir, err := config.DataDir()
	if err != nil {
		return err
	}
	cur, _, err := config.Load(dataDir)
	if err != nil {
		return err
	}
	if merge {
		// Merge routing rules, model overrides, privacy patterns, and alerts while preserving local keys & accounts.
		for _, r := range incoming.Routing {
			found := false
			for i := range cur.Routing {
				if cur.Routing[i].Pattern == r.Pattern && cur.Routing[i].Project == r.Project {
					cur.Routing[i] = r
					found = true
					break
				}
			}
			if !found {
				cur.Routing = append(cur.Routing, r)
			}
		}
		for _, m := range incoming.Models {
			found := false
			for i := range cur.Models {
				if cur.Models[i].ID == m.ID {
					cur.Models[i] = m
					found = true
					break
				}
			}
			if !found {
				cur.Models = append(cur.Models, m)
			}
		}
		if incoming.Privacy.Enabled {
			cur.Privacy = incoming.Privacy
		}
		if incoming.Alerts.Enabled {
			cur.Alerts = incoming.Alerts
		}
	} else {
		// Replace non-secret settings while keeping existing local key and client API keys.
		incoming.Server.LocalKey = cur.Server.LocalKey
		incoming.Server.APIKeys = cur.Server.APIKeys
		cur = &incoming
	}
	if err := config.Save(dataDir, cur); err != nil {
		return err
	}
	gateway.NotifyRunningDaemon(cur)
	fmt.Printf("Imported config from %s (secrets preserved).\n", inPath)
	return nil
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// cmdRoutingExplain prints which account a model would go to and why, using
// the running service's live quota and health, without sending a request.
func cmdRoutingExplain(args []string) error {
	asJSON := false
	model, session := "", ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			asJSON = true
		case "--session", "-s":
			if i+1 < len(args) {
				i++
				session = args[i]
			}
		default:
			if !strings.HasPrefix(args[i], "-") && model == "" {
				model = args[i]
			}
		}
	}
	if model == "" {
		return fmt.Errorf("usage: apx routing explain <model> [--session <key>] [--json]")
	}

	dataDir, cfg, sec, reg, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()
	srv := gateway.New(dataDir, cfg, sec, reg, logs)
	attached, _ := srv.AttachIfRunning()
	if !attached {
		defer srv.ACP().Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ex, err := srv.ExplainRoute(ctx, model, session)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(ex)
	}

	if ex.Error != "" {
		return fmt.Errorf("%s: %s", model, ex.Error)
	}
	src := "live service"
	if !attached {
		src = "local config only; start apx for live quota and health"
	}
	fmt.Printf("%s -> %s on %s (%s)\n", ex.RequestedModel, ex.UpstreamModel, ex.Backend, src)
	rule := ex.Rule
	if rule == "" {
		rule = "(none)"
	}
	fmt.Printf("  rule: %s, strategy: %s", rule, ex.Pool.Strategy)
	if ex.Pool.QuotaGroup != "" {
		fmt.Printf(", quota group: %s", ex.Pool.QuotaGroup)
	}
	fmt.Println()
	if session != "" {
		if ex.Pool.PinnedTo != "" {
			fmt.Printf("  conversation is on: %s\n", ex.Pool.PinnedTo)
		} else {
			fmt.Println("  conversation: new (no account yet)")
		}
	}
	fmt.Printf("  %-24s %-14s %-6s %s\n", "ACCOUNT", "STATUS", "USED", "REASON")
	for _, c := range ex.Pool.Candidates {
		used := "—"
		if c.QuotaUsedPct != nil {
			used = fmt.Sprintf("%.0f%%", *c.QuotaUsedPct)
		}
		mark := " "
		if c.AccountID == ex.Pool.ChosenID {
			mark = "*"
		}
		fmt.Printf("%s %-24s %-14s %-6s %s\n", mark, c.Account, c.Status, used, c.Reason)
	}
	if ex.Pool.Error != "" {
		fmt.Printf("  result: refused — %s\n", ex.Pool.Error)
		return nil
	}
	fmt.Printf("  result: %s — %s\n", ex.Pool.Chosen, ex.Pool.Why)
	return nil
}
