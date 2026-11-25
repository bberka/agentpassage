package router

import (
	"testing"

	"github.com/bberka/acpx/internal/config"
)

func TestModelNamespaceAliasesAndManualRouting(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts = []config.AccountConfig{
		{ID: "agy-work", Name: "work", Kind: config.KindACP, Backend: "agy", Enabled: true, Models: []string{"gemini-3.1-pro"}},
		{ID: "agy-personal", Name: "personal", Kind: config.KindACP, Backend: "agy", Enabled: true, Models: []string{"gemini-3.1-pro"}},
		{ID: "deepseek-main", Name: "main", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, Models: []string{"deepseek-chat"}},
	}
	cfg.Models = []config.ModelOverride{
		{ID: "agy/work/gemini-3.1-pro", Alias: "work-pro"},
		{ID: "deepseek/main/deepseek-chat", Hidden: true},
	}

	r := New(cfg)
	models := r.ListModels(false)
	foundAlias := false
	for _, m := range models {
		if m.ID == "deepseek/main/deepseek-chat" {
			t.Fatalf("expected hidden model excluded from ListModels(false)")
		}
		if m.ID == "work-pro" {
			foundAlias = true
		}
	}
	if !foundAlias {
		t.Fatalf("expected alias work-pro in model list")
	}

	// Manual targeting
	plan, err := r.Resolve("agy/work/gemini-3.1-pro")
	if err != nil {
		t.Fatalf("Resolve manual failed: %v", err)
	}
	if !plan.IsManual || len(plan.CandidateIDs) != 1 || plan.CandidateIDs[0] != "agy-work" || plan.UpstreamModel != "gemini-3.1-pro" {
		t.Fatalf("unexpected manual plan: %+v", plan)
	}

	// Alias resolution
	aliasPlan, err := r.Resolve("work-pro")
	if err != nil {
		t.Fatalf("Resolve alias failed: %v", err)
	}
	if aliasPlan.CandidateIDs[0] != "agy-work" || aliasPlan.UpstreamModel != "gemini-3.1-pro" {
		t.Fatalf("unexpected alias plan: %+v", aliasPlan)
	}

	// Pooled resolution
	poolPlan, err := r.Resolve("agy/gemini-3.1-pro")
	if err != nil {
		t.Fatalf("Resolve pooled failed: %v", err)
	}
	if len(poolPlan.CandidateIDs) != 2 {
		t.Fatalf("expected 2 candidates for pooled agy/gemini-3.1-pro, got %v", poolPlan.CandidateIDs)
	}
}
