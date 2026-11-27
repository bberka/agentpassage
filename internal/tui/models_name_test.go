package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/gateway"
	"github.com/bberka/agentpassage/internal/logbuf"
	"github.com/bberka/agentpassage/internal/secrets"
)

func TestModelsTabShowsAndRenamesModelNames(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.UI.OnboardingComplete = true
	cfg.UI.HostAuthPromptDismissed = true
	cfg.Accounts = []config.AccountConfig{{
		ID: "claude-main", Name: "main", Kind: config.KindACP, Backend: "claude", Enabled: true,
		Models: []string{"opus"}, ModelLabels: map[string]string{"opus": "Opus 5.5"},
	}}
	sec := secrets.NewFileOnly(dir)
	reg, _ := descriptors.LoadRegistry(dir)
	logs := logbuf.New(50, dir, sec.KnownSecrets)
	defer logs.Close()
	app := New(gateway.New(dir, cfg, sec, reg, logs))
	defer app.Close()

	press := func(k tea.KeyMsg) { app.Update(k) }
	runes := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
	app.Update(tea.WindowSizeMsg{Width: 140, Height: 36})
	press(runes("3"))
	if !strings.Contains(app.View(), "Claude Opus 5.5") {
		t.Fatalf("Models tab does not show the client-facing name:\n%s", app.View())
	}

	// Saving the prefilled name unchanged stores no override.
	press(runes("r"))
	press(tea.KeyMsg{Type: tea.KeyEnter})
	if got := app.srv.Config().Models; len(got) != 0 {
		t.Fatalf("unchanged rename stored an override: %+v", got)
	}

	press(runes("r"))
	app.editInput.SetValue("Big Brain")
	press(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(app.View(), "Big Brain") || !strings.Contains(app.View(), "renamed") {
		t.Fatalf("Models tab does not show the new name:\n%s", app.View())
	}

	// A blank name returns to the automatic one.
	press(runes("r"))
	app.editInput.SetValue("")
	press(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(app.View(), "Claude Opus 5.5") {
		t.Fatalf("blank name did not restore the automatic one:\n%s", app.View())
	}
}
