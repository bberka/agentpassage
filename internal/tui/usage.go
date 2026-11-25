package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bberka/agentpassage/internal/quota"
	"github.com/bberka/agentpassage/internal/usage"
)

func (a *App) updateUsage(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if a.usageQuotaOffset > 0 {
			a.usageQuotaOffset--
		}
	case "down", "j":
		total := len(a.srv.LiveQuota().Accounts)
		if a.usageQuotaOffset < max(total-1, 0) {
			a.usageQuotaOffset++
		}
	case "pgup":
		a.usageQuotaOffset = max(a.usageQuotaOffset-6, 0)
	case "pgdown":
		total := len(a.srv.LiveQuota().Accounts)
		a.usageQuotaOffset = min(a.usageQuotaOffset+6, max(total-1, 0))
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
	case "p":
		periods := []string{"all", "1h", "24h", "7d", "30d"}
		cur := a.usagePeriod
		if cur == "" {
			cur = "all"
		}
		next := periods[0]
		for i, p := range periods {
			if p == cur {
				next = periods[(i+1)%len(periods)]
				break
			}
		}
		a.usagePeriod = next
		a.setStatus("Usage period -> "+next, false)
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

func (a *App) surfaceText(s string) string {
	if !a.styles.HasCanvasBg {
		return s
	}
	return lipgloss.NewStyle().Background(a.styles.SurfaceBg).Render(s)
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
	return st.Render(bar) + a.styles.ColGap(1) + a.surfaceText(meta)
}

func (a *App) viewUsage(bodyHeight int) string {
	period := a.usagePeriod
	if period == "" {
		period = "all"
	}
	u := a.srv.LiveUsagePeriod(period)
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

	costStr := "$0.00"
	if u.EstimatedCostUSD > 0 {
		if u.EstimatedCostUSD < 0.01 {
			costStr = fmt.Sprintf("~$%.4f est", u.EstimatedCostUSD)
		} else {
			costStr = fmt.Sprintf("~$%.2f est", u.EstimatedCostUSD)
		}
	}

	hubURL := a.srv.UsageHubURL(false)
	topLines := []string{
		a.styles.Title.Render("◆ Usage Report Hub") + a.styles.ColGap(2) +
			a.styles.Muted.Render("["+period+" · p:period]") + a.styles.ColGap(3) +
			a.styles.Highlight.Render(hubURL),
		a.styles.Divider.Render(strings.Repeat("─", innerW)),
		a.surfaceText("  Requests: ") + a.styles.Highlight.Render(fmt.Sprintf("%d", u.TotalRequests)) +
			a.surfaceText(fmt.Sprintf(" (24h:%d · 7d:%d)   Success: ", u.Requests24h, u.Requests7d)) +
			rateStyle.Render(fmt.Sprintf("%.1f%%", rate)) +
			a.surfaceText(fmt.Sprintf(" (%d err)   Tokens: ", u.ErrorRequests)) +
			a.styles.Highlight.Render(fmt.Sprintf("%d", u.TotalTokens)) +
			a.surfaceText("   Cost: ") + a.styles.Highlight.Render(costStr) +
			a.surfaceText(fmt.Sprintf("   Avg: %dms", u.AvgLatencyMs)),
		a.surfaceText("  ") + a.styles.Muted.Render(chartTitle) + a.surfaceText(":  ") + a.styles.Highlight.Render(renderSparkline(buckets)),
	}
	topCard := a.styles.Card.Width(fullW).Render(strings.Join(topLines, "\n"))

	// Subscription Limits card (Hourly / Weekly remaining quota)
	minBottomH := 7
	if a.width < 104 {
		minBottomH = 10
	}
	availForQuota := max(bodyHeight-lipgloss.Height(topCard)-minBottomH-4, 4)

	var qLines []string
	qMeta := "press R to refresh live limits"
	if !qRep.UpdatedAt.IsZero() {
		qMeta = "updated " + qRep.UpdatedAt.Format("15:04:05")
	}
	totalQ := len(qRep.Accounts)
	maxQ := availForQuota
	startQ := 0
	if totalQ > maxQ {
		// Reserve 1 line for scroll indicator
		maxQ = max(availForQuota-1, 3)
		maxStart := max(totalQ-maxQ, 0)
		startQ = clamp(a.usageQuotaOffset, 0, maxStart)
		a.usageQuotaOffset = startQ
	} else {
		a.usageQuotaOffset = 0
	}
	endQ := min(startQ+maxQ, totalQ)

	qHeader := a.styles.Subtitle.Render(fmt.Sprintf("Subscription Limits (Hourly / Weekly) (%d)", totalQ)) + a.styles.ColGap(3) + a.styles.Muted.Render(qMeta)
	if totalQ > maxQ {
		qHeader += a.styles.ColGap(2) + a.styles.Highlight.Render(fmt.Sprintf("[%d–%d of %d · j/k scroll]", startQ+1, endQ, totalQ))
	}
	qLines = append(qLines, qHeader)
	qLines = append(qLines, a.styles.JoinRow(2,
		a.styles.TableHeader.Render(fmt.Sprintf("%-16s", "ACCOUNT")),
		a.styles.TableHeader.Render(fmt.Sprintf("%-8s", "PLAN")),
		a.styles.TableHeader.Render(fmt.Sprintf("%-34s", "PRIMARY / HOURLY")),
		a.styles.TableHeader.Render(fmt.Sprintf("%-26s", "WEEKLY / SECONDARY")),
		a.styles.TableHeader.Render("APX 5h/7d"),
	))
	if totalQ == 0 {
		qLines = append(qLines, a.styles.Muted.Render("No accounts configured yet."))
	} else {
		for _, q := range qRep.Accounts[startQ:endQ] {
			plan := q.PlanType
			if plan == "" {
				switch q.Source {
				case "claude-logs":
					plan = "claude"
				case "deepseek-live", "openrouter-live", "headers-live":
					plan = "api"
				case "local-budget":
					plan = "budget"
				case "unavailable":
					plan = "n/a"
				default:
					plan = "local"
				}
			}
			prim := a.renderQuotaWindowInline(q.Primary, now)
			if q.Primary == nil && q.Credits != "" {
				prim = a.styles.StateReady.Render(truncate(q.Credits, 34))
			}
			sec := a.renderQuotaWindowInline(q.Secondary, now)
			localCol := fmt.Sprintf("%d / %d reqs", q.Local5h, q.Local7d)
			if q.Tokens5h > 0 || q.Tokens7d > 0 {
				localCol = fmt.Sprintf("%d/%d req · %s/%s", q.Local5h, q.Local7d, quota.FormatCompactTokens(q.Tokens5h), quota.FormatCompactTokens(q.Tokens7d))
			}
			qLines = append(qLines, a.styles.JoinRow(2,
				a.styles.PadCell(a.surfaceText(truncate(q.AccountName, 16)), 16),
				a.styles.PadCell(a.styles.Muted.Render(truncate(plan, 8)), 8),
				a.styles.PadCell(prim, 34),
				a.styles.PadCell(sec, 26),
				a.styles.Muted.Render(truncate(localCol, max(innerW-94, 8))),
			))
		}
		if totalQ > maxQ {
			var navParts []string
			if startQ > 0 {
				navParts = append(navParts, fmt.Sprintf("↑ %d above", startQ))
			}
			if endQ < totalQ {
				navParts = append(navParts, fmt.Sprintf("↓ %d more", totalQ-endQ))
			}
			qLines = append(qLines, a.styles.Muted.Render("  "+strings.Join(navParts, " · ")+"  (j/k or pgup/pgdn to scroll)"))
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
		avail := max(a.width-5, 60)
		leftW := avail / 2
		rightW := avail - leftW
		bottom := lipgloss.JoinHorizontal(lipgloss.Top, renderAccountCard(leftW, remH), " ", renderModelCard(rightW, remH))
		return lipgloss.JoinVertical(lipgloss.Left, topCard, quotaCard, bottom)
	}

	h1 := max(remH/2, 4)
	h2 := max(remH-h1, 4)
	return lipgloss.JoinVertical(lipgloss.Left, topCard, quotaCard, renderAccountCard(fullW, h1), renderModelCard(fullW, h2))
}
