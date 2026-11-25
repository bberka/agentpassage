package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/acpx/internal/pool"
	"github.com/bberka/acpx/internal/secrets"
)

func (a *App) updateDashboard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cfg := a.srv.Config()
	switch msg.String() {
	case "c":
		a.copyToClipboard("endpoint URL", cfg.BaseURL())
	case "k":
		a.copyToClipboard("local API key", cfg.Server.LocalKey)
	case "v":
		a.revealKey = !a.revealKey
	case "a":
		a.openAddAccount()
	case "W":
		a.openWelcome()
	}
	return a, nil
}

func (a *App) viewDashboard(bodyHeight int) string {
	cfg := a.srv.Config()
	status := a.styles.StateErr.Render("STOPPED")
	if a.srv.Running() {
		status = a.styles.StateReady.Render("RUNNING")
	}
	keyDisp := secrets.Mask(cfg.Server.LocalKey)
	if a.revealKey {
		keyDisp = cfg.Server.LocalKey
	}

	serverInfo := strings.Join([]string{
		fmt.Sprintf("Server: %s   URL: %s   (c: copy URL)", status, cfg.BaseURL()),
		fmt.Sprintf("Key:    %s   (k: copy key, v: toggle, a: add, W: detect)", keyDisp),
	}, "\n")
	topBox := a.styles.Box.Width(max(a.width-2, 40)).Render(serverInfo)

	// Accounts summary
	snaps := a.srv.Pool().Snapshots()
	var acctLines []string
	acctLines = append(acctLines, a.styles.Subtitle.Render(fmt.Sprintf("Accounts (%d)", len(snaps))))
	if len(snaps) == 0 {
		acctLines = append(acctLines, a.styles.Muted.Render("  No accounts yet. Press 'a' to add one or 'W' to detect local ACP CLIs."))
	} else {
		maxRows := max((bodyHeight-6)/2, 3)
		for i, s := range snaps {
			if i >= maxRows {
				acctLines = append(acctLines, a.styles.Muted.Render(fmt.Sprintf("  ... +%d more (press 2 for Accounts)", len(snaps)-maxRows)))
				break
			}
			stateStr := a.renderState(s)
			usage := fmt.Sprintf("req:%d err:%d (%d/%d)", s.TotalRequests, s.TotalErrors, s.InFlight, s.MaxConcurrency)
			errPart := ""
			if s.LastError != "" {
				errPart = "  " + a.styles.StateErr.Render(truncate(s.LastError, max(a.width-55, 16)))
			}
			acctLines = append(acctLines, fmt.Sprintf("  %-18s %-7s %-12s %s%s",
				truncate(s.Backend+"/"+s.Name, 18), s.Kind, stateStr, usage, errPart))
		}
	}

	// Live request log
	entries := a.srv.Logs().Entries()
	var logLines []string
	logLines = append(logLines, a.styles.Subtitle.Render("Live Log"))
	maxLogs := max(bodyHeight-len(acctLines)-5, 3)
	if len(entries) == 0 {
		logLines = append(logLines, a.styles.Muted.Render("  Waiting for requests..."))
	} else {
		start := max(len(entries)-maxLogs, 0)
		for _, e := range entries[start:] {
			ts := e.Time.Format("15:04:05")
			var detail string
			if e.Kind == "request" {
				detail = fmt.Sprintf("%s %s %s -> %s (%d, %dms)", e.Method, e.Path, e.Model, e.Account, e.Status, e.DurationMs)
			} else {
				detail = fmt.Sprintf("[%s] %s", e.Kind, e.Message)
			}
			if e.Error != "" {
				detail += " err=" + e.Error
			}
			line := fmt.Sprintf("  %s %s", ts, truncate(detail, max(a.width-14, 20)))
			if e.Level == "ERROR" {
				line = a.styles.StateErr.Render(line)
			} else if e.Level == "WARN" {
				line = a.styles.StateWarn.Render(line)
			}
			logLines = append(logLines, line)
		}
	}

	return topBox + "\n" + strings.Join(acctLines, "\n") + "\n\n" + strings.Join(logLines, "\n")
}

func (a *App) renderState(s pool.AccountSnapshot) string {
	switch s.State {
	case pool.StateReady:
		return a.styles.StateReady.Render("ready")
	case pool.StateBusy:
		return a.styles.StateBusy.Render("busy")
	case pool.StateCoolingDown:
		rem := ""
		if s.CooldownUntil != nil {
			secs := int(time.Until(*s.CooldownUntil).Seconds())
			if secs > 0 {
				rem = fmt.Sprintf("(%ds)", secs)
			}
		}
		return a.styles.StateCool.Render("cool" + rem)
	case pool.StateLoginNeeded:
		return a.styles.StateWarn.Render("login_needed")
	default:
		return a.styles.StateErr.Render("disabled")
	}
}
