package tui

import (
	"fmt"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/sharing"
)

// openToolsModal shows the MCP servers and skills with their states. agent
// limits the servers to one agent's; title names whose list it is.
func (a *App) openToolsModal(agent, title string) {
	choices, items, err := a.srv.SharedTools("")
	if err != nil {
		a.setStatus(err.Error(), true)
	}
	if agent != "" {
		items = slices.DeleteFunc(items, func(i sharing.Item) bool {
			return i.Kind == sharing.KindMCP && !slices.Contains(i.Agents, agent)
		})
	}
	if len(items) == 0 {
		a.setStatus("No MCP servers or skills found", false)
		return
	}
	a.toolChoices, a.toolItems, a.toolTitle, a.toolCursor = choices, items, title, 0
	a.toolOn = make([]bool, len(items))
	for i, item := range items {
		a.toolOn[i] = item.State == sharing.On
	}
	a.modal = modalTools
}

// openAccountTools shows what one account gets.
func (a *App) openAccountTools(acct config.AccountConfig) {
	switch {
	case acct.Kind != config.KindACP && acct.Kind != config.KindCLI:
		a.setStatus("API-key accounts get no MCP servers or skills", false)
	case acct.UseHostProfile:
		a.setStatus(acct.Name+" uses your own agent setup; apx adds nothing", false)
	default:
		a.openToolsModal(acct.Backend, acct.Backend+"/"+acct.Name)
	}
}

// reviewNewTools opens the list when a server waits for an answer and nothing
// else is open.
func (a *App) reviewNewTools() {
	if a.modal != modalNone {
		return
	}
	if _, items, _ := a.srv.SharedTools(""); sharing.Pending(items) > 0 {
		a.openToolsModal("", sharing.PendingText(sharing.Pending(items)))
	}
}

func (a *App) updateToolsModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		a.toolCursor = max(a.toolCursor-1, 0)
	case "down", "j":
		a.toolCursor = min(a.toolCursor+1, len(a.toolItems)-1)
	case " ":
		if a.toolItems[a.toolCursor].Project {
			a.setStatus("Project skills are always on", false)
		} else {
			a.toolOn[a.toolCursor] = !a.toolOn[a.toolCursor]
		}
	case "enter":
		for i, item := range a.toolItems {
			if !item.Project {
				a.toolChoices.Set(item, a.toolOn[i])
			}
		}
		if err := sharing.Save(a.srv.DataDir(), a.toolChoices); err != nil {
			a.setStatus("Couldn't save: "+err.Error(), true)
			return a, nil
		}
		a.modal = modalNone
		a.setStatus("Saved for all accounts. Applies to new sessions.", false)
	case "esc", "q":
		a.modal = modalNone
	}
	return a, nil
}

func (a *App) viewToolsModal() string {
	cardW := clamp(a.width-8, 60, 92)
	innerW := cardW - 6
	lines := []string{
		a.styles.Title.Render("◆ Shared tools: " + a.toolTitle),
		a.styles.Muted.Render("One choice covers all accounts. New MCP servers stay off."),
		a.styles.Divider.Render(strings.Repeat("─", innerW)),
	}
	rows := clamp(a.height-12, 4, 30)
	first := clamp(a.toolCursor-rows/2, 0, max(len(a.toolItems)-rows, 0))
	for i := first; i < min(first+rows, len(a.toolItems)); i++ {
		item := a.toolItems[i]
		box := "[ ]"
		if a.toolOn[i] {
			box = "[x]"
		}
		kind, note := "MCP", strings.Join(item.Agents, ",")+"  "+item.Detail
		if item.Kind == sharing.KindSkill {
			kind, note = "skill", ""
		}
		if item.Project {
			note = "this project"
		}
		if item.State == sharing.New {
			note = "new  " + note
		}
		row := truncate(fmt.Sprintf("%s %-5s %-24s %s", box, kind, truncate(item.Name, 24), note), innerW-2)
		if i == a.toolCursor {
			lines = append(lines, a.styles.Selected.Width(innerW).Render("> "+row))
		} else {
			lines = append(lines, "  "+row)
		}
	}
	return a.styles.Modal.Width(cardW).Render(strings.Join(lines, "\n"))
}
