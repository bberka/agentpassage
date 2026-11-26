package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/health"
	"github.com/bberka/agentpassage/internal/hostauth"
	"github.com/bberka/agentpassage/internal/pool"
	"github.com/bberka/agentpassage/internal/router"
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
	case "c":
		found := hostauth.DetectAll("")
		if len(found) == 0 {
			a.setStatus("No existing host CLI logins detected on this machine.", false)
			return a, nil
		}
		a.openHostAuthModal(found, nil, false)
		return a, nil
	}

	if len(accts) == 0 {
		return a, nil
	}
	selected := accts[a.acctCursor]

	switch msg.String() {
	case "M", "enter": // open interactive Model Picker (auto-fetches live models for API-key accounts)
		return a, a.openAccountModelsModal(selected)

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
		return a, a.openEditModal("Rename Account", "New name for "+selected.Backend+"/"+selected.Name+" (moves profile & updates routing)", selected.Name, func(val string) error {
			if err := a.renameAccount(selected.ID, val); err != nil {
				return err
			}
			a.setStatus("Renamed account to "+selected.Backend+"/"+val, false)
			return nil
		})

	case "e":
		return a, a.openAccountEditor(selected)

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
		return a, a.openEditModalCmd("Update API Key", "Paste new API key for "+selected.Backend+"/"+selected.Name, "", func(val string) (tea.Cmd, error) {
			if val == "" {
				return nil, fmt.Errorf("API key cannot be empty")
			}
			if err := a.srv.Secrets().Set(selected.ID, val); err != nil {
				return nil, err
			}
			a.srv.Pool().ClearHealth(selected.ID)
			_ = a.srv.ReloadConfig(a.srv.Config())
			a.setStatus("Updated API key for "+selected.Name+" — fetching models...", false)
			return a.testAccountCmd(selected), nil
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
	healthRep := a.srv.LiveHealth()
	healthByID := make(map[string]health.Result, len(healthRep.Accounts))
	for _, hr := range healthRep.Accounts {
		healthByID[hr.AccountID] = hr
	}

	fullW := max(a.width-2, 40)
	innerW := fullW - 4

	var lines []string
	lines = append(lines, a.styles.Title.Render(fmt.Sprintf("Accounts (%d)", len(accts))))
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))

	if len(accts) == 0 {
		lines = append(lines, "")
		lines = append(lines, a.styles.Muted.Render("  No matching accounts. Press 'a' to add one or 'i' to browse & install 45+ ACP tools."))
		return a.styles.Card.Width(fullW).Height(bodyHeight - 2).Render(strings.Join(lines, "\n"))
	}

	lines = append(lines, "  "+a.styles.JoinRow(2,
		a.styles.TableHeader.Render(fmt.Sprintf("%-18s", "ACCOUNT")),
		a.styles.TableHeader.Render(fmt.Sprintf("%-8s", "KIND")),
		a.styles.TableHeader.Render(fmt.Sprintf("%-10s", "STATE")),
		a.styles.TableHeader.Render(fmt.Sprintf("%-10s", "PROBE")),
		a.styles.TableHeader.Render(fmt.Sprintf("%-13s", "EXPOSE")),
		a.styles.TableHeader.Render(fmt.Sprintf("%-7s", "MODELS")),
		a.styles.TableHeader.Render("DETAILS"),
	))

	maxRows := max(bodyHeight-11, 4)
	start := 0
	if a.acctCursor >= maxRows {
		start = a.acctCursor - maxRows + 1
	}

	for i := start; i < len(accts) && i < start+maxRows; i++ {
		acct := accts[i]
		snap := snaps[acct.ID]
		hr := healthByID[acct.ID]
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
		enabledModels, totalModels := accountGroupedModelCounts(acct, !a.srv.Config().Catalog.ShowEffortVariants)
		modelCol := fmt.Sprintf("%d/%d", enabledModels, totalModels)
		if i == a.acctCursor {
			plainState := plainStateText(snap)
			plainProbe := plainProbeText(hr)
			row := fmt.Sprintf("%-18s  %-8s  %-10s  %-10s  %-13s  %-7s  %s",
				truncate(acct.Backend+"/"+acct.Name, 18),
				truncate(acct.Kind, 8),
				truncate(plainState, 10),
				truncate(plainProbe, 10),
				truncate(expose, 13),
				truncate(modelCol, 7),
				truncate(extra, max(innerW-80, 10)),
			)
			lines = append(lines, a.styles.Selected.Width(innerW).Render("> "+row))
		} else {
			row := a.styles.JoinRow(2,
				a.styles.PadCell(truncate(acct.Backend+"/"+acct.Name, 18), 18),
				a.styles.PadCell(a.styles.Muted.Render(truncate(acct.Kind, 8)), 8),
				a.styles.PadCell(a.renderState(snap), 10),
				a.styles.PadCell(a.renderProbeShort(hr), 10),
				a.styles.PadCell(truncate(expose, 13), 13),
				a.styles.PadCell(truncate(modelCol, 7), 7),
				truncate(extra, max(innerW-80, 10)),
			)
			lines = append(lines, "  "+row)
		}
	}

	// Detail footer inside card for selected account
	if a.acctCursor >= 0 && a.acctCursor < len(accts) {
		sel := accts[a.acctCursor]
		snap := snaps[sel.ID]
		hr := healthByID[sel.ID]
		lines = append(lines, "")
		lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
		if sel.Kind == config.KindACP {
			pdir, _ := config.ProfileDir(a.srv.DataDir(), sel.Backend, sel.Name)
			lines = append(lines, a.styles.Muted.Render("Profile: "+truncate(pdir, innerW-10)))
		} else {
			lines = append(lines, a.styles.Muted.Render("Base URL: "+truncate(sel.BaseURL, innerW-11)))
		}
		healthSummary := "not probed yet (press 't' to test or 'T' to health-check all)"
		if !hr.CheckedAt.IsZero() {
			ago := time.Since(hr.CheckedAt).Round(time.Second)
			healthSummary = fmt.Sprintf("%s · %dms · %d models · checked %s ago", hr.Status, hr.LatencyMs, hr.ModelsFound, ago)
			if hr.Error != "" {
				healthSummary += " · " + hr.Error
			}
		}
		lines = append(lines, a.styles.Muted.Render("Health:  "+truncate(healthSummary, innerW-10)))
		modelSummary := formatAccountModelsSummary(sel, !a.srv.Config().Catalog.ShowEffortVariants)
		lines = append(lines, a.styles.Muted.Render("Models:  "+truncate(modelSummary, innerW-10)))
		if snap.LastError != "" {
			lines = append(lines, a.styles.StateErr.Render("Error:   "+truncate(snap.LastError, innerW-10)))
		}
	}

	return a.styles.Card.Width(fullW).Height(bodyHeight - 2).Render(strings.Join(lines, "\n"))
}

func plainStateText(s pool.AccountSnapshot) string {
	switch s.State {
	case pool.StateReady:
		return "● ready"
	case pool.StateBusy:
		return "◐ busy"
	case pool.StateCoolingDown:
		return "◷ cool"
	case pool.StateLoginNeeded:
		return "▲ login"
	default:
		return "○ off"
	}
}

func plainProbeText(r health.Result) string {
	switch r.Status {
	case health.StatusHealthy:
		return fmt.Sprintf("%dms", r.LatencyMs)
	case health.StatusDegraded:
		return "degraded"
	case health.StatusUnhealthy:
		return "down"
	default:
		return "—"
	}
}

func accountGroupedModelCounts(acct config.AccountConfig, groupThinking bool) (enabledCount, totalCount int) {
	catalog := acct.AvailableModels
	if len(catalog) < len(acct.Models) {
		catalog = acct.Models
	}
	if !groupThinking {
		return len(acct.Models), len(catalog)
	}
	enGrouped, _ := router.GroupEffortVariants(acct.Models)
	totGrouped, _ := router.GroupEffortVariants(catalog)
	return len(enGrouped), max(len(totGrouped), len(enGrouped))
}

func formatAccountModelsSummary(acct config.AccountConfig, groupThinking bool) string {
	if !groupThinking {
		return strings.Join(acct.Models, ", ")
	}
	grouped, effortsByBase := router.GroupEffortVariants(acct.Models)
	parts := make([]string, 0, len(grouped))
	for _, m := range grouped {
		effs := effortsByBase[m]
		if len(effs) == 0 && len(acct.ModelEfforts) > 0 {
			if me := acct.ModelEfforts[m]; len(me) > 0 {
				effs = me
			} else if len(effortsByBase) == 0 && len(acct.ModelEfforts["*"]) > 0 {
				effs = acct.ModelEfforts["*"]
			}
		}
		if len(effs) > 0 {
			parts = append(parts, fmt.Sprintf("%s (%s)", m, strings.Join(effs, "/")))
		} else {
			parts = append(parts, m)
		}
	}
	return strings.Join(parts, ", ")
}
