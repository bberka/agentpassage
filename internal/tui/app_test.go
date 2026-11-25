package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/descriptors"
	"github.com/bberka/acpx/internal/gateway"
	"github.com/bberka/acpx/internal/logbuf"
	"github.com/bberka/acpx/internal/secrets"
)

func TestTUIRendersAllScreensAt80x24(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.UI.OnboardingComplete = true
	cfg.Accounts = []config.AccountConfig{
		{ID: "agy-work", Name: "work", Kind: config.KindACP, Backend: "agy", Enabled: true, Models: []string{"gemini-3.1-pro"}},
		{ID: "deepseek-main", Name: "main", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1", Models: []string{"deepseek-chat"}},
	}
	cfg.Routing = []config.RoutingRule{
		{Pattern: "agy/*", Strategy: config.StrategySticky},
	}

	sec := secrets.NewFileOnly(dir)
	reg, _ := descriptors.LoadRegistry(dir)
	logs := logbuf.New(50, dir, sec.KnownSecrets)
	defer logs.Close()

	srv := gateway.New(dir, cfg, sec, reg, logs)
	app := New(srv)
	defer app.Close()

	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	for idx, key := range []string{"1", "2", "3", "4", "5", "6"} {
		app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
		out := app.View()
		if len(strings.TrimSpace(out)) == 0 {
			t.Fatalf("screen %d rendered empty output", idx)
		}
	}

	// Open help modal and ensure it renders
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("?")})
	if !strings.Contains(app.View(), "Keyboard Shortcuts") {
		t.Fatalf("expected help modal")
	}

	// Close help, go to Routing (4), cycle strategy until subscription warning triggers
	app.Update(tea.KeyMsg{Type: tea.KeyEsc})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	// sticky -> manual -> failover -> round-robin (warning!)
	for range 3 {
		app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	}
	if app.modal != modalSubStrategyWarn {
		t.Fatalf("expected subscription strategy warning modal, got modal=%v", app.modal)
	}
	if !strings.Contains(app.View(), "Subscription Pool Warning") {
		t.Fatalf("expected subscription warning text in view")
	}
}
