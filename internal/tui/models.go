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
	}

	if len(models) == 0 {
		return a, nil
	}
	sel := models[a.modelCursor]

	switch msg.String() {
	case "h":
		a.upsertModelOverride(sel.ID, func(ov *config.ModelOverride) {
			ov.Hidden = !ov.Hidden
		})
		a.setStatus("Toggled visibility for "+sel.ID, false)

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
	var lines []string
	lines = append(lines, a.styles.Title.Render(fmt.Sprintf("Models (%d)", len(models)))+"  "+
		a.styles.Muted.Render("r:rename  a:alias  h:hide  H:show-hidden  /:filter"))
	lines = append(lines, "")

	if len(models) == 0 {
		lines = append(lines, a.styles.Muted.Render("No models found. Add an account to populate models."))
		return strings.Join(lines, "\n")
	}

	maxRows := max(bodyHeight-3, 4)
	start := 0
	if a.modelCursor >= maxRows {
		start = a.modelCursor - maxRows + 1
	}

	for i := start; i < len(models) && i < start+maxRows; i++ {
		m := models[i]
		flags := ""
		if m.Hidden {
			flags += "[hidden] "
		}
		if m.Alias != "" {
			flags += "alias:" + m.Alias + " "
		}
		if m.DisplayName != "" {
			flags += "name:" + m.DisplayName
		}
		row := fmt.Sprintf("%-34s %-7s %s", truncate(m.ID, 34), m.Kind, truncate(flags, max(a.width-46, 10)))
		if i == a.modelCursor {
			lines = append(lines, a.styles.Selected.Render("> "+row))
		} else {
			lines = append(lines, "  "+row)
		}
	}
	return strings.Join(lines, "\n")
}
