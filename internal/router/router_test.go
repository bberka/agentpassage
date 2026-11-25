package router

import (
	"testing"

	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/pool"
)

func TestModelNamespaceAliasesAndManualRouting(t *testing.T) {
	cfg := config.Default()
	cfg.Catalog.ExposeDirectModels = true
	cfg.Accounts = []config.AccountConfig{
		{ID: "agy-work", Name: "work", Kind: config.KindACP, Backend: "agy", Enabled: true, ExposeMode: config.ExposePool, Models: []string{"gemini-3.1-pro"}},
		{ID: "agy-personal", Name: "personal", Kind: config.KindACP, Backend: "agy", Enabled: true, ExposeMode: config.ExposePool, Models: []string{"gemini-3.1-pro"}},
		{ID: "deepseek-main", Name: "main", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, ExposeMode: config.ExposePool, Models: []string{"deepseek-chat"}},
	}
	cfg.Models = []config.ModelOverride{
		{ID: "agy/work/gemini-3.1-pro", Alias: "work-pro"},
		{ID: "deepseek/main/deepseek-chat", Hidden: true},
	}

	r := New(cfg)
	models := r.ListModels(false)
	foundAlias := false
	foundBare := false
	for _, m := range models {
		if m.ID == "deepseek/main/deepseek-chat" {
			t.Fatalf("expected hidden model excluded from ListModels(false)")
		}
		if m.ID == "work-pro" {
			foundAlias = true
		}
		if m.ID == "gemini-3.1-pro" {
			foundBare = true
		}
	}
	if !foundAlias {
		t.Fatalf("expected alias work-pro in model list")
	}
	if !foundBare {
		t.Fatalf("expected pooled bare model gemini-3.1-pro in model list")
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

	// Default catalog (ExposeDirectModels=false) hides <backend>/<account>/<model> from ListModels,
	// while still allowing manual resolution if requested explicitly.
	cfgClean := config.Default()
	cfgClean.Accounts = cfg.Accounts
	rClean := New(cfgClean)
	for _, m := range rClean.ListModels(false) {
		if m.ID == "agy/work/gemini-3.1-pro" || m.ID == "agy/personal/gemini-3.1-pro" {
			t.Fatalf("expected account-specific direct model hidden by default, got %s", m.ID)
		}
	}
	if _, err := rClean.Resolve("agy/work/gemini-3.1-pro"); err != nil {
		t.Fatalf("explicit manual Resolve should still work when direct models are hidden: %v", err)
	}

	// HideBackendQualified keeps only bare router models (e.g. "gemini-3.1-pro")
	cfgClean.Catalog.HideBackendQualified = true
	for _, m := range rClean.ListModels(false) {
		if m.ID == "agy/gemini-3.1-pro" {
			t.Fatalf("expected backend-qualified duplicate hidden when HideBackendQualified=true")
		}
	}
}

func TestPrefixedModelRegistrationAndThreadRoundRobin(t *testing.T) {
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

	// Case 2: Pooled mode with round-robin rotating different threads across accounts a and b
	cfgPool := config.Default()
	cfgPool.Accounts = []config.AccountConfig{
		{ID: "agy-a", Name: "a", Kind: config.KindACP, Backend: "agy", Enabled: true, ExposeMode: config.ExposePool, Models: []string{"gemini-4-neon"}, MaxConcurrency: 1},
		{ID: "agy-b", Name: "b", Kind: config.KindACP, Backend: "agy", Enabled: true, ExposeMode: config.ExposePool, Models: []string{"gemini-4-neon"}, MaxConcurrency: 1},
	}
	cfgPool.Routing = []config.RoutingRule{
		{Pattern: "agy/*", Strategy: config.StrategyRoundRobin, SubscriptionWarningAcked: true},
	}
	rPool := New(cfgPool)
	plan, err := rPool.Resolve("gemini-4-neon")
	if err != nil {
		t.Fatalf("resolve bare gemini-4-neon failed: %v", err)
	}
	if plan.Rule.Strategy != config.StrategyRoundRobin || len(plan.CandidateIDs) != 2 {
		t.Fatalf("expected round-robin across 2 accounts, got %+v", plan)
	}

	pm := pool.NewManager(cfgPool.Accounts)
	l1, err := pm.Acquire(pool.SelectRequest{RuleKey: plan.Rule.Pattern, Strategy: plan.Rule.Strategy, CandidateIDs: plan.CandidateIDs, SessionKey: "thread-1", SubscriptionWarningAcked: true})
	if err != nil {
		t.Fatal(err)
	}
	l1.Release()

	l2, err := pm.Acquire(pool.SelectRequest{RuleKey: plan.Rule.Pattern, Strategy: plan.Rule.Strategy, CandidateIDs: plan.CandidateIDs, SessionKey: "thread-2", SubscriptionWarningAcked: true})
	if err != nil {
		t.Fatal(err)
	}
	l2.Release()

	if l1.Account.ID == l2.Account.ID {
		t.Fatalf("expected thread-1 and thread-2 to rotate across accounts, both got %s", l1.Account.ID)
	}

	// Follow-up in thread-1 stays pinned to thread-1's account!
	l3, err := pm.Acquire(pool.SelectRequest{RuleKey: plan.Rule.Pattern, Strategy: plan.Rule.Strategy, CandidateIDs: plan.CandidateIDs, SessionKey: "thread-1", SubscriptionWarningAcked: true})
	if err != nil {
		t.Fatal(err)
	}
	l3.Release()
	if l3.Account.ID != l1.Account.ID {
		t.Fatalf("expected follow-up in thread-1 to stay on %s, got %s", l1.Account.ID, l3.Account.ID)
	}
}
