package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/secrets"
)

const settingsCount = 6

func (a *App) updateSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cfg := a.srv.Config()
	switch msg.String() {
	case "up", "k":
		if a.settingCursor > 0 {
			a.settingCursor--
		}
	case "down", "j":
		if a.settingCursor < settingsCount-1 {
			a.settingCursor++
		}
	case "v":
		a.revealKey = !a.revealKey
	case "c":
		a.copyToClipboard("local API key", cfg.Server.LocalKey)
	case "g":
		cfg.Server.LocalKey = config.GenerateLocalKey()
		_ = a.srv.ReloadConfig(cfg)
		a.setStatus("Rotated local API key (clients must use the new key)", false)
	case "enter", " ":
		switch a.settingCursor {
		case 0:
			return a, a.openEditModal("Server Port", "TCP port (1-65535); restart required to rebind", strconv.Itoa(cfg.Server.Port), func(val string) error {
				p, err := strconv.Atoi(val)
				if err != nil || p < 1 || p > 65535 {
					return fmt.Errorf("invalid port %q", val)
				}
				cfg := a.srv.Config()
				cfg.Server.Port = p
				_ = a.srv.ReloadConfig(cfg)
				a.setStatus(fmt.Sprintf("Saved port %d (restart acpx to apply)", p), false)
				return nil
			})
		case 1:
			return a, a.openEditModal("Bind Address", "Default 127.0.0.1 (non-loopback requires --allow-remote)", cfg.Server.Bind, func(val string) error {
				if val == "" {
					val = config.DefaultBind
				}
				cfg := a.srv.Config()
				cfg.Server.Bind = val
				_ = a.srv.ReloadConfig(cfg)
				a.setStatus("Saved bind address "+val+" (restart acpx to apply)", false)
				return nil
			})
		case 2:
			a.copyToClipboard("local API key", cfg.Server.LocalKey)
		case 3:
			themes := []string{"auto", "dark", "light", "transparent", "ansi16"}
			next := themes[0]
			for i, t := range themes {
				if t == cfg.UI.Theme {
					next = themes[(i+1)%len(themes)]
					break
				}
			}
			cfg.UI.Theme = next
			a.styles = NewStyles(next)
			_ = a.srv.ReloadConfig(cfg)
			a.setStatus("Theme set to "+next, false)
		case 4:
			if cfg.ACP.ToolActivity == config.ToolActivityDrop {
				cfg.ACP.ToolActivity = config.ToolActivityText
			} else {
				cfg.ACP.ToolActivity = config.ToolActivityDrop
			}
			_ = a.srv.ReloadConfig(cfg)
			a.setStatus("ACP tool activity streaming: "+cfg.ACP.ToolActivity, false)
		case 5:
			a.copyToClipboard("data directory", a.srv.DataDir())
		}
	}
	return a, nil
}

func (a *App) viewSettings(bodyHeight int) string {
	cfg := a.srv.Config()
	fullW := max(a.width-2, 40)
	innerW := fullW - 4

	keyDisp := secrets.Mask(cfg.Server.LocalKey)
	if a.revealKey {
		keyDisp = cfg.Server.LocalKey
	}

	rows := []struct {
		label string
		value string
	}{
		{"Port", strconv.Itoa(cfg.Server.Port)},
		{"Bind Address", cfg.Server.Bind},
		{"Local Key", keyDisp + "   (v:reveal  c:copy  g:rotate)"},
		{"Theme", cfg.UI.Theme + "   (enter to cycle: auto / dark / light / transparent / ansi16)"},
		{"ACP Tool Activity", cfg.ACP.ToolActivity + "   (enter to toggle: drop / text)"},
		{"Data Directory", a.srv.DataDir() + "   (secrets: " + a.srv.Secrets().Backend() + ")"},
	}

	var lines []string
	lines = append(lines, a.styles.Title.Render("Settings")+"   "+a.styles.Muted.Render("enter:edit/toggle  v:reveal key  g:rotate key  C:connect CLIs"))
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
	lines = append(lines, "")
	for i, r := range rows {
		line := fmt.Sprintf("%-20s %s", r.label, truncate(r.value, max(innerW-24, 16)))
		if i == a.settingCursor {
			lines = append(lines, a.styles.Selected.Width(innerW).Render("> "+line))
		} else {
			lines = append(lines, "  "+line)
		}
	}
	return a.styles.Card.Width(fullW).Height(bodyHeight - 2).Render(strings.Join(lines, "\n"))
}
