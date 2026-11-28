package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/agentpassage/internal/logbuf"
	"github.com/bberka/agentpassage/internal/usage"
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
	entries := a.filteredLogs()
	switch msg.String() {
	case "up", "k":
		if a.logCursor < max(len(entries)-1, 0) {
			a.logCursor++
		}
	case "down", "j":
		if a.logCursor > 0 {
			a.logCursor--
		}
	case "enter":
		a.logInspect = !a.logInspect
	case "esc":
		if a.logInspect {
			a.logInspect = false
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
		a.styles.Muted.Render(fmt.Sprintf("level: %s · enter: trace inspector · secrets redacted", lvl)))
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))

	if len(entries) == 0 {
		lines = append(lines, "")
		lines = append(lines, a.styles.Muted.Render("  No log entries yet."))
		return a.styles.Card.Width(fullW).Height(bodyHeight - 2).Render(strings.Join(lines, "\n"))
	}

	if a.logCursor >= len(entries) {
		a.logCursor = len(entries) - 1
	}
	selectedIdx := len(entries) - 1 - a.logCursor

	inspectLines := 0
	if a.logInspect {
		inspectLines = 7
	}
	maxRows := max(bodyHeight-5-inspectLines, 4)
	end := selectedIdx + 1
	if end < maxRows {
		end = min(len(entries), maxRows)
	}
	start := max(end-maxRows, 0)

	for idx := start; idx < end; idx++ {
		e := entries[idx]
		ts := e.Time.Format("15:04:05")
		var msg string
		if e.Kind == "request" {
			msg = fmt.Sprintf("%s %s model=%s acct=%s status=%d %s", e.Method, e.Path, e.Model, e.Account, e.Status, usage.FormatLatency(e.DurationMs))
		} else {
			msg = fmt.Sprintf("[%s] %s", e.Kind, e.Message)
		}
		if e.Error != "" {
			msg += " err=" + e.Error
		}
		prefix := "  "
		if idx == selectedIdx {
			prefix = "> "
		}
		row := fmt.Sprintf("%s%s %-5s %s", prefix, ts, e.Level, truncate(msg, max(innerW-18, 20)))
		if idx == selectedIdx {
			row = a.styles.Selected.Width(innerW).MaxWidth(innerW).Render(row)
		} else {
			switch e.Level {
			case logbuf.LevelError:
				row = a.styles.StateErr.Render(row)
			case logbuf.LevelWarn:
				row = a.styles.StateWarn.Render(row)
			}
		}
		lines = append(lines, row)
	}

	if a.logInspect && selectedIdx >= 0 && selectedIdx < len(entries) {
		e := entries[selectedIdx]
		lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
		lines = append(lines, a.styles.Highlight.Render("Trace Inspector")+"  "+a.styles.Muted.Render(e.Time.Format("2006-01-02 15:04:05")+" · "+string(e.Level)+" · "+e.Kind))
		if e.Kind == "request" {
			up := e.UpstreamModel
			if up == "" {
				up = e.Model
			}
			eff := e.Effort
			if eff == "" {
				eff = "default"
			}
			proj := e.Project
			if proj == "" {
				proj = "—"
			}
			lines = append(lines, a.styles.Muted.Render(fmt.Sprintf("Route:   %s %s  model=%s -> upstream=%s  effort=%s  project=%s", e.Method, e.Path, e.Model, up, eff, proj)))
			lines = append(lines, a.styles.Muted.Render(fmt.Sprintf("Account: %s  status=%d  latency=%s  failover_hops=%d  tokens=%s in / %s out", e.Account, e.Status, usage.FormatLatency(e.DurationMs), e.FailoverHops, usage.FormatCount(e.TokensIn), usage.FormatCount(e.TokensOut))))
		} else if e.Message != "" {
			lines = append(lines, a.styles.Muted.Render("Message: "+truncate(e.Message, max(innerW-10, 20))))
		}
		if e.Error != "" {
			lines = append(lines, a.styles.StateErr.Render("Error:   "+truncate(e.Error, max(innerW-10, 20))))
		}
	}
	return a.styles.Card.Width(fullW).Height(bodyHeight - 2).Render(strings.Join(lines, "\n"))
}
