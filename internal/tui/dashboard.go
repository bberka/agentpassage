package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

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
	}
	return a, nil
}

func (a *App) viewDashboard(bodyHeight int) string {
	cfg := a.srv.Config()
	snaps := a.srv.Pool().Snapshots()
	models := a.srv.Router().ListModels(false)

	status := a.styles.StateErr.Render("○ STOPPED")
	if a.srv.Running() {
		status = a.styles.StateReady.Render("● RUNNING")
	}
	keyDisp := secrets.Mask(cfg.Server.LocalKey)
	if a.revealKey {
		keyDisp = cfg.Server.LocalKey
	}

	// Empty state: OpenCode-style centered welcome hero!
	if len(snaps) == 0 {
		heroW := clamp(a.width-12, 58, 78)
		innerW := heroW - 6
		hero := a.styles.CardAccent.Width(heroW).Render(strings.Join([]string{
			a.styles.Title.Render("◆ acpx — Local ACP & API-Key Gateway"),
			a.styles.Muted.Render("One local endpoint for 45+ ACP coding agents and OpenAI/Anthropic API keys."),
			a.styles.Divider.Render(strings.Repeat("─", innerW)),
			"",
			fmt.Sprintf("  Status    %s", status),
			fmt.Sprintf("  Endpoint  %s   %s", a.styles.Highlight.Render(cfg.BaseURL()), a.styles.Muted.Render("(c: copy)")),
			fmt.Sprintf("  API Key   %s   %s", keyDisp, a.styles.Muted.Render("(k: copy, v: reveal)")),
			fmt.Sprintf("  ACP Mode  %s", a.styles.Muted.Render("acpx acp  (connect directly in T3 Code / Zed)")),
			"",
			a.styles.Divider.Render(strings.Repeat("─", innerW)),
			"  " + a.styles.KeyHint("i", "Browse & install 45+ ACP tools (agy, codex, claude, cursor, opencode...)"),
			"  " + a.styles.KeyHint("a", "Add an API-key provider (DeepSeek, OpenRouter, Ollama, OpenAI...)"),
			"  " + a.styles.KeyHint("C", "Connect T3 Code, OpenCode, Codex, Claude, Pi, Grok, Cursor, Cline"),
		}, "\n"))
		return a.styles.Place(a.width, bodyHeight, lipgloss.Center, lipgloss.Center, hero)
	}

	// Build Endpoint card
	renderEndpointCard := func(w int) string {
		inner := w - 4
		lines := []string{
			a.styles.Subtitle.Render("Gateway Endpoint") + "   " + status + "   " + a.styles.Muted.Render(fmt.Sprintf("%d models", len(models))),
			fmt.Sprintf("URL  %s   %s", a.styles.Highlight.Render(truncate(cfg.BaseURL(), max(inner-18, 20))), a.styles.Muted.Render("c:copy")),
			fmt.Sprintf("Key  %s   %s", truncate(keyDisp, max(inner-24, 16)), a.styles.Muted.Render("k:copy v:show")),
		}
		return a.styles.Card.Width(w).Render(strings.Join(lines, "\n"))
	}

	// Build Accounts card
	renderAccountsCard := func(w, h int) string {
		inner := w - 4
		maxRows := max(h-4, 2)
		var lines []string
		lines = append(lines, a.styles.Subtitle.Render(fmt.Sprintf("Accounts (%d)", len(snaps)))+"  "+a.styles.Muted.Render("2:manage  a:add  i:install"))
		lines = append(lines, a.styles.TableHeader.Render(fmt.Sprintf("%-18s %-7s %-11s %s", "ACCOUNT", "KIND", "STATE", "USAGE")))
		for i, s := range snaps {
			if i >= maxRows {
				lines = append(lines, a.styles.Muted.Render(fmt.Sprintf("... +%d more (press 2)", len(snaps)-maxRows)))
				break
			}
			usage := fmt.Sprintf("req:%d err:%d (%d/%d)", s.TotalRequests, s.TotalErrors, s.InFlight, s.MaxConcurrency)
			row := fmt.Sprintf("%-18s %-7s %-11s %s",
				truncate(s.Backend+"/"+s.Name, 18),
				s.Kind,
				a.renderState(s),
				truncate(usage, max(inner-40, 10)),
			)
			lines = append(lines, row)
		}
		for len(lines) < maxRows+2 {
			lines = append(lines, "")
		}
		return a.styles.Card.Width(w).Height(h - 2).Render(strings.Join(lines, "\n"))
	}

	// Build Live Activity card
	renderLogsCard := func(w, h int) string {
		inner := w - 4
		maxRows := max(h-3, 3)
		entries := a.srv.Logs().Entries()
		var lines []string
		lines = append(lines, a.styles.Subtitle.Render("Live Activity")+"  "+a.styles.Muted.Render("5:full logs  C:connect CLIs"))
		if len(entries) == 0 {
			lines = append(lines, a.styles.Muted.Render("Waiting for requests on "+cfg.BaseURL()+" ..."))
			lines = append(lines, a.styles.Muted.Render("Tip: press 'C' to auto-register acpx into T3 Code, OpenCode, Codex, Claude, Pi, or Grok."))
		} else {
			start := max(len(entries)-maxRows, 0)
			for _, e := range entries[start:] {
				ts := e.Time.Format("15:04:05")
				var detail string
				if e.Kind == "request" {
					detail = fmt.Sprintf("%s %s %s → %s (%d, %dms)", e.Method, e.Path, e.Model, e.Account, e.Status, e.DurationMs)
				} else {
					detail = fmt.Sprintf("[%s] %s", e.Kind, e.Message)
				}
				if e.Error != "" {
					detail += " err=" + e.Error
				}
				line := fmt.Sprintf("%s %s", a.styles.Muted.Render(ts), truncate(detail, max(inner-10, 16)))
				if e.Level == "ERROR" {
					line = a.styles.StateErr.Render(truncate(ts+" "+detail, inner))
				} else if e.Level == "WARN" {
					line = a.styles.StateWarn.Render(truncate(ts+" "+detail, inner))
				}
				lines = append(lines, line)
			}
		}
		return a.styles.Card.Width(w).Height(h - 2).Render(strings.Join(lines, "\n"))
	}

	// Wide terminal (>=104 cols): 2-column OpenCode-style split filling full screen!
	if a.width >= 104 {
		leftW := (a.width - 3) * 52 / 100
		rightW := a.width - 3 - leftW
		epCard := renderEndpointCard(leftW)
		epH := lipgloss.Height(epCard)
		acctH := max(bodyHeight-epH, 6)
		leftCol := lipgloss.JoinVertical(lipgloss.Left, epCard, renderAccountsCard(leftW, acctH))
		rightCol := renderLogsCard(rightW, bodyHeight)
		return lipgloss.JoinHorizontal(lipgloss.Top, leftCol, " ", rightCol)
	}

	// Compact terminal (80x24): clean vertical stack filling exact height.
	fullW := max(a.width-2, 40)
	epCard := renderEndpointCard(fullW)
	rem := max(bodyHeight-lipgloss.Height(epCard), 8)
	acctH := max(rem/2, 4)
	logH := max(rem-acctH, 4)
	return lipgloss.JoinVertical(lipgloss.Left, epCard, renderAccountsCard(fullW, acctH), renderLogsCard(fullW, logH))
}

func (a *App) renderState(s pool.AccountSnapshot) string {
	switch s.State {
	case pool.StateReady:
		return a.styles.StateReady.Render("● ready")
	case pool.StateBusy:
		return a.styles.StateBusy.Render("◐ busy")
	case pool.StateCoolingDown:
		rem := ""
		if s.CooldownUntil != nil {
			secs := int(time.Until(*s.CooldownUntil).Seconds())
			if secs > 0 {
				rem = fmt.Sprintf("(%ds)", secs)
			}
		}
		return a.styles.StateCool.Render("◷ cool" + rem)
	case pool.StateLoginNeeded:
		return a.styles.StateWarn.Render("▲ login")
	default:
		return a.styles.StateErr.Render("○ off")
	}
}
