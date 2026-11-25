package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/agentpassage/internal/logbuf"
)

func (a *App) filteredLogs() []logbuf.Entry {
	all := a.srv.LiveLogEntries()
	q := strings.ToLower(a.filterText)
	var out []logbuf.Entry
	for _, e := range all {
		if a.logLevel != "" && string(e.Level) != a.logLevel {
			continue
		}
		if q != "" {
			hay := strings.ToLower(e.Kind + " " + e.Model + " " + e.Account + " " + e.Message + " " + e.Error + " " + e.Path)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		out = append(out, e)
	}
	return out
}

func (a *App) updateLogs(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		a.logCursor++
	case "down", "j":
		if a.logCursor > 0 {
			a.logCursor--
		}
	case "l":
		switch a.logLevel {
		case "":
			a.logLevel = "INFO"
		case "INFO":
			a.logLevel = "WARN"
		case "WARN":
			a.logLevel = "ERROR"
		default:
			a.logLevel = ""
		}
		a.logCursor = 0
	}
	return a, nil
}

func (a *App) viewLogs(bodyHeight int) string {
	entries := a.filteredLogs()
	fullW := max(a.width-2, 40)
	innerW := fullW - 4

	lvl := a.logLevel
	if lvl == "" {
		lvl = "ALL"
	}
	var lines []string
	lines = append(lines, a.styles.Title.Render(fmt.Sprintf("Logs (%d)", len(entries)))+"   "+
		a.styles.Muted.Render(fmt.Sprintf("level: %s · secrets redacted", lvl)))
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))

	if len(entries) == 0 {
		lines = append(lines, "")
		lines = append(lines, a.styles.Muted.Render("  No log entries yet."))
		return a.styles.Card.Width(fullW).Height(bodyHeight - 2).Render(strings.Join(lines, "\n"))
	}

	maxRows := max(bodyHeight-5, 4)
	end := len(entries) - a.logCursor
	if end < 0 {
		end = 0
	}
	start := max(end-maxRows, 0)

	for _, e := range entries[start:end] {
		ts := e.Time.Format("15:04:05")
		var msg string
		if e.Kind == "request" {
			msg = fmt.Sprintf("%s %s model=%s acct=%s status=%d %dms", e.Method, e.Path, e.Model, e.Account, e.Status, e.DurationMs)
		} else {
			msg = fmt.Sprintf("[%s] %s", e.Kind, e.Message)
		}
		if e.Error != "" {
			msg += " err=" + e.Error
		}
		row := fmt.Sprintf("%s %-5s %s", ts, e.Level, truncate(msg, max(innerW-16, 20)))
		switch e.Level {
		case logbuf.LevelError:
			row = a.styles.StateErr.Render(row)
		case logbuf.LevelWarn:
			row = a.styles.StateWarn.Render(row)
		}
		lines = append(lines, row)
	}
	return a.styles.Card.Width(fullW).Height(bodyHeight - 2).Render(strings.Join(lines, "\n"))
}
