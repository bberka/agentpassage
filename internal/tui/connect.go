package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/agentpassage/internal/connect"
)

func (a *App) openConnectModal() {
	a.connectTargets = connect.ListTargets()
	a.connectConfirmAction = ""
	a.connectSearching = false
	a.connectSearch.SetValue("")
	a.connectSearch.Blur()
	if a.connectCursor >= len(a.connectTargets) {
		a.connectCursor = 0
	}
	a.modal = modalConnect
}

func (a *App) filteredConnectTargets() []connect.Target {
	q := strings.ToLower(strings.TrimSpace(a.connectSearch.Value()))
	if q == "" {
		return a.connectTargets
	}
	var out []connect.Target
	for _, t := range a.connectTargets {
		hay := strings.ToLower(t.ID + " " + t.Name + " " + t.Description + " " + t.ConfigPath)
		if strings.Contains(hay, q) {
			out = append(out, t)
		}
	}
	return out
}

func (a *App) updateConnectModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Confirmation gate before writing or removing any external IDE/CLI config file.
	if a.connectConfirmAction != "" {
		switch msg.String() {
		case "y", "Y", "enter":
			action := a.connectConfirmAction
			target := a.connectConfirmTarget
			a.connectConfirmAction = ""
			switch action {
			case "connect":
				models := a.srv.Router().ListModels(false)
				res, err := connect.Apply(target.ID, a.srv.Config(), models, target.AutoApply)
				if err != nil {
					a.setStatus("Connect failed: "+err.Error(), true)
					return a, nil
				}
				a.connectTargets = connect.ListTargets()
				a.setStatus(res.Message, false)
			case "disconnect":
				res, err := connect.Disconnect(target.ID)
				if err != nil {
					a.setStatus("Disconnect failed: "+err.Error(), true)
					return a, nil
				}
				a.connectTargets = connect.ListTargets()
				a.setStatus(res.Message, false)
			case "disconnect_all":
				results, errs := connect.DisconnectAll()
				a.connectTargets = connect.ListTargets()
				removed := 0
				for _, r := range results {
					if r.Removed {
						removed++
					}
				}
				if len(errs) > 0 {
					a.setStatus(fmt.Sprintf("Disconnected %d client(s) with %d error(s)", removed, len(errs)), true)
				} else {
					a.setStatus(fmt.Sprintf("Disconnected apx from all %d connected IDE/CLI(s)", removed), false)
				}
			}
			return a, nil
		case "n", "N", "esc", "q":
			a.connectConfirmAction = ""
			a.setStatus("Cancelled — no files modified", false)
			return a, nil
		default:
			return a, nil
		}
	}

	if a.connectSearching {
		switch msg.String() {
		case "esc", "enter":
			a.connectSearching = false
			a.connectSearch.Blur()
			a.connectCursor = 0
			return a, nil
		default:
			var cmd tea.Cmd
			a.connectSearch, cmd = a.connectSearch.Update(msg)
			a.connectCursor = 0
			return a, cmd
		}
	}

	targets := a.filteredConnectTargets()
	if a.connectCursor >= len(targets) && len(targets) > 0 {
		a.connectCursor = len(targets) - 1
	}

	switch msg.String() {
	case "/":
		a.connectSearching = true
		a.connectSearch.Focus()
		return a, textinput.Blink
	case "esc":
		if strings.TrimSpace(a.connectSearch.Value()) != "" {
			a.connectSearch.SetValue("")
			a.connectCursor = 0
			return a, nil
		}
		a.modal = modalNone
		return a, nil
	case "q":
		a.modal = modalNone
		return a, nil
	case "up", "k":
		if a.connectCursor > 0 {
			a.connectCursor--
		}
	case "down", "j":
		if a.connectCursor < len(targets)-1 {
			a.connectCursor++
		}
	case "pgup":
		a.connectCursor = max(a.connectCursor-6, 0)
	case "pgdown":
		if len(targets) > 0 {
			a.connectCursor = min(a.connectCursor+6, len(targets)-1)
		}
	case "enter":
		if a.connectCursor >= 0 && a.connectCursor < len(targets) {
			t := targets[a.connectCursor]
			if !t.AutoApply {
				models := a.srv.Router().ListModels(false)
				res, err := connect.Apply(t.ID, a.srv.Config(), models, false)
				if err == nil {
					a.copyToClipboard(t.Name+" config", res.Snippet)
				}
				return a, nil
			}
			a.connectConfirmAction = "connect"
			a.connectConfirmTarget = t
		}
	case "u", "x", "delete":
		if a.connectCursor >= 0 && a.connectCursor < len(targets) {
			t := targets[a.connectCursor]
			if !t.AutoApply || !t.Connected {
				a.setStatus(t.Name+" is not auto-connected", false)
				return a, nil
			}
			a.connectConfirmAction = "disconnect"
			a.connectConfirmTarget = t
		}
	case "U":
		connectedCount := 0
		for _, t := range a.connectTargets {
			if t.Connected {
				connectedCount++
			}
		}
		if connectedCount == 0 {
			a.setStatus("No connected IDEs or CLIs found to disconnect", false)
			return a, nil
		}
		a.connectConfirmAction = "disconnect_all"
	case "c":
		if a.connectCursor >= 0 && a.connectCursor < len(targets) {
			t := targets[a.connectCursor]
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
	cardW := clamp(a.width-8, 66, 96)
	innerW := cardW - 6

	connectedCount := 0
	for _, t := range a.connectTargets {
		if t.Connected {
			connectedCount++
		}
	}

	targets := a.filteredConnectTargets()

	var lines []string
	lines = append(lines, a.styles.Title.Render(fmt.Sprintf("◆ Connect IDEs & Coding CLIs (%d connected)", connectedCount)))
	lines = append(lines, a.styles.Muted.Render("T3 Code, Zed, VS Code, JetBrains, Continue, Neovim, OpenCode, Codex, Claude, Pi, Grok, Aider, Cursor..."))
	if a.connectSearching {
		lines = append(lines, a.styles.Highlight.Render("Search: ")+a.connectSearch.View()+" "+a.styles.Muted.Render("(enter/esc)"))
	} else if q := strings.TrimSpace(a.connectSearch.Value()); q != "" {
		lines = append(lines, a.styles.Muted.Render(fmt.Sprintf("Search: %q  (/ edit, esc clear)", q)))
	}
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))

	maxRows := clamp(a.height-18, 6, 12)
	start := 0
	if a.connectCursor >= maxRows {
		start = a.connectCursor - maxRows + 1
	}
	end := min(start+maxRows, len(targets))

	if len(targets) == 0 {
		lines = append(lines, a.styles.Muted.Render("  No matching IDEs or CLIs. Press '/' to edit or 'esc' to clear."))
	}
	for i := start; i < end; i++ {
		t := targets[i]
		badge := a.styles.Muted.Render("○ not found")
		plainBadge := "○ not found"
		if t.Connected {
			badge = a.styles.StateReady.Render("● connected")
			plainBadge = "● connected"
		} else if t.Detected {
			badge = a.styles.StateWarn.Render("◐ detected")
			plainBadge = "◐ detected"
		}
		mode := a.styles.Muted.Render("manual")
		plainMode := "manual"
		if t.AutoApply {
			mode = a.styles.Highlight.Render("auto")
			plainMode = "auto"
		}
		pathOrDesc := t.ConfigPath
		if pathOrDesc == "" {
			pathOrDesc = t.Description
		}
		if i == a.connectCursor {
			row := fmt.Sprintf("> %-18s  %-12s  %-6s  %s",
				truncate(t.Name, 18),
				plainBadge,
				plainMode,
				truncate(pathOrDesc, max(innerW-44, 12)),
			)
			lines = append(lines, a.styles.Selected.Width(innerW).MaxWidth(innerW).Render(truncate(row, innerW)))
		} else {
			row := a.styles.JoinRow(2,
				a.styles.PadCell(a.surfaceText("  "+truncate(t.Name, 18)), 20),
				a.styles.PadCell(badge, 12),
				a.styles.PadCell(mode, 6),
				a.styles.Muted.Render(truncate(pathOrDesc, max(innerW-44, 12))),
			)
			lines = append(lines, row)
		}
	}
	for i := end - start; i < maxRows; i++ {
		lines = append(lines, "")
	}

	if a.connectConfirmAction != "" {
		lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
		switch a.connectConfirmAction {
		case "connect":
			lines = append(lines, a.styles.StateWarn.Render("Confirm Connect — "+a.connectConfirmTarget.Name))
			lines = append(lines, "  Will write apx config to: "+a.styles.Highlight.Render(truncate(a.connectConfirmTarget.ConfigPath, innerW-28)))
			lines = append(lines, a.styles.Muted.Render("  Backup will be saved to .apx.bak before modifying."))
		case "disconnect":
			lines = append(lines, a.styles.StateWarn.Render("Confirm Disconnect — "+a.connectConfirmTarget.Name))
			lines = append(lines, "  Will remove apx entry from: "+a.styles.Highlight.Render(truncate(a.connectConfirmTarget.ConfigPath, innerW-30)))
			lines = append(lines, a.styles.Muted.Render("  All other settings stay untouched (.apx.bak backup created)."))
		case "disconnect_all":
			lines = append(lines, a.styles.StateErr.Render(fmt.Sprintf("Confirm Disconnect All (%d connected)", connectedCount)))
			lines = append(lines, a.styles.Muted.Render("  Will remove apx from every connected IDE & CLI config file (.apx.bak backups saved)."))
		}
		lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
		lines = append(lines, a.styles.JoinHints(
			a.styles.KeyHint("y / enter", "confirm"),
			a.styles.KeyHint("n / esc", "cancel"),
		))
		return a.styles.ModalWarn.Width(cardW).Render(strings.Join(lines, "\n"))
	}

	if a.connectCursor >= 0 && a.connectCursor < len(targets) {
		sel := targets[a.connectCursor]
		models := a.srv.Router().ListModels(false)
		res, _ := connect.Apply(sel.ID, a.srv.Config(), models, false)

		lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
		lines = append(lines, a.styles.Subtitle.Render("Preview — "+sel.Name))
		if res != nil {
			snipLines := strings.Split(strings.TrimSpace(res.Snippet), "\n")
			maxSnip := clamp(a.height-maxRows-15, 4, 7)
			for idx, sl := range snipLines {
				if idx >= maxSnip {
					lines = append(lines, a.styles.Muted.Render("  ..."))
					break
				}
				lines = append(lines, a.styles.Muted.Render("  "+truncate(sl, innerW-2)))
			}
		}
	}

	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
	lines = append(lines, a.styles.JoinHints(
		a.styles.KeyHint("/", "search"),
		a.styles.KeyHint("enter", "connect"),
		a.styles.KeyHint("u", "disconnect"),
		a.styles.KeyHint("U", "disconnect all"),
		a.styles.KeyHint("c", "copy"),
		a.styles.KeyHint("esc", "close"),
	))
	return a.styles.Modal.Width(cardW).Render(strings.Join(lines, "\n"))
}
