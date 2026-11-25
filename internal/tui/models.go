package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/router"
)

func (a *App) filteredModels() []router.ModelEntry {
	return a.filterModelList(a.srv.Router().ListModels(a.showHidden))
}

func (a *App) filterModelList(all []router.ModelEntry) []router.ModelEntry {
	if a.filterText == "" {
		return all
	}
	q := strings.ToLower(a.filterText)
	var out []router.ModelEntry
	for _, m := range all {
		if strings.Contains(strings.ToLower(m.ID), q) ||
			strings.Contains(strings.ToLower(m.DisplayName), q) ||
			strings.Contains(strings.ToLower(m.Alias), q) ||
			strings.Contains(strings.ToLower(m.Backend), q) {
			out = append(out, m)
		}
	}
	return out
}

func (a *App) cycleModelNamingMode() {
	cfg := a.srv.Config()
	switch {
	case !cfg.Catalog.ExposeDirectModels && cfg.Catalog.HideBackendQualified:
		// Clean -> + Provider
		cfg.Catalog.HideBackendQualified = false
		cfg.Catalog.ExposeDirectModels = false
		_ = a.srv.ReloadConfig(cfg)
		a.setStatus("Model names: + Provider (e.g. claude-sonnet-5-5 and agy/claude-sonnet-5-5)", false)
	case !cfg.Catalog.ExposeDirectModels && !cfg.Catalog.HideBackendQualified:
		// + Provider -> + Per-account
		cfg.Catalog.HideBackendQualified = false
		cfg.Catalog.ExposeDirectModels = true
		_ = a.srv.ReloadConfig(cfg)
		a.setStatus("Model names: + Per-account (includes agy/account/model routes)", false)
	default:
		// + Per-account -> Clean
		cfg.Catalog.HideBackendQualified = true
		cfg.Catalog.ExposeDirectModels = false
		_ = a.srv.ReloadConfig(cfg)
		a.setStatus("Model names: Clean (short shared router names only)", false)
	}
}

func modelNamingLabel(cfg *config.Config) string {
	if cfg.Catalog.ExposeDirectModels {
		return "Names: + Per-account"
	}
	if cfg.Catalog.HideBackendQualified {
		return "Names: Clean"
	}
	return "Names: + Provider"
}

func (a *App) upsertModelOverride(id string, mutate func(*config.ModelOverride)) {
	cfg := a.srv.Config()
	for i := range cfg.Models {
		if cfg.Models[i].ID == id {
			mutate(&cfg.Models[i])
			_ = a.srv.ReloadConfig(cfg)
			return
		}
	}
	ov := config.ModelOverride{ID: id}
	mutate(&ov)
	cfg.Models = append(cfg.Models, ov)
	_ = a.srv.ReloadConfig(cfg)
}

func (a *App) updateModels(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	models := a.filteredModels()
	if a.modelCursor >= len(models) && len(models) > 0 {
		a.modelCursor = len(models) - 1
	}

	switch msg.String() {
	case "up", "k":
		if a.modelCursor > 0 {
			a.modelCursor--
		}
	case "down", "j":
		if a.modelCursor < len(models)-1 {
			a.modelCursor++
		}
	case "H":
		a.showHidden = !a.showHidden
		if a.showHidden {
			a.setStatus("Showing disabled models", false)
		} else {
			a.setStatus("Hiding disabled models", false)
		}
		return a, nil
	case "v", "D", "Q": // cycle simple naming mode: Clean -> + Provider -> + Per-account
		a.cycleModelNamingMode()
		return a, nil
	case "t": // toggle grouping -low/-medium/-high/[xhigh] into Thinking Effort levels
		cfg := a.srv.Config()
		cfg.Catalog.ShowEffortVariants = !cfg.Catalog.ShowEffortVariants
		_ = a.srv.ReloadConfig(cfg)
		if cfg.Catalog.ShowEffortVariants {
			a.setStatus("Thinking variants: expanded (showing separate -low/-high entries)", false)
		} else {
			a.setStatus("Thinking variants: grouped into effort levels (low/medium/high/xhigh)", false)
		}
		return a, nil
	case "X": // bulk disable all matching models
		if len(models) > 0 {
			cfg := a.srv.Config()
			ovMap := make(map[string]int, len(cfg.Models))
			for i, ov := range cfg.Models {
				ovMap[ov.ID] = i
			}
			count := 0
			for _, m := range models {
				if m.Hidden {
					continue
				}
				if idx, ok := ovMap[m.ID]; ok {
					cfg.Models[idx].Hidden = true
				} else {
					cfg.Models = append(cfg.Models, config.ModelOverride{ID: m.ID, Hidden: true})
					ovMap[m.ID] = len(cfg.Models) - 1
				}
				count++
			}
			_ = a.srv.ReloadConfig(cfg)
			a.setStatus(fmt.Sprintf("Disabled %d model(s)", count), false)
		}
		return a, nil
	case "E": // enable all matching models (even if currently hidden!)
		matching := a.filterModelList(a.srv.Router().ListModels(true))
		if len(matching) > 0 {
			matchIDs := make(map[string]bool, len(matching))
			for _, m := range matching {
				matchIDs[m.ID] = true
			}
			cfg := a.srv.Config()
			enabled := 0
			for i := range cfg.Models {
				if matchIDs[cfg.Models[i].ID] && cfg.Models[i].Hidden {
					cfg.Models[i].Hidden = false
					enabled++
				}
			}
			_ = a.srv.ReloadConfig(cfg)
			if enabled > 0 {
				a.setStatus(fmt.Sprintf("Enabled %d model(s)", enabled), false)
			} else {
				a.setStatus(fmt.Sprintf("All %d matching model(s) already enabled", len(matching)), false)
			}
		}
		return a, nil
	case "f":
		a.setStatus("Fetching all models across enabled accounts...", false)
		return a, a.fetchAllAccountsModelsCmd()
	}

	if len(models) == 0 {
		return a, nil
	}
	sel := models[a.modelCursor]

	switch msg.String() {
	case " ", "h":
		a.upsertModelOverride(sel.ID, func(ov *config.ModelOverride) {
			ov.Hidden = !ov.Hidden
		})
		if sel.Hidden {
			a.setStatus("Enabled "+sel.ID, false)
		} else {
			a.setStatus("Disabled (hidden) "+sel.ID, false)
		}

	case "r":
		return a, a.openEditModal("Rename Model", "Display name for "+sel.ID+" (blank to clear)", sel.DisplayName, func(val string) error {
			a.upsertModelOverride(sel.ID, func(ov *config.ModelOverride) {
				ov.DisplayName = val
			})
			a.setStatus("Updated display name for "+sel.ID, false)
			return nil
		})

	case "a":
		return a, a.openEditModal("Set Model Alias", "Short alias for "+sel.ID+" (e.g. fast, pro; blank to clear)", sel.Alias, func(val string) error {
			a.upsertModelOverride(sel.ID, func(ov *config.ModelOverride) {
				ov.Alias = val
			})
			a.setStatus("Updated alias for "+sel.ID, false)
			return nil
		})
	}
	return a, nil
}

func (a *App) viewModels(bodyHeight int) string {
	models := a.filteredModels()
	allTotal := len(a.filterModelList(a.srv.Router().ListModels(true)))
	enabledTotal := len(a.filterModelList(a.srv.Router().ListModels(false)))
	fullW := max(a.width-2, 40)
	innerW := fullW - 4

	cfg := a.srv.Config()
	thinkLabel := "Thinking: grouped"
	if cfg.Catalog.ShowEffortVariants {
		thinkLabel = "Thinking: expanded"
	}
	header := a.styles.Title.Render(fmt.Sprintf("Models (%d enabled / %d total)", enabledTotal, allTotal)) +
		"   " + a.styles.Highlight.Render("["+modelNamingLabel(cfg)+" · "+thinkLabel+"]")
	if a.showHidden {
		header += "   " + a.styles.Muted.Render("(showing disabled)")
	}

	var lines []string
	lines = append(lines, header)
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))

	if len(models) == 0 {
		lines = append(lines, "")
		if allTotal > 0 {
			lines = append(lines, a.styles.Muted.Render(fmt.Sprintf("  All %d models are currently disabled. Press 'E' to enable all, or 'H' to show disabled models.", allTotal)))
		} else {
			lines = append(lines, a.styles.Muted.Render("  No models yet. Add an account on tab 2 or press 'f' to fetch models."))
		}
		return a.styles.Card.Width(fullW).Height(bodyHeight - 2).Render(strings.Join(lines, "\n"))
	}

	lines = append(lines, "  "+a.styles.JoinRow(2,
		a.styles.TableHeader.Render(fmt.Sprintf("%-10s", "STATUS")),
		a.styles.TableHeader.Render(fmt.Sprintf("%-30s", "MODEL")),
		a.styles.TableHeader.Render(fmt.Sprintf("%-11s", "ROUTING")),
		a.styles.TableHeader.Render(fmt.Sprintf("%-8s", "KIND")),
		a.styles.TableHeader.Render("NOTES"),
	))

	maxRows := max(bodyHeight-6, 4)
	start := 0
	if a.modelCursor >= maxRows {
		start = a.modelCursor - maxRows + 1
	}

	for i := start; i < len(models) && i < start+maxRows; i++ {
		m := models[i]
		status := a.styles.StateReady.Render("● enabled")
		plainSt := "● enabled"
		if m.Hidden {
			status = a.styles.Muted.Render("○ disabled")
			plainSt = "○ disabled"
		}
		routing := fmt.Sprintf("shared (%d)", len(m.AccountIDs))
		if m.Prefixed {
			routing = "prefixed"
		} else if m.AccountID != "" {
			routing = "1 account"
		}
		var flags []string
		if len(m.Efforts) > 0 {
			flags = append(flags, "thinking:"+strings.Join(m.Efforts, "/"))
		}
		if m.Alias != "" {
			flags = append(flags, "alias:"+m.Alias)
		}
		if m.DisplayName != "" {
			flags = append(flags, "name:"+m.DisplayName)
		}
		if m.AccountName != "" {
			flags = append(flags, "acct:"+m.AccountName)
		}
		notes := truncate(strings.Join(flags, " "), max(innerW-70, 10))
		if i == a.modelCursor {
			row := fmt.Sprintf("%-10s  %-30s  %-11s  %-8s  %s",
				plainSt,
				truncate(m.ID, 30),
				routing,
				truncate(m.Kind, 8),
				notes,
			)
			lines = append(lines, a.styles.Selected.Width(innerW).Render("> "+row))
		} else {
			row := a.styles.JoinRow(2,
				a.styles.PadCell(status, 10),
				a.styles.PadCell(truncate(m.ID, 30), 30),
				a.styles.PadCell(routing, 11),
				a.styles.PadCell(a.styles.Muted.Render(truncate(m.Kind, 8)), 8),
				a.styles.Muted.Render(notes),
			)
			lines = append(lines, "  "+row)
		}
	}
	return a.styles.Card.Width(fullW).Height(bodyHeight - 2).Render(strings.Join(lines, "\n"))
}
