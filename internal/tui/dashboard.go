package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bberka/agentpassage/internal/health"
	"github.com/bberka/agentpassage/internal/pool"
	"github.com/bberka/agentpassage/internal/quota"
	"github.com/bberka/agentpassage/internal/secrets"
	"github.com/bberka/agentpassage/internal/usage"
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
	snaps := a.srv.LiveSnapshots()
	models := a.srv.Router().ListModels(false)

	status := a.styles.StateErr.Render("○ STOPPED")
	if a.srv.Running() {
		if a.srv.Attached() {
			status = a.styles.StateReady.Render("● ATTACHED TO SERVICE")
		} else {
			status = a.styles.StateReady.Render("● RUNNING")
		}
	}
	keyDisp := secrets.Mask(cfg.Server.LocalKey)
	if a.revealKey {
		keyDisp = cfg.Server.LocalKey
	}

	// Empty state: centered welcome screen.
	if len(snaps) == 0 {
		heroW := clamp(a.width-12, 58, 78)
		innerW := heroW - 6
		hero := a.styles.CardAccent.Width(heroW).Render(strings.Join([]string{
			a.styles.Title.Render("◆ apx — Local ACP & API-Key Gateway"),
			a.styles.Muted.Render("One local endpoint for 45+ ACP coding agents and OpenAI/Anthropic API keys."),
			a.styles.Divider.Render(strings.Repeat("─", innerW)),
			"",
			fmt.Sprintf("  Status    %s", status),
			fmt.Sprintf("  Service   %s   %s", a.renderServiceSummaryInline(), a.styles.Muted.Render("(S: manage)")),
			fmt.Sprintf("  Endpoint  %s   %s", a.styles.Highlight.Render(cfg.BaseURL()), a.styles.Muted.Render("(c: copy)")),
			fmt.Sprintf("  API Key   %s   %s", keyDisp, a.styles.Muted.Render("(k: copy, v: reveal)")),
			fmt.Sprintf("  ACP Mode  %s", a.styles.Muted.Render("apx acp  (connect directly in T3 Code / Zed)")),
			"",
			a.styles.Divider.Render(strings.Repeat("─", innerW)),
			"  " + a.styles.KeyHint("i", "Browse & install 45+ ACP tools (agy, codex, claude, cursor, opencode...)"),
			"  " + a.styles.KeyHint("a", "Add an API-key, ACP, or Direct CLI account"),
			"  " + a.styles.KeyHint("C", "Connect T3 Code, OpenCode, Codex, Claude, Pi, Grok, Cursor, Cline"),
			"  " + a.styles.KeyHint("S", "Install & manage background service + auto-start on login"),
		}, "\n"))
		return a.styles.Place(a.width, bodyHeight, lipgloss.Center, lipgloss.Center, hero)
	}

	healthRep := a.srv.LiveHealth()
	healthByID := make(map[string]health.Result, len(healthRep.Accounts))
	for _, hr := range healthRep.Accounts {
		healthByID[hr.AccountID] = hr
	}
	renderHealthSummaryInline := func() string {
		if healthRep.TotalCount == 0 {
			return a.styles.Muted.Render("none")
		}
		if healthRep.HealthyCount == 0 && healthRep.UnhealthyCount == 0 {
			return a.styles.Muted.Render("pending (T:check)")
		}
		lbl := fmt.Sprintf("%d/%d ok", healthRep.HealthyCount, healthRep.TotalCount)
		if healthRep.UnhealthyCount > 0 {
			lbl += fmt.Sprintf(" %d err", healthRep.UnhealthyCount)
			return a.styles.StateWarn.Render("▲ " + lbl)
		}
		return a.styles.StateReady.Render("● " + lbl)
	}

	renderEndpointCard := func(w int) string {
		inner := w - 4
		g := a.styles.ColGap(3)
		lines := []string{
			a.styles.Subtitle.Render("Gateway Endpoint") + g + status + g + a.styles.Muted.Render(fmt.Sprintf("%d models", len(models))),
			a.styles.PadCell("URL", 8) + a.styles.Highlight.Render(truncate(cfg.BaseURL(), max(inner-10, 20))),
			a.styles.PadCell("Key", 8) + truncate(keyDisp, max(inner-10, 16)),
			a.styles.PadCell("Service", 8) + a.renderServiceSummaryInline() + g + a.styles.PadCell("Health", 7) + renderHealthSummaryInline(),
		}
		return a.styles.Card.Width(w).Render(strings.Join(lines, "\n"))
	}

	renderAccountsCard := func(w, h int) string {
		inner := max(w-4, 30)
		maxRows := max(h-5, 2)
		acctW := 16
		kindW := 8
		stateW := 10
		probeW := 10
		if inner >= 72 {
			acctW = 19
		}
		usageW := max(inner-(acctW+kindW+stateW+probeW+8), 10)

		var lines []string
		lines = append(lines, a.styles.Subtitle.Render(fmt.Sprintf("Accounts (%d)", len(snaps))))
		hdr := a.styles.JoinRow(2,
			a.styles.TableHeader.Render(fmt.Sprintf("%-*s", acctW, "ACCOUNT")),
			a.styles.TableHeader.Render(fmt.Sprintf("%-*s", kindW, "KIND")),
			a.styles.TableHeader.Render(fmt.Sprintf("%-*s", stateW, "STATE")),
			a.styles.TableHeader.Render(fmt.Sprintf("%-*s", probeW, "PROBE")),
			a.styles.TableHeader.Render("USAGE"),
		)
		lines = append(lines, hdr)
		lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", inner)))

		for i, s := range snaps {
			if i >= maxRows {
				lines = append(lines, a.styles.Muted.Render(fmt.Sprintf("... +%d more (press 2)", len(snaps)-maxRows)))
				break
			}
			probe := a.renderProbeShort(healthByID[s.ID])
			usage := fmt.Sprintf("req:%s err:%s (%d/%d)", usage.FormatCount(int(s.TotalRequests)), usage.FormatCount(int(s.TotalErrors)), s.InFlight, s.MaxConcurrency)
			row := a.styles.JoinRow(2,
				a.styles.PadCell(truncate(s.Backend+"/"+s.Name, acctW), acctW),
				a.styles.PadCell(a.styles.Muted.Render(truncate(s.Kind, kindW)), kindW),
				a.styles.PadCell(a.renderState(s), stateW),
				a.styles.PadCell(probe, probeW),
				truncate(usage, usageW),
			)
			lines = append(lines, row)
		}
		for len(lines) < maxRows+3 {
			lines = append(lines, "")
		}
		return a.styles.Card.Width(w).Height(h - 2).Render(strings.Join(lines, "\n"))
	}

	renderLogsCard := func(w, h int) string {
		inner := max(w-4, 24)
		maxRows := max(h-4, 3)
		entries := a.srv.LiveLogEntries()
		var lines []string
		lines = append(lines, a.styles.Subtitle.Render("Live Activity"))
		lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", inner)))
		if len(entries) == 0 {
			lines = append(lines, a.styles.Muted.Render("Waiting for requests on "+cfg.BaseURL()+" ..."))
			lines = append(lines, a.styles.Muted.Render("Tip: press 'C' to connect T3 Code, Zed, VS Code, JetBrains, OpenCode, or Pi."))
		} else {
			start := max(len(entries)-maxRows, 0)
			for _, e := range entries[start:] {
				ts := e.Time.Format("15:04:05")
				var detail string
				if e.Kind == "request" {
					detail = fmt.Sprintf("%s %s %s → %s (%d, %s)", e.Method, e.Path, e.Model, e.Account, e.Status, usage.FormatLatency(e.DurationMs))
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

	// Wide terminal (>=104 cols): 2-column split (each bordered card adds 2 cols + 1 col gap = 5 total).
	if a.width >= 104 {
		avail := max(a.width-5, 60)
		leftW := avail * 54 / 100
		rightW := avail - leftW
		epCard := renderEndpointCard(leftW)
		epH := lipgloss.Height(epCard)
		acctH := max(bodyHeight-epH, 7)
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
		// Time left replaces the word so the label fits the state column.
		label := "cool"
		if now := time.Now(); s.CooldownUntil != nil && s.CooldownUntil.After(now) {
			label = quota.FormatResetIn(s.CooldownUntil, now)
		}
		return a.styles.StateCool.Render("◷ " + label)
	case pool.StateLoginNeeded:
		return a.styles.StateWarn.Render("▲ login")
	default:
		return a.styles.StateErr.Render("○ off")
	}
}

func (a *App) renderProbeShort(r health.Result) string {
	switch r.Status {
	case health.StatusHealthy:
		return a.styles.StateReady.Render(usage.FormatLatency(r.LatencyMs))
	case health.StatusDegraded:
		return a.styles.StateWarn.Render("degraded")
	case health.StatusUnhealthy:
		return a.styles.StateErr.Render("down")
	default:
		return a.styles.Muted.Render("—")
	}
}
