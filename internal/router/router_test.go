package router

import (
	"testing"

	"github.com/bberka/agentpassage/internal/config"
)

func TestModelNamespaceAliasesAndManualRouting(t *testing.T) {
	cfg := config.Default()
	cfg.Catalog.Mode = config.CatalogModeProviderAccount
	cfg.Accounts = []config.AccountConfig{
		{ID: "agy-work", Name: "work", Kind: config.KindACP, Backend: "agy", Enabled: true, ExposeMode: config.ExposePool, Models: []string{"gemini-3.1-pro", "claude-sonnet-5-5"}},
		{ID: "agy-personal", Name: "personal", Kind: config.KindACP, Backend: "agy", Enabled: true, ExposeMode: config.ExposePool, Models: []string{"gemini-3.1-pro"}},
		{ID: "claude-main", Name: "main", Kind: config.KindACP, Backend: "claude", Enabled: true, ExposeMode: config.ExposePool, Models: []string{"claude-sonnet-5-5", "fable"}, ModelLabels: map[string]string{"fable": "Fable 5.2"}},
		{ID: "deepseek-main", Name: "main", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, ExposeMode: config.ExposePool, Models: []string{"deepseek-chat"}},
	}
	cfg.Models = []config.ModelOverride{
		{ID: "agy/work/gemini-3.1-pro", Alias: "work-pro"},
		{ID: "deepseek/main/deepseek-chat", Hidden: true},
	}

	r := New(cfg)
	models := r.ListModels(false)
	ids := make(map[string]ModelEntry, len(models))
	for _, m := range models {
		if m.ID == "deepseek/main/deepseek-chat" {
			t.Fatalf("expected hidden model excluded from ListModels(false)")
		}
		ids[m.ID] = m
	}
	if _, ok := ids["work-pro"]; !ok {
		t.Fatalf("expected alias work-pro in model list")
	}
	// Bare/trimmed models must NOT appear in provider or provider+account mode
	for _, bare := range []string{"gemini-3.1-pro", "fable", "claude-sonnet-5-5", "deepseek-chat"} {
		if _, ok := ids[bare]; ok {
			t.Fatalf("unexpected bare trimmed model %q in %s mode", bare, cfg.Catalog.Mode)
		}
	}
	// Same model served by Google (agy) and Claude must NEVER merge
	if _, ok := ids["agy/claude-sonnet-5-5"]; !ok {
		t.Fatalf("expected agy/claude-sonnet-5-5 separate from claude")
	}
	if _, ok := ids["claude/claude-sonnet-5-5"]; !ok {
		t.Fatalf("expected claude/claude-sonnet-5-5 separate from agy")
	}
	// Upstream ModelLabels must populate DisplayName (e.g. fable -> Fable 5.2)
	if got := ids["claude/fable"].DisplayName; got != "Fable 5.2" {
		t.Fatalf("expected claude/fable DisplayName='Fable 5.2', got %q", got)
	}
	if got := ids["claude/main/fable"].DisplayName; got != "Fable 5.2" {
		t.Fatalf("expected claude/main/fable DisplayName='Fable 5.2', got %q", got)
	}

	plan, err := r.Resolve("agy/work/gemini-3.1-pro")
	if err != nil {
		t.Fatalf("Resolve manual failed: %v", err)
	}
	if !plan.IsManual || len(plan.CandidateIDs) != 1 || plan.CandidateIDs[0] != "agy-work" || plan.UpstreamModel != "gemini-3.1-pro" {
		t.Fatalf("unexpected manual plan: %+v", plan)
	}

	aliasPlan, err := r.Resolve("work-pro")
	if err != nil {
		t.Fatalf("Resolve alias failed: %v", err)
	}
	if aliasPlan.CandidateIDs[0] != "agy-work" || aliasPlan.UpstreamModel != "gemini-3.1-pro" {
		t.Fatalf("unexpected alias plan: %+v", aliasPlan)
	}

	poolPlan, err := r.Resolve("agy/gemini-3.1-pro")
	if err != nil {
		t.Fatalf("Resolve pooled failed: %v", err)
	}
	if len(poolPlan.CandidateIDs) != 2 {
		t.Fatalf("expected 2 candidates for pooled agy/gemini-3.1-pro, got %v", poolPlan.CandidateIDs)
	}

	// Default catalog (Provider mode) exposes only <provider>/<model> (no bare, no per-account),
	// while explicit manual Resolve still works.
	cfgProv := config.Default()
	cfgProv.Accounts = cfg.Accounts
	rProv := New(cfgProv)
	for _, m := range rProv.ListModels(false) {
		if m.ID == "agy/work/gemini-3.1-pro" || m.ID == "gemini-3.1-pro" || m.ID == "fable" {
			t.Fatalf("expected provider-only mode to hide %q", m.ID)
		}
	}
	if _, err := rProv.Resolve("agy/work/gemini-3.1-pro"); err != nil {
		t.Fatalf("explicit manual Resolve should still work in provider mode: %v", err)
	}

	// Per-Account mode exposes ONLY <provider>/<account>/<model> (no pooled, no bare)
	cfgAcct := config.Default()
	cfgAcct.Catalog.Mode = config.CatalogModeAccount
	cfgAcct.Accounts = cfg.Accounts
	rAcct := New(cfgAcct)
	acctIDs := map[string]bool{}
	for _, m := range rAcct.ListModels(false) {
		acctIDs[m.ID] = true
		if m.ID == "agy/gemini-3.1-pro" || m.ID == "gemini-3.1-pro" || m.ID == "claude/fable" {
			t.Fatalf("expected account-only mode to hide %q", m.ID)
		}
	}
	if !acctIDs["agy/work/gemini-3.1-pro"] || !acctIDs["agy/personal/gemini-3.1-pro"] || !acctIDs["claude/main/fable"] {
		t.Fatalf("expected per-account models in account mode, got %+v", acctIDs)
	}

	// Legacy HideBackendQualified maps to Bare mode
	cfgBare := config.Default()
	cfgBare.Accounts = cfg.Accounts
	cfgBare.Catalog.HideBackendQualified = true
	rBare := New(cfgBare)
	for _, m := range rBare.ListModels(false) {
		if m.ID == "agy/gemini-3.1-pro" {
			t.Fatalf("expected backend-qualified duplicate hidden when HideBackendQualified=true")
		}
	}
}

func TestPrefixedModelRegistrationAndPoolDefaults(t *testing.T) {
	// Case 1: Account prefix mode ("a-gemini-4-neon", "b-gemini-4-neon")
	cfgPrefix := config.Default()
	cfgPrefix.Accounts = []config.AccountConfig{
		{ID: "agy-a", Name: "a", Kind: config.KindACP, Backend: "agy", Enabled: true, ExposeMode: config.ExposePrefix, ModelPrefix: "a-", Models: []string{"gemini-4-neon"}, MaxConcurrency: 1},
		{ID: "agy-b", Name: "b", Kind: config.KindACP, Backend: "agy", Enabled: true, ExposeMode: config.ExposePrefix, ModelPrefix: "b-", Models: []string{"gemini-4-neon"}, MaxConcurrency: 1},
	}
	rp := New(cfgPrefix)
	models := rp.ListModels(false)
	ids := map[string]bool{}
	for _, m := range models {
		ids[m.ID] = true
	}
	if !ids["a-gemini-4-neon"] || !ids["b-gemini-4-neon"] {
		t.Fatalf("expected prefixed models a-gemini-4-neon and b-gemini-4-neon, got %+v", models)
	}
	if ids["gemini-4-neon"] {
		t.Fatalf("prefix-only accounts should not expose pooled bare model")
	}

	planA, err := rp.Resolve("a-gemini-4-neon")
	if err != nil || planA.CandidateIDs[0] != "agy-a" || planA.UpstreamModel != "gemini-4-neon" {
		t.Fatalf("unexpected planA: %+v err=%v", planA, err)
	}
	planB, err := rp.Resolve("b-gemini-4-neon")
	if err != nil || planB.CandidateIDs[0] != "agy-b" || planB.UpstreamModel != "gemini-4-neon" {
		t.Fatalf("unexpected planB: %+v err=%v", planB, err)
	}

	// Case 2: Pooled mode defaults to primary-first failover with 429 retry enabled.
	cfgPool := config.Default()
	cfgPool.Accounts = []config.AccountConfig{
		{ID: "agy-a", Name: "a", Kind: config.KindACP, Backend: "agy", Enabled: true, ExposeMode: config.ExposePool, Models: []string{"gemini-4-neon"}, MaxConcurrency: 1},
		{ID: "agy-b", Name: "b", Kind: config.KindACP, Backend: "agy", Enabled: true, ExposeMode: config.ExposePool, Models: []string{"gemini-4-neon"}, MaxConcurrency: 1},
	}
	rPool := New(cfgPool)
	plan, err := rPool.Resolve("gemini-4-neon")
	if err != nil {
		t.Fatalf("resolve bare gemini-4-neon failed: %v", err)
	}
	if plan.Rule.Strategy != config.StrategyFailover || !plan.Rule.AllowCrossAccountRetryOn429 || len(plan.CandidateIDs) != 2 {
		t.Fatalf("expected default primary-first failover with 429 retry across 2 accounts, got %+v", plan)
	}

	// Config.Normalize preserves round-robin/least-busy for API-key pools and defaults empty strategy to failover.
	cfgPool.Routing = []config.RoutingRule{
		{Pattern: "deepseek/*", Strategy: config.StrategyRoundRobin},
		{Pattern: "openrouter/*", Strategy: config.StrategyLeastBusy},
		{Pattern: "agy/*", Strategy: ""},
	}
	cfgPool.Normalize()
	if cfgPool.Routing[0].Strategy != config.StrategyRoundRobin || cfgPool.Routing[1].Strategy != config.StrategyLeastBusy {
		t.Fatalf("expected round-robin/least-busy preserved by Normalize, got %+v", cfgPool.Routing)
	}
	if cfgPool.Routing[2].Strategy != config.StrategyFailover {
		t.Fatalf("expected empty strategy normalized to failover, got %+v", cfgPool.Routing[2])
	}

}

func TestProjectBasedRoutingPriority(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts = []config.AccountConfig{
		{ID: "ds-personal", Name: "personal", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, ExposeMode: config.ExposePool, Models: []string{"deepseek-chat"}},
		{ID: "ds-work", Name: "work", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, ExposeMode: config.ExposePool, Models: []string{"deepseek-chat"}},
	}
	cfg.Routing = []config.RoutingRule{
		{Pattern: "deepseek/*", Accounts: []string{"ds-personal"}, Strategy: config.StrategyFailover},
		{Pattern: "deepseek/*", Project: "work", Accounts: []string{"ds-work"}, Strategy: config.StrategyFailover, FallbackModels: []string{"openai/gpt-4o"}},
	}
	r := New(cfg)

	// Default (no project) matches global rule -> ds-personal
	pGlobal, err := r.ResolveForProject("deepseek/deepseek-chat", "")
	if err != nil || pGlobal.CandidateIDs[0] != "ds-personal" {
		t.Fatalf("expected ds-personal for default project, got %+v err=%v", pGlobal, err)
	}

	// Project "work" matches project-scoped rule -> ds-work with fallback chain
	pWork, err := r.ResolveForProject("deepseek/deepseek-chat", "work")
	if err != nil || pWork.CandidateIDs[0] != "ds-work" || len(pWork.Rule.FallbackModels) != 1 {
		t.Fatalf("expected ds-work with fallback for project 'work', got %+v err=%v", pWork, err)
	}
}

func TestLabelAliasHiddenButRoutable(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts = []config.AccountConfig{
		{ID: "claude-main", Name: "main", Kind: config.KindACP, Backend: "claude", Enabled: true, ExposeMode: config.ExposePool,
			Models:      []string{"claude-fable-5-2", "fable", "opus"},
			ModelLabels: map[string]string{"claude-fable-5-2": "Fable 5.2", "fable": "Fable 5.2", "opus": "Opus 5.5"}},
	}
	r := New(cfg)
	visible := make(map[string]bool)
	for _, m := range r.ListModels(false) {
		visible[m.ID] = true
	}
	if visible["claude/fable"] {
		t.Fatalf("expected duplicate-label alias claude/fable hidden")
	}
	if !visible["claude/claude-fable-5-2"] || !visible["claude/opus"] {
		t.Fatalf("expected full fable id and opus visible, got %v", visible)
	}
	hidden := false
	for _, m := range r.ListModels(true) {
		if m.ID == "claude/fable" && m.Hidden {
			hidden = true
		}
	}
	if !hidden {
		t.Fatalf("expected claude/fable listed as hidden when including hidden models")
	}
	if _, err := r.Resolve("claude/fable"); err != nil {
		t.Fatalf("expected hidden alias to still route: %v", err)
	}
}
