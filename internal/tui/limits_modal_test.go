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

func TestLimitsModalSavesAccountLimits(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.UI.OnboardingComplete = true
	cfg.UI.HostAuthPromptDismissed = true
	cfg.Limits.Weekly = 20
	cfg.Accounts = []config.AccountConfig{{ID: "claude-a", Name: "a", Kind: config.KindACP, Backend: "claude", Enabled: true}}
	sec := secrets.NewFileOnly(dir)
	reg, _ := descriptors.LoadRegistry(dir)
	logs := logbuf.New(20, dir, sec.KnownSecrets)
	defer logs.Close()
	app := New(gateway.New(dir, cfg, sec, reg, logs))
	defer app.Close()
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	press := func(keys ...tea.KeyMsg) {
		for _, k := range keys {
			app.Update(k)
		}
	}
	runes := func(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
	tab := tea.KeyMsg{Type: tea.KeyTab}

	press(runes("2"), runes("L"))
	view := app.View()
	if app.modal != modalLimits || !strings.Contains(view, "default (20)") || !strings.Contains(view, "allow more") {
		t.Fatalf("modal %v:\n%s", app.modal, view)
	}
	// Period 1d, weekly off, monthly 5.
	press(tea.KeyMsg{Type: tea.KeyRight}, tab, runes("off"), tab, runes("5"), tea.KeyMsg{Type: tea.KeyEnter})
	got := app.srv.Config().Accounts[0].Limits
	if app.modal != modalNone || got == nil || *got != (config.LimitsConfig{Every: "1d", Weekly: -1, Monthly: 5}) {
		t.Fatalf("modal %v, limits %+v", app.modal, got)
	}
	saved, _, err := config.Load(dir)
	if err != nil || saved.Limits.Weekly != 20 || saved.Accounts[0].Limits == nil || *saved.Accounts[0].Limits != *got {
		t.Fatalf("saved %+v, %v", saved.Accounts[0].Limits, err)
	}
}
