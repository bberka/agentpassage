package router_test

import (
	"reflect"
	"testing"

	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/router"
)

func TestGroupEffortVariantsAndResolve(t *testing.T) {
	raw := []string{
		"gemini-4-neon",
		"gemini-4-neon-low",
		"gemini-4-neon-medium",
		"gemini-4-neon-high",
		"gemini-4-neon-xhigh",
		"gpt-oss-120b-medium", // standalone model with no base or siblings -> must NOT collapse
		"gpt-6-luna",
		"gpt-6-luna[low]",
		"gpt-6-luna[high]",
		"gpt-6-luna[max]",
	}

	grouped, efforts := router.GroupEffortVariants(raw)
	wantGrouped := []string{"gemini-4-neon", "gpt-oss-120b-medium", "gpt-6-luna"}
	if !reflect.DeepEqual(grouped, wantGrouped) {
		t.Fatalf("grouped mismatch:\ngot:  %v\nwant: %v", grouped, wantGrouped)
	}
	if !reflect.DeepEqual(efforts["gemini-4-neon"], []string{"low", "medium", "high", "xhigh"}) {
		t.Fatalf("unexpected gemini efforts: %v", efforts["gemini-4-neon"])
	}
	if !reflect.DeepEqual(efforts["gpt-6-luna"], []string{"low", "high", "max"}) {
		t.Fatalf("unexpected gpt-6-luna efforts: %v", efforts["gpt-6-luna"])
	}
	if _, ok := efforts["gpt-oss-120b"]; ok {
		t.Fatalf("gpt-oss-120b-medium should not have been collapsed")
	}

	agyAcct := config.AccountConfig{
		ID:      "agy-bb",
		Name:    "bb",
		Backend: "agy",
		Kind:    config.KindACP,
		Enabled: true,
		Models:  []string{"gemini-4-neon", "gemini-4-neon-low", "gemini-4-neon-high", "gemini-4-neon-xhigh"},
	}
	codexAcct := config.AccountConfig{
		ID:      "codex-bb",
		Name:    "bb",
		Backend: "codex",
		Kind:    config.KindACP,
		Enabled: true,
		Models:  []string{"gpt-6-luna", "gpt-6-luna[low]", "gpt-6-luna[high]", "gpt-6-luna[max]"},
	}

	// ResolveAccountEffortModel maps (base, effort) -> exact upstream variant
	if got := router.ResolveAccountEffortModel(agyAcct, "gemini-4-neon", "high"); got != "gemini-4-neon-high" {
		t.Fatalf("expected gemini-4-neon-high, got %q", got)
	}
	if got := router.ResolveAccountEffortModel(codexAcct, "gpt-6-luna", "max"); got != "gpt-6-luna[max]" {
		t.Fatalf("expected gpt-6-luna[max], got %q", got)
	}
	if got := router.ResolveAccountEffortModel(codexAcct, "gpt-6-luna", "default"); got != "gpt-6-luna" {
		t.Fatalf("expected base gpt-6-luna for default effort, got %q", got)
	}

	// ListModels collapses variants by default and preserves efforts metadata
	cfg := config.Default()
	cfg.Catalog.HideBackendQualified = true
	cfg.Accounts = []config.AccountConfig{agyAcct, codexAcct}
	r := router.New(cfg)
	list := r.ListModels(false)
	if len(list) != 2 {
		t.Fatalf("expected 2 collapsed base models, got %d: %+v", len(list), list)
	}
	for _, m := range list {
		if m.ID == "gemini-4-neon" && !reflect.DeepEqual(m.Efforts, []string{"low", "high", "xhigh"}) {
			t.Fatalf("expected efforts on gemini-4-neon, got %v", m.Efforts)
		}
		if m.ID == "gpt-6-luna" && !reflect.DeepEqual(m.Efforts, []string{"low", "high", "max"}) {
			t.Fatalf("expected efforts on gpt-6-luna, got %v", m.Efforts)
		}
	}

	// BudgetToEffort helper
	if got := router.BudgetToEffort(1024); got != "low" {
		t.Fatalf("expected low, got %q", got)
	}
	if got := router.BudgetToEffort(16000); got != "high" {
		t.Fatalf("expected high, got %q", got)
	}
}
