package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bberka/acpx/internal/quota"
	"github.com/bberka/acpx/internal/usage"
)

func (a *App) updateUsage(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "u":
		a.copyToClipboard("Usage Hub URL", a.srv.UsageHubURL(true))
	case "o":
		url := a.srv.UsageHubURL(true)
		if err := openBrowser(url); err != nil {
			a.copyToClipboard("Usage Hub URL", url)
			a.setStatus("Browser open failed; copied URL: "+a.srv.UsageHubURL(false), false)
		} else {
			a.setStatus("Opened Usage Hub in browser: "+a.srv.UsageHubURL(false), false)
		}
	case "t":
		a.usageShow14d = !a.usageShow14d
	case "r", "R":
		a.setStatus("Refreshing live subscription limits...", false)
		return a, a.refreshQuotaCmd()
	}
	return a, nil
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", "", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

func renderSparkline(buckets []usage.Bucket) string {
	blocks := []rune("▁▂▃▄▅▆▇█")
	maxV := 0
	for _, b := range buckets {
		if b.Requests > maxV {
			maxV = b.Requests
		}
	}
	var sb strings.Builder
	for _, b := range buckets {
		if b.Requests == 0 || maxV == 0 {
			sb.WriteRune('·')
		} else {
			idx := (b.Requests * (len(blocks) - 1)) / maxV
			if idx < 0 {
				idx = 0
			}
			if idx >= len(blocks) {
				idx = len(blocks) - 1
			}
			sb.WriteRune(blocks[idx])
		}
		sb.WriteRune(' ')
	}
	return strings.TrimRight(sb.String(), " ")
}

func renderShareBar(count, total, width int) string {
	if width <= 0 {
		return ""
	}
	filled := 0
	if total > 0 && count > 0 {
		filled = max((count*width)/total, 1)
	}
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
}

func (a *App) renderQuotaWindowInline(w *quota.Window, now time.Time) string {
	if w == nil {
		return a.styles.Muted.Render("—")
	}
	left := int(w.LeftPercent + 0.5)
	if left < 0 {
		left = 0
	}
	if left > 100 {
		left = 100
	}
	bar := renderShareBar(left, 100, 8)
	st := a.styles.StateReady
	if w.UsedPercent >= 90 {
		st = a.styles.StateErr
	} else if w.UsedPercent >= 70 {
		st = a.styles.StateWarn
	}
	reset := quota.FormatResetIn(w.ResetsAt, now)
	meta := fmt.Sprintf("%s %d%% left", w.Label, left)
	if reset != "" {
		meta += " (" + reset + ")"
	}
	return st.Render(bar) + " " + meta
}

func (a *App) viewUsage(bodyHeight int) string {
	u := a.srv.LiveUsage()
	qRep := a.srv.LiveQuota()
	now := time.Now()
	fullW := max(a.width-2, 40)
	innerW := fullW - 4

	rate := 100.0
	if u.TotalRequests > 0 {
		rate = u.SuccessRate
	}
	rateStyle := a.styles.StateReady
	if rate < 90 {
		rateStyle = a.styles.StateWarn
	}
	if rate < 70 {
		rateStyle = a.styles.StateErr
	}

	buckets := u.Hourly24h
	chartTitle := "Last 24 Hours"
	if a.usageShow14d {
		buckets = u.Daily14d
		chartTitle = "Last 14 Days"
	}

	hubURL := a.srv.UsageHubURL(false)
	topLines := []string{
		a.styles.Title.Render("◆ Usage Report Hub") + "   " +
			a.styles.Highlight.Render(hubURL),
		a.styles.Divider.Render(strings.Repeat("─", innerW)),
		fmt.Sprintf("  Requests: %s (24h:%d · 7d:%d)   Success: %s (%d err)   Tokens: %s (in:%d out:%d)   Avg: %dms",
			a.styles.Highlight.Render(fmt.Sprintf("%d", u.TotalRequests)),
			u.Requests24h, u.Requests7d,
			rateStyle.Render(fmt.Sprintf("%.1f%%", rate)),
			u.ErrorRequests,
			a.styles.Highlight.Render(fmt.Sprintf("%d", u.TotalTokens)),
			u.PromptTokens, u.CompletionTokens,
			u.AvgLatencyMs,
		),
		fmt.Sprintf("  %s:  %s", a.styles.Muted.Render(chartTitle), a.styles.Highlight.Render(renderSparkline(buckets))),
	}
	topCard := a.styles.Card.Width(fullW).Render(strings.Join(topLines, "\n"))

	// Subscription Limits card (Hourly / Weekly remaining quota)
	var qLines []string
	qMeta := "press R to refresh live limits"
	if !qRep.UpdatedAt.IsZero() {
		qMeta = "updated " + qRep.UpdatedAt.Format("15:04:05")
	}
	qLines = append(qLines, a.styles.Subtitle.Render("Subscription Limits (Hourly / Weekly)")+"   "+a.styles.Muted.Render(qMeta))
	qLines = append(qLines, a.styles.TableHeader.Render(fmt.Sprintf("%-18s %-8s %-30s %-26s %s", "ACCOUNT", "PLAN", "PRIMARY / HOURLY", "WEEKLY / SECONDARY", "ACPX 5h/7d")))
	if len(qRep.Accounts) == 0 {
		qLines = append(qLines, a.styles.Muted.Render("No accounts configured yet."))
	} else {
		maxQ := clamp(bodyHeight/3, 3, 8)
		for i, q := range qRep.Accounts {
			if i >= maxQ {
				qLines = append(qLines, a.styles.Muted.Render(fmt.Sprintf("... +%d more (open web hub with 'o')", len(qRep.Accounts)-maxQ)))
				break
			}
			plan := q.PlanType
			if plan == "" {
				switch q.Source {
				case "local-budget":
					plan = "budget"
				case "unavailable":
					plan = "n/a"
				default:
					plan = "local"
				}
			}
			prim := a.renderQuotaWindowInline(q.Primary, now)
			sec := a.renderQuotaWindowInline(q.Secondary, now)
			localCol := fmt.Sprintf("%d / %d reqs", q.Local5h, q.Local7d)
			qLines = append(qLines, fmt.Sprintf("%-18s %-8s %-30s %-26s %s",
				truncate(q.AccountName, 18),
				truncate(plan, 8),
				prim,
				sec,
				a.styles.Muted.Render(truncate(localCol, max(innerW-88, 8))),
			))
		}
	}
	quotaCard := a.styles.Card.Width(fullW).Render(strings.Join(qLines, "\n"))

	remH := max(bodyHeight-lipgloss.Height(topCard)-lipgloss.Height(quotaCard), 6)
	totalReq := max(u.TotalRequests, 1)

	renderAccountCard := func(w, h int) string {
		inW := w - 4
		maxRows := max(h-4, 2)
		var lines []string
		lines = append(lines, a.styles.Subtitle.Render(fmt.Sprintf("By Account (%d)", len(u.ByAccount))))
		lines = append(lines, a.styles.TableHeader.Render(fmt.Sprintf("%-18s %-6s %-5s %-8s %-6s %s", "ACCOUNT", "REQS", "ERR", "TOKENS", "AVG", "SHARE")))
		if len(u.ByAccount) == 0 {
			lines = append(lines, a.styles.Muted.Render("No traffic recorded yet."))
		} else {
			for i, ac := range u.ByAccount {
				if i >= maxRows {
					break
				}
				barW := clamp(inW-48, 6, 14)
				lines = append(lines, fmt.Sprintf("%-18s %-6d %-5d %-8d %-6s %s",
					truncate(ac.Account, 18),
					ac.Requests,
					ac.Errors,
					ac.TotalTokens,
					fmt.Sprintf("%dms", ac.AvgLatencyMs),
					a.styles.Highlight.Render(renderShareBar(ac.Requests, totalReq, barW)),
				))
			}
		}
		return a.styles.Card.Width(w).Height(h - 2).Render(strings.Join(lines, "\n"))
	}

	renderModelCard := func(w, h int) string {
		inW := w - 4
		maxRows := max(h-4, 2)
		var lines []string
		lines = append(lines, a.styles.Subtitle.Render(fmt.Sprintf("By Model (%d)", len(u.ByModel))))
		lines = append(lines, a.styles.TableHeader.Render(fmt.Sprintf("%-24s %-6s %-8s %-6s %s", "MODEL", "REQS", "TOKENS", "AVG", "SHARE")))
		if len(u.ByModel) == 0 {
			lines = append(lines, a.styles.Muted.Render("No model traffic recorded yet."))
		} else {
			for i, m := range u.ByModel {
				if i >= maxRows {
					break
				}
				barW := clamp(inW-48, 6, 14)
				lines = append(lines, fmt.Sprintf("%-24s %-6d %-8d %-6s %s",
					truncate(m.Model, 24),
					m.Requests,
					m.TotalTokens,
					fmt.Sprintf("%dms", m.AvgLatencyMs),
					a.styles.Highlight.Render(renderShareBar(m.Requests, totalReq, barW)),
				))
			}
		}
		return a.styles.Card.Width(w).Height(h - 2).Render(strings.Join(lines, "\n"))
	}

	if a.width >= 104 {
		leftW := (a.width - 3) / 2
		rightW := a.width - 3 - leftW
		bottom := lipgloss.JoinHorizontal(lipgloss.Top, renderAccountCard(leftW, remH), " ", renderModelCard(rightW, remH))
		return lipgloss.JoinVertical(lipgloss.Left, topCard, quotaCard, bottom)
	}

	h1 := max(remH/2, 4)
	h2 := max(remH-h1, 4)
	return lipgloss.JoinVertical(lipgloss.Left, topCard, quotaCard, renderAccountCard(fullW, h1), renderModelCard(fullW, h2))
}
