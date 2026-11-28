package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/gateway"
	"github.com/bberka/agentpassage/internal/logbuf"
	"github.com/bberka/agentpassage/internal/secrets"
	"github.com/bberka/agentpassage/internal/sharing"
)

func TestNewMCPServerIsReviewedOnStart(t *testing.T) {
	home, dir := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	if err := sharing.Save(dir, sharing.Choices{}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"mcpServers":{"browse":{"command":"node"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.UI.OnboardingComplete = true
	cfg.UI.HostAuthPromptDismissed = true
	cfg.Accounts = []config.AccountConfig{{ID: "claude-a", Name: "a", Kind: config.KindACP, Backend: "claude", Enabled: true}}
	sec := secrets.NewFileOnly(dir)
	reg, _ := descriptors.LoadRegistry(dir)
	logs := logbuf.New(20, dir, sec.KnownSecrets)
	defer logs.Close()
	app := New(gateway.New(dir, cfg, sec, reg, logs))
	defer app.Close()
	app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	view := app.View()
	if app.modal != modalTools || !strings.Contains(view, "1 new MCP server to review") || !strings.Contains(view, "[ ] MCP   browse") || !strings.Contains(view, "on/off") {
		t.Fatalf("modal %v:\n%s", app.modal, view)
	}
	app.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")})
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, items, err := app.srv.SharedTools("")
	if err != nil || app.modal != modalNone || len(items) != 1 || items[0].State != sharing.On {
		t.Fatalf("modal %v, items %+v, %v", app.modal, items, err)
	}
}
