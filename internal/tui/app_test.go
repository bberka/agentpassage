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

func TestTUIFullScreenScreensAndModals(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.UI.OnboardingComplete = true
	cfg.Accounts = []config.AccountConfig{
		{ID: "agy-a", Name: "a", Kind: config.KindACP, Backend: "agy", Enabled: true, ExposeMode: config.ExposeBoth, ModelPrefix: "a-", Models: []string{"gemini-4-neon"}},
		{ID: "deepseek-main", Name: "main", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1", Models: []string{"deepseek-chat"}},
	}
	cfg.Routing = []config.RoutingRule{
		{Pattern: "agy/*", Strategy: config.StrategyFailover, AllowCrossAccountRetryOn429: true},
	}

	sec := secrets.NewFileOnly(dir)
	reg, _ := descriptors.LoadRegistry(dir)
	logs := logbuf.New(50, dir, sec.KnownSecrets)
	defer logs.Close()

	srv := gateway.New(dir, cfg, sec, reg, logs)
	app := New(srv)
	defer app.Close()

	// Test both compact 80x24 and wide 120x36 full-screen layouts
	for _, sz := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 120, Height: 36}} {
		app.Update(sz)
		for idx, key := range []string{"1", "2", "3", "4", "5", "6", "7"} {
			app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
			out := app.View()
			if len(strings.TrimSpace(out)) == 0 {
				t.Fatalf("size %dx%d screen %d rendered empty output", sz.Width, sz.Height, idx)
			}
		}
	}

	// Open ACP Registry & Installer modal (i)
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	if app.modal != modalWelcome || !strings.Contains(app.View(), "ACP Registry") {
		t.Fatalf("expected ACP Registry modal on 'i'")
	}
	app.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// Open Connect CLIs & T3 Code modal (C)
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("C")})
	if app.modal != modalConnect || !strings.Contains(app.View(), "T3 Code") {
		t.Fatalf("expected Connect modal with T3 Code on 'C'")
	}
	app.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// Open Background Service & Auto-Start modal (S)
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	if app.modal != modalService || !strings.Contains(app.View(), "Background Service & Auto-Start") {
		t.Fatalf("expected Service modal on 'S', got modal=%v", app.modal)
	}
	app.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// Go to Routing (4), press 's' to cycle failover -> sticky -> manual -> failover (no round-robin/warning modal).
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	for _, want := range []string{config.StrategySticky, config.StrategyManual, config.StrategyFailover} {
		app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
		if app.modal != modalNone {
			t.Fatalf("expected no modal while cycling strategies, got modal=%v", app.modal)
		}
		if got := app.srv.Config().Routing[0].Strategy; got != want {
			t.Fatalf("expected strategy %q, got %q", want, got)
		}
	}

	// Open Accounts (2) -> e opens structured Account Editor modal (not comma-separated model text!)
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if app.modal != modalEditAccount || !strings.Contains(app.View(), "Edit Account") {
		t.Fatalf("expected Account Editor modal on 'e', got modal=%v", app.modal)
	}
	app.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// Safe rename moves profile dir & updates routing rules
	if _, err := config.EnsureProfileDir(dir, "agy", "a"); err != nil {
		t.Fatal(err)
	}
	if err := app.renameAccount("agy-a", "work"); err != nil {
		t.Fatalf("renameAccount failed: %v", err)
	}
	if app.srv.Config().FindAccount("agy-work") == nil {
		t.Fatalf("expected renamed account agy-work in config")
	}

	// Accounts -> M opens interactive Account Models Picker; Space toggles, Enter saves
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("M")})
	if app.modal != modalAccountModels || !strings.Contains(app.View(), "Models —") {
		t.Fatalf("expected Account Models Picker modal on 'M', got modal=%v", app.modal)
	}
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if app.modal != modalNone {
		t.Fatalf("expected modal closed after Enter")
	}
}
