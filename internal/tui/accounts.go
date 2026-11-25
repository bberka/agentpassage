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
			strings.Contains(strings.ToLower(acct.Kind), q) {
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
	case "W":
		a.openWelcome()
		return a, nil
	}

	if len(accts) == 0 {
		return a, nil
	}
	selected := accts[a.acctCursor]

	switch msg.String() {
	case "d": // toggle disable/enable
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

	case "r": // rename
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

	case "e": // edit base URL / cwd or API key
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
		return a, a.openEditModal("Edit Default Cwd", "Default working directory for ACP account "+selected.Name, selected.DefaultCwd, func(val string) error {
			cfg := a.srv.Config()
			if acct := cfg.FindAccount(selected.ID); acct != nil {
				acct.DefaultCwd = val
				_ = a.srv.ReloadConfig(cfg)
				a.setStatus("Updated default cwd for "+acct.Name, false)
			}
			return nil
		})

	case "p": // cycle permission mode for ACP accounts
		if selected.Kind == config.KindACP {
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

	case "l": // re-login (ACP) or update API key
		if selected.Kind == config.KindACP {
			a.setStatus("Launching interactive login for "+selected.Backend+"/"+selected.Name+"...", false)
			return a, a.loginAccountCmd(selected)
		}
		return a, a.openEditModal("Update API Key", "Paste new API key for "+selected.Backend+"/"+selected.Name, "", func(val string) error {
			if val == "" {
				return fmt.Errorf("API key cannot be empty")
			}
			if err := a.srv.Secrets().Set(selected.ID, val); err != nil {
				return err
			}
			a.srv.Pool().ClearHealth(selected.ID)
			a.setStatus("Updated API key for "+selected.Name, false)
			return nil
		})

	case "t": // test account
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
	for _, s := range a.srv.Pool().Snapshots() {
		snaps[s.ID] = s
	}

	var lines []string
	lines = append(lines, a.styles.Title.Render(fmt.Sprintf("Accounts (%d)", len(accts)))+"  "+
		a.styles.Muted.Render("a:add  e:edit  r:rename  d:toggle  l:login/key  t:test  p:perm  x:del"))
	lines = append(lines, "")

	if len(accts) == 0 {
		lines = append(lines, a.styles.Muted.Render("No matching accounts. Press 'a' to add one or 'W' to detect ACP CLIs."))
		return strings.Join(lines, "\n")
	}

	maxRows := max(bodyHeight-4, 4)
	start := 0
	if a.acctCursor >= maxRows {
		start = a.acctCursor - maxRows + 1
	}

	for i := start; i < len(accts) && i < start+maxRows; i++ {
		acct := accts[i]
		snap := snaps[acct.ID]
		extra := acct.BaseURL
		if acct.Kind == config.KindACP {
			extra = fmt.Sprintf("perm=%s cap=%d", acct.PermissionMode, acct.MaxConcurrency)
		}
		row := fmt.Sprintf("%-18s %-7s %-12s %-6s %s",
			truncate(acct.Backend+"/"+acct.Name, 18),
			acct.Kind,
			a.renderState(snap),
			fmt.Sprintf("%dm", len(acct.Models)),
			truncate(extra, max(a.width-48, 12)),
		)
		if i == a.acctCursor {
			lines = append(lines, a.styles.Selected.Render("> "+row))
		} else {
			lines = append(lines, "  "+row)
		}
	}

	if a.acctCursor >= 0 && a.acctCursor < len(accts) {
		sel := accts[a.acctCursor]
		snap := snaps[sel.ID]
		if snap.LastError != "" {
			lines = append(lines, "")
			lines = append(lines, a.styles.StateErr.Render("Last error: "+truncate(snap.LastError, max(a.width-14, 20))))
		}
	}

	return strings.Join(lines, "\n")
}
