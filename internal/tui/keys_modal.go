package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/secrets"
)

func (a *App) openKeysModal() {
	a.keyCursor = 0
	a.modal = modalAPIKeys
}

func (a *App) updateKeysModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cfg := a.srv.Config()
	total := 1 + len(cfg.Server.APIKeys) // row 0 = primary admin key
	if a.keyCursor >= total {
		a.keyCursor = total - 1
	}

	switch msg.String() {
	case "esc", "q":
		a.modal = modalNone
		return a, nil
	case "up", "k":
		if a.keyCursor > 0 {
			a.keyCursor--
		}
	case "down", "j":
		if a.keyCursor < total-1 {
			a.keyCursor++
		}
	case "v":
		a.revealKey = !a.revealKey
	case "a":
		return a, a.openEditModal("Create API Key", "Device/client name (e.g. laptop, phone, cloudflare-work — default role: client)", "", func(val string) error {
			cfg := a.srv.Config()
			ck, err := cfg.AddAPIKey(val, config.KeyRoleClient, nil)
			if err != nil {
				return err
			}
			_ = a.srv.ReloadConfig(cfg)
			a.copyToClipboard("new API key "+ck.Name, ck.Key)
			a.keyCursor = len(cfg.Server.APIKeys)
			a.modal = modalAPIKeys
			return nil
		})
	case "c":
		if a.keyCursor == 0 {
			a.copyToClipboard("primary admin key", cfg.Server.LocalKey)
		} else {
			ck := cfg.Server.APIKeys[a.keyCursor-1]
			a.copyToClipboard("API key "+ck.Name, ck.Key)
		}
	case "r":
		if a.keyCursor > 0 {
			idx := a.keyCursor - 1
			if cfg.Server.APIKeys[idx].EffectiveRole() == config.KeyRoleAdmin {
				cfg.Server.APIKeys[idx].Role = config.KeyRoleClient
			} else {
				cfg.Server.APIKeys[idx].Role = config.KeyRoleAdmin
			}
			_ = a.srv.ReloadConfig(cfg)
			a.setStatus(fmt.Sprintf("Key %s role -> %s", cfg.Server.APIKeys[idx].Name, cfg.Server.APIKeys[idx].Role), false)
		}
	case "g":
		if a.keyCursor > 0 {
			ck := &cfg.Server.APIKeys[a.keyCursor-1]
			if ck.EffectiveRole() == config.KeyRoleAdmin {
				a.setStatus("Admin keys always may use agents", false)
				break
			}
			ck.Agents = !ck.Agents
			_ = a.srv.ReloadConfig(cfg)
			if ck.Agents {
				a.setStatus(ck.Name+" can now run agents on this machine", true)
			} else {
				a.setStatus(ck.Name+" can no longer use agents", false)
			}
		}
	case "d":
		if a.keyCursor > 0 {
			idx := a.keyCursor - 1
			cfg.Server.APIKeys[idx].Disabled = !cfg.Server.APIKeys[idx].Disabled
			_ = a.srv.ReloadConfig(cfg)
			st := "enabled"
			if cfg.Server.APIKeys[idx].Disabled {
				st = "disabled"
			}
			a.setStatus(fmt.Sprintf("API key %s %s", cfg.Server.APIKeys[idx].Name, st), false)
		}
	case "x", "delete":
		if a.keyCursor == 0 {
			a.setStatus("Primary key cannot be deleted (press 'g' on Settings to rotate it)", true)
		} else {
			name := cfg.Server.APIKeys[a.keyCursor-1].Name
			cfg.DeleteAPIKey(name)
			_ = a.srv.ReloadConfig(cfg)
			if a.keyCursor > len(cfg.Server.APIKeys) {
				a.keyCursor = len(cfg.Server.APIKeys)
			}
			a.setStatus("Revoked API key "+name, false)
		}
	}
	return a, nil
}

func (a *App) viewKeysModal() string {
	cfg := a.srv.Config()
	cardW := clamp(a.width-8, 66, 94)
	innerW := cardW - 6

	fmtKey := func(k string) string {
		if a.revealKey {
			return k
		}
		return secrets.Mask(k)
	}

	var lines []string
	lines = append(lines, a.styles.Title.Render(fmt.Sprintf("◆ API Keys (%d total)", 1+len(cfg.Server.APIKeys))))
	lines = append(lines, a.styles.Muted.Render("Create one client key per device. Client keys can't run agents unless you allow it."))
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))

	lines = append(lines, "  "+a.styles.JoinRow(2,
		a.styles.TableHeader.Render(fmt.Sprintf("%-16s", "NAME")),
		a.styles.TableHeader.Render(fmt.Sprintf("%-8s", "ROLE")),
		a.styles.TableHeader.Render(fmt.Sprintf("%-10s", "STATUS")),
		a.styles.TableHeader.Render("KEY"),
	))

	// Row 0: Primary key
	primKey := fmtKey(cfg.Server.LocalKey)
	if a.keyCursor == 0 {
		row := fmt.Sprintf("%-16s  %-8s  %-10s  %s", "primary", "admin", "● active", truncate(primKey, max(innerW-44, 16)))
		lines = append(lines, a.styles.Selected.Width(innerW).Render("> "+row))
	} else {
		row := a.styles.JoinRow(2,
			a.styles.PadCell("primary", 16),
			a.styles.PadCell(a.styles.Highlight.Render("admin"), 8),
			a.styles.PadCell(a.styles.StateReady.Render("● active"), 10),
			a.styles.Muted.Render(truncate(primKey, max(innerW-44, 16))),
		)
		lines = append(lines, "  "+row)
	}

	maxExtra := clamp(a.height-16, 4, 12)
	for i, ck := range cfg.Server.APIKeys {
		if i >= maxExtra {
			lines = append(lines, a.styles.Muted.Render(fmt.Sprintf("  ... +%d more (run `apx keys`)", len(cfg.Server.APIKeys)-maxExtra)))
			break
		}
		stStyled := a.styles.StateReady.Render("● active")
		stPlain := "● active"
		if ck.Disabled {
			stStyled = a.styles.StateErr.Render("○ off")
			stPlain = "○ off"
		}
		role := ck.EffectiveRole()
		kDisp := fmtKey(ck.Key)
		if role != config.KeyRoleAdmin && ck.Agents {
			kDisp += "  +agents"
		}
		if len(ck.Models) > 0 {
			kDisp += "  [" + strings.Join(ck.Models, ",") + "]"
		}
		if a.keyCursor == i+1 {
			row := fmt.Sprintf("%-16s  %-8s  %-10s  %s", truncate(ck.Name, 16), role, stPlain, truncate(kDisp, max(innerW-44, 16)))
			lines = append(lines, a.styles.Selected.Width(innerW).Render("> "+row))
		} else {
			roleStyled := a.styles.Muted.Render(role)
			if role == config.KeyRoleAdmin {
				roleStyled = a.styles.Highlight.Render(role)
			}
			row := a.styles.JoinRow(2,
				a.styles.PadCell(truncate(ck.Name, 16), 16),
				a.styles.PadCell(roleStyled, 8),
				a.styles.PadCell(stStyled, 10),
				a.styles.Muted.Render(truncate(kDisp, max(innerW-44, 16))),
			)
			lines = append(lines, "  "+row)
		}
	}

	lines = append(lines, "")
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
	lines = append(lines, a.styles.JoinHints(
		a.styles.KeyHint("a", "new key"),
		a.styles.KeyHint("c", "copy"),
		a.styles.KeyHint("v", "reveal"),
		a.styles.KeyHint("r", "role"),
		a.styles.KeyHint("g", "agents"),
		a.styles.KeyHint("d", "toggle"),
		a.styles.KeyHint("x", "revoke"),
		a.styles.KeyHint("esc", "close"),
	))
	return a.styles.Modal.Width(cardW).Render(strings.Join(lines, "\n"))
}

// --- Remote Access Guide Modal (Tailscale + Cloudflare Tunnel) ---

func (a *App) openRemoteModal() {
	a.modal = modalRemoteGuide
}

func (a *App) updateRemoteModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	port := a.srv.Config().Server.Port
	switch msg.String() {
	case "esc", "q", "enter":
		a.modal = modalNone
	case "1":
		a.copyToClipboard("Tailscale Serve command", fmt.Sprintf("tailscale serve --bg --https=443 http://127.0.0.1:%d", port))
	case "2":
		a.copyToClipboard("Cloudflare quick tunnel command", fmt.Sprintf("cloudflared tunnel --url http://127.0.0.1:%d", port))
	case "k":
		a.openKeysModal()
	}
	return a, nil
}

func (a *App) viewRemoteModal() string {
	port := a.srv.Config().Server.Port
	cardW := clamp(a.width-8, 66, 92)
	innerW := cardW - 6

	lines := []string{
		a.styles.Title.Render("◆ Remote Access Guide (Tailscale & Cloudflare Tunnel)"),
		a.styles.Muted.Render("Keep apx bound to 127.0.0.1 — let Tailscale or Cloudflare terminate HTTPS safely."),
		a.styles.Divider.Render(strings.Repeat("─", innerW)),
		"",
		a.styles.Subtitle.Render("Option 1 — Tailscale Serve (Private Tailnet HTTPS — Recommended)"),
		"  1. Create a device key:  " + a.styles.Highlight.Render("apx keys create laptop --role client"),
		"  2. Expose over tailnet:  " + a.styles.Highlight.Render(fmt.Sprintf("tailscale serve --bg --https=443 http://127.0.0.1:%d", port)),
		a.styles.Muted.Render("  3. Point clients to:     https://<host>.<tailnet>.ts.net/v1"),
		"",
		a.styles.Subtitle.Render("Option 2 — Cloudflare Tunnel (Public HTTPS, Zero Open Ports)"),
		"  Quick test tunnel:       " + a.styles.Highlight.Render(fmt.Sprintf("cloudflared tunnel --url http://127.0.0.1:%d", port)),
		"  Permanent domain:        " + a.styles.Muted.Render("cloudflared tunnel create apx && cloudflared tunnel route dns apx ai.yourdomain.com"),
		a.styles.Muted.Render("  Full YAML + Access policy guide: docs/remote-access.md  (or run `apx remote`)"),
		"",
		a.styles.Muted.Render("Tip: 'client' keys can use /v1/* models but are blocked from /admin/* & /usage."),
		a.styles.Divider.Render(strings.Repeat("─", innerW)),
		a.styles.JoinHints(
			a.styles.KeyHint("1", "copy Tailscale cmd"),
			a.styles.KeyHint("2", "copy Cloudflare cmd"),
			a.styles.KeyHint("k", "manage API keys"),
			a.styles.KeyHint("esc", "close"),
		),
	}
	return a.styles.Modal.Width(cardW).Render(strings.Join(lines, "\n"))
}
