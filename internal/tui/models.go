package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/router"
)

func (a *App) filteredModels() []router.ModelEntry {
	all := a.srv.Router().ListModels(a.showHidden)
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
		return a, nil
	case "D": // toggle account-specific <backend>/<account>/<model> entries
		cfg := a.srv.Config()
		cfg.Catalog.ExposeDirectModels = !cfg.Catalog.ExposeDirectModels
		_ = a.srv.ReloadConfig(cfg)
		if cfg.Catalog.ExposeDirectModels {
			a.setStatus("Showing account-specific direct models (<backend>/<account>/<model>)", false)
		} else {
			a.setStatus("Hid account-specific direct models — keeping clean router models", false)
		}
		return a, nil
	case "Q": // toggle backend-qualified <backend>/<model> entries when bare model exists
		cfg := a.srv.Config()
		cfg.Catalog.HideBackendQualified = !cfg.Catalog.HideBackendQualified
		_ = a.srv.ReloadConfig(cfg)
		if cfg.Catalog.HideBackendQualified {
			a.setStatus("Showing bare router models only (hid <backend>/<model> duplicates)", false)
		} else {
			a.setStatus("Showing both bare and <backend>/<model> entries", false)
		}
		return a, nil
	case "X": // bulk disable all currently visible/filtered models (great with / filter e.g. /low, /3.1, /legacy)
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
			a.setStatus(fmt.Sprintf("Disabled %d matching model(s)", count), false)
		}
		return a, nil
	case "E": // bulk enable all currently visible/filtered models
		if len(models) > 0 {
			cfg := a.srv.Config()
			for _, m := range models {
				for i := range cfg.Models {
					if cfg.Models[i].ID == m.ID {
						cfg.Models[i].Hidden = false
					}
				}
			}
			_ = a.srv.ReloadConfig(cfg)
			a.setStatus(fmt.Sprintf("Enabled %d matching model(s)", len(models)), false)
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
	fullW := max(a.width-2, 40)
	innerW := fullW - 4

	cfg := a.srv.Config()
	directBadge := "direct:off"
	if cfg.Catalog.ExposeDirectModels {
		directBadge = "direct:on"
	}
	qualBadge := "bare+ns"
	if cfg.Catalog.HideBackendQualified {
		qualBadge = "bare-only"
	}

	var lines []string
	lines = append(lines, a.styles.Title.Render(fmt.Sprintf("Models (%d)", len(models)))+"  "+
		a.styles.Highlight.Render("["+directBadge+" · "+qualBadge+"]")+"  "+
		a.styles.Muted.Render("space:toggle  D:direct  Q:bare-only  /:filter  X:disable-shown  E:enable-shown  f:fetch"))
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))

	if len(models) == 0 {
		lines = append(lines, "")
		lines = append(lines, a.styles.Muted.Render("  No models exposed yet. Add an account on tab 2 or press 'f' to fetch all models."))
		return a.styles.Card.Width(fullW).Height(bodyHeight - 2).Render(strings.Join(lines, "\n"))
	}

	lines = append(lines, a.styles.TableHeader.Render(fmt.Sprintf("  %-10s %-32s %-10s %-7s %s", "STATUS", "MODEL ID", "MODE", "KIND", "NOTES")))

	maxRows := max(bodyHeight-6, 4)
	start := 0
	if a.modelCursor >= maxRows {
		start = a.modelCursor - maxRows + 1
	}

	for i := start; i < len(models) && i < start+maxRows; i++ {
		m := models[i]
		status := a.styles.StateReady.Render("● enabled ")
		if m.Hidden {
			status = a.styles.Muted.Render("○ disabled")
		}
		mode := fmt.Sprintf("pool(%d)", len(m.AccountIDs))
		if m.Prefixed {
			mode = "prefix"
		} else if m.AccountID != "" {
			mode = "direct"
		}
		var flags []string
		if m.Alias != "" {
			flags = append(flags, "alias:"+m.Alias)
		}
		if m.DisplayName != "" {
			flags = append(flags, "name:"+m.DisplayName)
		}
		if m.AccountName != "" {
			flags = append(flags, "acct:"+m.AccountName)
		}
		row := fmt.Sprintf("%-10s %-32s %-10s %-7s %s",
			status,
			truncate(m.ID, 32),
			mode,
			m.Kind,
			truncate(strings.Join(flags, " "), max(innerW-66, 10)),
		)
		if i == a.modelCursor {
			lines = append(lines, a.styles.Selected.Width(innerW).Render("> "+row))
		} else {
			lines = append(lines, "  "+row)
		}
	}
	return a.styles.Card.Width(fullW).Height(bodyHeight - 2).Render(strings.Join(lines, "\n"))
}
