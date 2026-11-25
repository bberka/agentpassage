package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/pool"
)

func (a *App) filteredAccounts() []config.AccountConfig {
	cfg := a.srv.Config()
	if a.filterText == "" {
		return cfg.Accounts
	}
	q := strings.ToLower(a.filterText)
	var out []config.AccountConfig
	for _, acct := range cfg.Accounts {
		if strings.Contains(strings.ToLower(acct.Name), q) ||
			strings.Contains(strings.ToLower(acct.Backend), q) ||
			strings.Contains(strings.ToLower(acct.Kind), q) ||
			strings.Contains(strings.ToLower(acct.ModelPrefix), q) {
			out = append(out, acct)
		}
	}
	return out
}

func (a *App) updateAccounts(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	accts := a.filteredAccounts()
	if a.acctCursor >= len(accts) && len(accts) > 0 {
		a.acctCursor = len(accts) - 1
	}

	switch msg.String() {
	case "up", "k":
		if a.acctCursor > 0 {
			a.acctCursor--
		}
	case "down", "j":
		if a.acctCursor < len(accts)-1 {
			a.acctCursor++
		}
	case "a":
		a.openAddAccount()
		return a, nil
	}

	if len(accts) == 0 {
		return a, nil
	}
	selected := accts[a.acctCursor]

	switch msg.String() {
	case "M", "enter": // open interactive Model Picker (load all, enable/disable, search, add)
		a.openAccountModelsModal(selected)
		return a, nil

	case "d":
		cfg := a.srv.Config()
		if acct := cfg.FindAccount(selected.ID); acct != nil {
			acct.Enabled = !acct.Enabled
			if acct.Enabled {
				acct.DisabledReason = ""
				a.srv.Pool().ClearHealth(acct.ID)
				a.setStatus("Enabled "+acct.Name, false)
			} else {
				a.srv.ACP().StopAccount(acct.ID)
				a.setStatus("Disabled "+acct.Name, false)
			}
			_ = a.srv.ReloadConfig(cfg)
		}

	case "m": // cycle ExposeMode: pool -> prefix -> both
		cfg := a.srv.Config()
		if acct := cfg.FindAccount(selected.ID); acct != nil {
			switch acct.ExposeMode {
			case config.ExposePool, "":
				acct.ExposeMode = config.ExposePrefix
			case config.ExposePrefix:
				acct.ExposeMode = config.ExposeBoth
			default:
				acct.ExposeMode = config.ExposePool
			}
			_ = a.srv.ReloadConfig(cfg)
			a.setStatus(fmt.Sprintf("%s model exposure: %s (prefix=%q)", acct.Name, acct.ExposeMode, acct.EffectivePrefix()), false)
		}

	case "P": // edit custom model prefix (e.g. a-, b-)
		return a, a.openEditModal("Model Prefix", "Prefix for "+selected.Backend+"/"+selected.Name+" (e.g. a- -> a-gemini-4-neon)", selected.EffectivePrefix(), func(val string) error {
			cfg := a.srv.Config()
			if acct := cfg.FindAccount(selected.ID); acct != nil {
				acct.ModelPrefix = val
				if val != "" && acct.ExposeMode == config.ExposePool {
					acct.ExposeMode = config.ExposePrefix
				}
				_ = a.srv.ReloadConfig(cfg)
				a.setStatus(fmt.Sprintf("Set %s prefix=%q (mode=%s)", acct.Name, acct.EffectivePrefix(), acct.ExposeMode), false)
			}
			return nil
		})

	case "r":
		return a, a.openEditModal("Rename Account", "New name for "+selected.Backend+"/"+selected.Name, selected.Name, func(val string) error {
			if err := config.ValidateName(val); err != nil {
				return err
			}
			cfg := a.srv.Config()
			if acct := cfg.FindAccount(selected.ID); acct != nil {
				acct.Name = val
				_ = a.srv.ReloadConfig(cfg)
				a.setStatus("Renamed account to "+val, false)
			}
			return nil
		})

	case "e":
		if selected.Kind == config.KindAPIKey {
			return a, a.openEditModal("Edit Base URL", "Base URL for "+selected.Backend+"/"+selected.Name, selected.BaseURL, func(val string) error {
				if val == "" {
					return fmt.Errorf("base URL cannot be empty")
				}
				cfg := a.srv.Config()
				if acct := cfg.FindAccount(selected.ID); acct != nil {
					acct.BaseURL = val
					_ = a.srv.ReloadConfig(cfg)
					a.setStatus("Updated base URL for "+acct.Name, false)
				}
				return nil
			})
		}
		return a, a.openEditModal("Edit Models", "Comma-separated models for "+selected.Backend+"/"+selected.Name, strings.Join(selected.Models, ","), func(val string) error {
			cfg := a.srv.Config()
			if acct := cfg.FindAccount(selected.ID); acct != nil {
				var ms []string
				for _, m := range strings.Split(val, ",") {
					if m = strings.TrimSpace(m); m != "" {
						ms = append(ms, m)
					}
				}
				if len(ms) == 0 {
					ms = []string{"default"}
				}
				acct.Models = ms
				_ = a.srv.ReloadConfig(cfg)
				a.setStatus("Updated models for "+acct.Name, false)
			}
			return nil
		})

	case "H": // toggle UseHostProfile (isolated profile vs host ~/.claude / ~/.codex / ~/.gemini)
		if selected.Kind == config.KindACP || selected.Kind == config.KindCLI {
			cfg := a.srv.Config()
			if acct := cfg.FindAccount(selected.ID); acct != nil {
				acct.UseHostProfile = !acct.UseHostProfile
				if acct.Kind == config.KindACP {
					a.srv.ACP().StopAccount(acct.ID)
				}
				_ = a.srv.ReloadConfig(cfg)
				mode := "isolated profile"
				if acct.UseHostProfile {
					mode = "host CLI login"
				}
				a.setStatus(fmt.Sprintf("%s switched to %s", acct.Name, mode), false)
			}
		}

	case "p":
		if selected.Kind == config.KindACP || selected.Kind == config.KindCLI {
			cfg := a.srv.Config()
			if acct := cfg.FindAccount(selected.ID); acct != nil {
				switch acct.PermissionMode {
				case config.PermDeny:
					acct.PermissionMode = config.PermAsk
				case config.PermAsk:
					acct.PermissionMode = config.PermAutoApprove
				default:
					acct.PermissionMode = config.PermDeny
				}
				_ = a.srv.ReloadConfig(cfg)
				a.setStatus(fmt.Sprintf("%s permission mode: %s", acct.Name, acct.PermissionMode), false)
			}
		}

	case "l":
		if selected.Kind == config.KindACP || selected.Kind == config.KindCLI {
			a.openLoginPrompt(selected)
			return a, nil
		}
		return a, a.openEditModal("Update API Key", "Paste new API key for "+selected.Backend+"/"+selected.Name, "", func(val string) error {
			if val == "" {
				return fmt.Errorf("API key cannot be empty")
			}
			if err := a.srv.Secrets().Set(selected.ID, val); err != nil {
				return err
			}
			a.srv.Pool().ClearHealth(selected.ID)
			_ = a.srv.ReloadConfig(a.srv.Config())
			a.setStatus("Updated API key for "+selected.Name, false)
			return nil
		})

	case "t":
		a.setStatus("Testing "+selected.Backend+"/"+selected.Name+"...", false)
		return a, a.testAccountCmd(selected)

	case "x", "delete":
		cp := selected
		a.deleteTarget = &cp
		a.modal = modalConfirmDelete
	}

	return a, nil
}

func (a *App) performDeleteAccount(target config.AccountConfig) {
	a.srv.ACP().StopAccount(target.ID)
	_ = a.srv.Secrets().Delete(target.ID)
	if target.Kind == config.KindACP {
		_ = config.DeleteProfileDir(a.srv.DataDir(), target.Backend, target.Name)
	}
	cfg := a.srv.Config()
	kept := cfg.Accounts[:0]
	for _, acct := range cfg.Accounts {
		if acct.ID != target.ID {
			kept = append(kept, acct)
		}
	}
	cfg.Accounts = kept
	_ = a.srv.ReloadConfig(cfg)
	a.setStatus("Deleted account "+target.Backend+"/"+target.Name, false)
}

func (a *App) viewAccounts(bodyHeight int) string {
	accts := a.filteredAccounts()
	snaps := make(map[string]pool.AccountSnapshot)
	for _, s := range a.srv.LiveSnapshots() {
		snaps[s.ID] = s
	}

	fullW := max(a.width-2, 40)
	innerW := fullW - 4

	var lines []string
	lines = append(lines, a.styles.Title.Render(fmt.Sprintf("Accounts (%d)", len(accts)))+"   "+
		a.styles.Muted.Render("M:models  m:pool/prefix  P:prefix  H:host/iso  l:login  t:test  p:perm  d:toggle  x:del"))
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))

	if len(accts) == 0 {
		lines = append(lines, "")
		lines = append(lines, a.styles.Muted.Render("  No matching accounts. Press 'a' to add one or 'i' to browse & install 45+ ACP tools."))
		return a.styles.Card.Width(fullW).Height(bodyHeight - 2).Render(strings.Join(lines, "\n"))
	}

	lines = append(lines, a.styles.TableHeader.Render(fmt.Sprintf("  %-18s %-7s %-11s %-14s %-6s %s", "ACCOUNT", "KIND", "STATE", "EXPOSE", "MODELS", "DETAILS")))

	maxRows := max(bodyHeight-10, 4)
	start := 0
	if a.acctCursor >= maxRows {
		start = a.acctCursor - maxRows + 1
	}

	for i := start; i < len(accts) && i < start+maxRows; i++ {
		acct := accts[i]
		snap := snaps[acct.ID]
		expose := acct.ExposeMode
		if expose == "" {
			expose = config.ExposePool
		}
		if expose == config.ExposePrefix || expose == config.ExposeBoth {
			expose = fmt.Sprintf("%s:%s", expose, acct.EffectivePrefix())
		}
		extra := acct.BaseURL
		if acct.Kind == config.KindACP || acct.Kind == config.KindCLI {
			prof := "iso"
			if acct.UseHostProfile {
				prof = "host"
			}
			extra = fmt.Sprintf("perm=%s %s cap=%d", acct.PermissionMode, prof, acct.MaxConcurrency)
		}
		totalModels := max(len(acct.AvailableModels), len(acct.Models))
		modelCol := fmt.Sprintf("%d/%d", len(acct.Models), totalModels)
		row := fmt.Sprintf("%-18s %-7s %-11s %-14s %-7s %s",
			truncate(acct.Backend+"/"+acct.Name, 18),
			acct.Kind,
			a.renderState(snap),
			truncate(expose, 14),
			modelCol,
			truncate(extra, max(innerW-65, 10)),
		)
		if i == a.acctCursor {
			lines = append(lines, a.styles.Selected.Width(innerW).Render("> "+row))
		} else {
			lines = append(lines, "  "+row)
		}
	}

	// Detail footer inside card for selected account
	if a.acctCursor >= 0 && a.acctCursor < len(accts) {
		sel := accts[a.acctCursor]
		snap := snaps[sel.ID]
		lines = append(lines, "")
		lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
		if sel.Kind == config.KindACP {
			pdir, _ := config.ProfileDir(a.srv.DataDir(), sel.Backend, sel.Name)
			lines = append(lines, a.styles.Muted.Render("Profile: "+truncate(pdir, innerW-10)))
		} else {
			lines = append(lines, a.styles.Muted.Render("Base URL: "+truncate(sel.BaseURL, innerW-11)))
		}
		lines = append(lines, a.styles.Muted.Render("Models:  "+truncate(strings.Join(sel.Models, ", "), innerW-10)))
		if snap.LastError != "" {
			lines = append(lines, a.styles.StateErr.Render("Error:   "+truncate(snap.LastError, innerW-10)))
		}
	}

	return a.styles.Card.Width(fullW).Height(bodyHeight - 2).Render(strings.Join(lines, "\n"))
}
