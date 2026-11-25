package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/acpx/internal/connect"
)

func (a *App) openConnectModal() {
	a.connectTargets = connect.ListTargets()
	if a.connectCursor >= len(a.connectTargets) {
		a.connectCursor = 0
	}
	a.modal = modalConnect
}

func (a *App) updateConnectModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q":
		a.modal = modalNone
		return a, nil
	case "up", "k":
		if a.connectCursor > 0 {
			a.connectCursor--
		}
	case "down", "j":
		if a.connectCursor < len(a.connectTargets)-1 {
			a.connectCursor++
		}
	case "enter":
		if a.connectCursor >= 0 && a.connectCursor < len(a.connectTargets) {
			t := a.connectTargets[a.connectCursor]
			models := a.srv.Router().ListModels(false)
			res, err := connect.Apply(t.ID, a.srv.Config(), models, t.AutoApply)
			if err != nil {
				a.setStatus("Connect failed: "+err.Error(), true)
				return a, nil
			}
			if res.Applied {
				a.setStatus(res.Message, false)
			} else {
				a.copyToClipboard(t.Name+" config", res.Snippet)
			}
		}
	case "c":
		if a.connectCursor >= 0 && a.connectCursor < len(a.connectTargets) {
			t := a.connectTargets[a.connectCursor]
			models := a.srv.Router().ListModels(false)
			res, err := connect.Apply(t.ID, a.srv.Config(), models, false)
			if err == nil {
				a.copyToClipboard(t.Name+" snippet", res.Snippet)
			}
		}
	}
	return a, nil
}

func (a *App) viewConnectModal() string {
	cardW := clamp(a.width-8, 64, 92)
	innerW := cardW - 6

	var lines []string
	lines = append(lines, a.styles.Title.Render("◆ Connect Coding CLIs & T3 Code to acpx"))
	lines = append(lines, a.styles.Muted.Render("Register your local acpx gateway & models into T3 Code, Zed, OpenCode, Codex, Claude, Pi, Grok, Cursor, Cline."))
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
	lines = append(lines, "")

	for i, t := range a.connectTargets {
		badge := a.styles.Muted.Render("○ not detected")
		if t.Detected {
			badge = a.styles.StateReady.Render("● detected")
		}
		mode := a.styles.Muted.Render("copy snippet")
		if t.AutoApply {
			mode = a.styles.Highlight.Render("auto-configure")
		}
		row := fmt.Sprintf("%-22s %-16s %-16s %s",
			truncate(t.Name, 22),
			badge,
			mode,
			a.styles.Muted.Render(truncate(t.ConfigPath, max(innerW-60, 10))),
		)
		if i == a.connectCursor {
			lines = append(lines, a.styles.Selected.Width(innerW).Render("> "+row))
		} else {
			lines = append(lines, "  "+row)
		}
	}

	if a.connectCursor >= 0 && a.connectCursor < len(a.connectTargets) {
		sel := a.connectTargets[a.connectCursor]
		models := a.srv.Router().ListModels(false)
		res, _ := connect.Apply(sel.ID, a.srv.Config(), models, false)

		lines = append(lines, "")
		lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
		lines = append(lines, a.styles.Subtitle.Render("Preview — "+sel.Name))
		if res != nil {
			snipLines := strings.Split(strings.TrimSpace(res.Snippet), "\n")
			maxSnip := clamp(a.height-len(a.connectTargets)-16, 4, 9)
			for idx, sl := range snipLines {
				if idx >= maxSnip {
					lines = append(lines, a.styles.Muted.Render("  ..."))
					break
				}
				lines = append(lines, a.styles.Muted.Render("  "+truncate(sl, innerW-2)))
			}
		}
	}

	lines = append(lines, "")
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
	lines = append(lines, a.styles.JoinHints(
		a.styles.KeyHint("enter", "apply / copy"),
		a.styles.KeyHint("c", "copy snippet"),
		a.styles.KeyHint("esc", "close"),
	))
	return a.styles.Modal.Width(cardW).Render(strings.Join(lines, "\n"))
}
