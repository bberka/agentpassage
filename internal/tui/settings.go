package tui

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/secrets"
	"github.com/bberka/agentpassage/internal/service"
)

const settingsCount = 20

func (a *App) updateSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cfg := a.srv.Config()
	switch msg.String() {
	case "up", "k":
		if a.settingCursor > 0 {
			a.settingCursor--
		}
	case "down", "j":
		if a.settingCursor < settingsCount-1 {
			a.settingCursor++
		}
	case "v":
		a.revealKey = !a.revealKey
	case "c":
		a.copyToClipboard("local API key", cfg.Server.LocalKey)
	case "g":
		cfg.Server.LocalKey = config.GenerateLocalKey()
		_ = a.srv.ReloadConfig(cfg)
		a.setStatus("Rotated local API key (clients must use the new key)", false)
	case "enter", " ":
		switch a.settingCursor {
		case 0:
			return a, a.openEditModal("Server Port", "TCP port (1-65535); restart required to rebind", strconv.Itoa(cfg.Server.Port), func(val string) error {
				p, err := strconv.Atoi(val)
				if err != nil || p < 1 || p > 65535 {
					return fmt.Errorf("invalid port %q", val)
				}
				cfg := a.srv.Config()
				cfg.Server.Port = p
				_ = a.srv.ReloadConfig(cfg)
				a.setStatus(fmt.Sprintf("Saved port %d (restart apx to apply)", p), false)
				return nil
			})
		case 1:
			return a, a.openEditModal("Bind Address", "Default 127.0.0.1 (non-loopback requires --allow-remote)", cfg.Server.Bind, func(val string) error {
				if val == "" {
					val = config.DefaultBind
				}
				cfg := a.srv.Config()
				cfg.Server.Bind = val
				_ = a.srv.ReloadConfig(cfg)
				a.setStatus("Saved bind address "+val+" (restart apx to apply)", false)
				return nil
			})
		case 2:
			a.copyToClipboard("local API key", cfg.Server.LocalKey)
		case 3:
			themes := []string{"auto", "dark", "light", "transparent", "ansi16"}
			next := themes[0]
			for i, t := range themes {
				if t == cfg.UI.Theme {
					next = themes[(i+1)%len(themes)]
					break
				}
			}
			cfg.UI.Theme = next
			a.styles = NewStyles(next)
			_ = a.srv.ReloadConfig(cfg)
			a.setStatus("Theme set to "+next, false)
		case 4:
			if cfg.ACP.ToolActivity == config.ToolActivityDrop {
				cfg.ACP.ToolActivity = config.ToolActivityText
			} else {
				cfg.ACP.ToolActivity = config.ToolActivityDrop
			}
			_ = a.srv.ReloadConfig(cfg)
			a.setStatus("ACP tool activity streaming: "+cfg.ACP.ToolActivity, false)
		case 5:
			a.copyToClipboard("data directory", a.srv.DataDir())
		case 6: // Install/start or restart OS background service (gracefully hands off local port!)
			st := service.Status()
			if !st.Installed {
				wasLocal := !a.srv.Attached() && a.srv.Running()
				if wasLocal {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					_ = a.srv.DetachAndStopLocal(ctx)
					cancel()
				}
				if unit, err := service.Install(a.srv.DataDir(), true); err != nil {
					if wasLocal {
						_ = a.srv.Start()
					}
					a.setStatus("Service install failed: "+err.Error(), true)
				} else {
					time.Sleep(350 * time.Millisecond)
					_, _ = a.srv.AttachIfRunning()
					a.setStatus("Installed, enabled auto-start & attached to service: "+unit, false)
				}
			} else if !st.Running {
				if !a.srv.Attached() && a.srv.Running() {
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
					_ = a.srv.DetachAndStopLocal(ctx)
					cancel()
				}
				if err := service.Start(); err != nil {
					a.setStatus("Service start failed: "+err.Error(), true)
				} else {
					time.Sleep(350 * time.Millisecond)
					_, _ = a.srv.AttachIfRunning()
					a.setStatus("Started & attached to background service", false)
				}
			} else {
				if err := service.Restart(); err != nil {
					a.setStatus("Service restart failed: "+err.Error(), true)
				} else {
					time.Sleep(350 * time.Millisecond)
					_, _ = a.srv.AttachIfRunning()
					a.setStatus("Restarted background service", false)
				}
			}
		case 7: // Toggle Auto-Start on Login
			st := service.Status()
			next := !st.Enabled
			if err := service.SetAutoStart(a.srv.DataDir(), next); err != nil {
				a.setStatus("Auto-start toggle failed: "+err.Error(), true)
			} else if next {
				a.setStatus("Auto-start on login enabled", false)
			} else {
				a.setStatus("Auto-start on login disabled", false)
			}
		case 8: // Cycle Health Check Interval: off -> 1m -> 5m -> 15m -> 30m -> off
			intervals := []int{0, 60, 300, 900, 1800}
			cur := 0
			if cfg.Health.Enabled {
				cur = cfg.Health.IntervalSeconds
			}
			next := intervals[1]
			for i, v := range intervals {
				if v == cur {
					next = intervals[(i+1)%len(intervals)]
					break
				}
			}
			cfg.Health.Enabled = next > 0
			cfg.Health.IntervalSeconds = next
			_ = a.srv.ReloadConfig(cfg)
			if next == 0 {
				a.setStatus("Background health checks disabled", false)
			} else {
				a.setStatus(fmt.Sprintf("Background health check interval set to %s", formatHealthInterval(next)), false)
			}
		case 9: // Manage Multiple API Keys
			a.openKeysModal()
		case 10: // Remote Access Guide (Tailscale & Cloudflare Tunnel)
			a.openRemoteModal()
		case 11: // Toggle Reversible Privacy Filter
			cfg.Privacy.Enabled = !cfg.Privacy.Enabled
			_ = a.srv.ReloadConfig(cfg)
			if cfg.Privacy.Enabled {
				a.setStatus("Privacy filter enabled (reversible redaction for API keys, emails, phones)", false)
			} else {
				a.setStatus("Privacy filter disabled", false)
			}
		case 12: // Toggle Desktop Alerts
			cfg.Alerts.Enabled = !cfg.Alerts.Enabled
			if cfg.Alerts.Enabled && !cfg.Alerts.Desktop && cfg.Alerts.WebhookURL == "" {
				cfg.Alerts.Desktop = true
			}
			_ = a.srv.ReloadConfig(cfg)
			a.setStatus(fmt.Sprintf("Alerts enabled=%v desktop=%v", cfg.Alerts.Enabled, cfg.Alerts.Desktop), false)
		case 13: // Edit Alerts Webhook URL
			return a, a.openEditModal("Alerts Webhook URL", "POST JSON alert events to this URL (blank = off)", cfg.Alerts.WebhookURL, func(val string) error {
				cfg := a.srv.Config()
				cfg.Alerts.WebhookURL = strings.TrimSpace(val)
				if cfg.Alerts.WebhookURL != "" {
					cfg.Alerts.Enabled = true
				}
				_ = a.srv.ReloadConfig(cfg)
				a.setStatus("Saved alerts webhook URL", false)
				return nil
			})
		case 14:
			return a, a.openEditModal("Quota Alert %", "Alert when a limit reaches this percent used (blank or 0 = off)", strconv.Itoa(cfg.Alerts.QuotaThresholdPct), func(val string) error {
				v, err := strconv.Atoi(orZero(val))
				if err != nil || v < 0 || v > 100 {
					return fmt.Errorf("enter a whole percent from 0 to 100")
				}
				c := a.srv.Config()
				c.Alerts.QuotaThresholdPct = v
				if v > 0 {
					enableAlertDestination(c)
				}
				_ = a.srv.ReloadConfig(c)
				a.setStatus("Saved quota alert threshold", false)
				return nil
			})
		case 15:
			return a, a.openEditModal("Low Balance Alert", "Alert below this amount, in the balance's own currency (blank or 0 = off)", strconv.FormatFloat(cfg.Alerts.BalanceThreshold, 'f', -1, 64), func(val string) error {
				v, err := strconv.ParseFloat(orZero(val), 64)
				if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
					return fmt.Errorf("enter an amount of 0 or more")
				}
				c := a.srv.Config()
				c.Alerts.BalanceThreshold = v
				if v > 0 {
					enableAlertDestination(c)
				}
				_ = a.srv.ReloadConfig(c)
				a.setStatus("Saved low balance threshold", false)
				return nil
			})
		case 16:
			return a, a.openEditModal("Balance Reminder", "Repeat the low balance alert every N hours (blank or 0 = once)", strconv.Itoa(cfg.Alerts.ReminderHours), func(val string) error {
				v, err := strconv.Atoi(orZero(val))
				if err != nil || v < 0 {
					return fmt.Errorf("enter a whole number of hours, 0 or more")
				}
				c := a.srv.Config()
				c.Alerts.ReminderHours = v
				_ = a.srv.ReloadConfig(c)
				a.setStatus("Saved balance reminder interval", false)
				return nil
			})
		case 17: // Toggle Local Response Cache
			cfg.Cache.Enabled = !cfg.Cache.Enabled
			_ = a.srv.ReloadConfig(cfg)
			a.setStatus(fmt.Sprintf("Local response/embedding cache = %v", cfg.Cache.Enabled), false)
		case 18: // Toggle Decision Auto-Router Model (apx/auto)
			cfg.Decision.Enabled = !cfg.Decision.Enabled
			if cfg.Decision.Enabled && cfg.Decision.ModelID == "" {
				cfg.Decision.ModelID = "apx/auto"
			}
			_ = a.srv.ReloadConfig(cfg)
			a.setStatus(fmt.Sprintf("Decision model (%s) = %v", cfg.Decision.ModelID, cfg.Decision.Enabled), false)
		case 19: // Toggle Session Wait-for-Reset on Quota Exhaustion
			cfg.Sessions.WaitForResetWhenExhausted = !cfg.Sessions.WaitForResetWhenExhausted
			_ = a.srv.ReloadConfig(cfg)
			if cfg.Sessions.WaitForResetWhenExhausted {
				a.setStatus("Wait on quota enabled (conversations wait for reset instead of switching accounts)", false)
			} else {
				a.setStatus("Wait on quota disabled (conversations switch accounts when exhausted)", false)
			}
		}
	case "S":
		// Uninstall background service when on Settings
		if a.settingCursor == 6 || a.settingCursor == 7 {
			if err := service.Uninstall(); err != nil {
				a.setStatus("Service uninstall failed: "+err.Error(), true)
			} else {
				a.setStatus("Stopped & uninstalled background service", false)
			}
		}
	}
	return a, nil
}

func orZero(val string) string {
	if v := strings.TrimSpace(val); v != "" {
		return v
	}
	return "0"
}

// enableAlertDestination turns alerts on, using desktop notifications when no
// other destination is configured.
func enableAlertDestination(cfg *config.Config) {
	cfg.Alerts.Enabled = true
	if !cfg.Alerts.Desktop && cfg.Alerts.WebhookURL == "" {
		cfg.Alerts.Desktop = true
	}
}

func (a *App) viewSettings(bodyHeight int) string {
	cfg := a.srv.Config()
	fullW := max(a.width-2, 40)
	innerW := fullW - 4

	keyDisp := secrets.Mask(cfg.Server.LocalKey)
	if a.revealKey {
		keyDisp = cfg.Server.LocalKey
	}

	svc := service.Status()
	svcVal := fmt.Sprintf("%s: %s", svc.Manager, svc.Detail)
	autoStartVal := "disabled"
	if svc.Enabled {
		autoStartVal = "enabled"
	}

	healthVal := "off"
	if cfg.Health.Enabled && cfg.Health.IntervalSeconds > 0 {
		healthVal = fmt.Sprintf("every %s", formatHealthInterval(cfg.Health.IntervalSeconds))
	}

	privVal := "off"
	if cfg.Privacy.Enabled {
		privVal = fmt.Sprintf("ON (keys=%v emails=%v phones=%v custom=%d)",
			cfg.Privacy.RedactAPIKeys, cfg.Privacy.RedactEmails, cfg.Privacy.RedactPhones, len(cfg.Privacy.CustomPatterns))
	}

	alertsVal := "off"
	if cfg.Alerts.Enabled {
		alertsVal = fmt.Sprintf("ON (desktop=%v webhook=%v)", cfg.Alerts.Desktop, cfg.Alerts.WebhookURL != "")
	}
	quotaVal := "off"
	if cfg.Alerts.QuotaThresholdPct > 0 {
		quotaVal = fmt.Sprintf("%d%% used", cfg.Alerts.QuotaThresholdPct)
	}
	balanceVal := "off"
	if cfg.Alerts.BalanceThreshold > 0 {
		balanceVal = strconv.FormatFloat(cfg.Alerts.BalanceThreshold, 'f', -1, 64)
	}
	reminderVal := "once"
	if cfg.Alerts.ReminderHours > 0 {
		reminderVal = fmt.Sprintf("every %dh", cfg.Alerts.ReminderHours)
	}
	webhookVal := "none"
	if cfg.Alerts.WebhookURL != "" {
		webhookVal = cfg.Alerts.WebhookURL
	}
	cacheVal := "off"
	if cfg.Cache.Enabled {
		cacheVal = "ON (embeddings, rerank & temperature=0 / X-Apx-Cache: true)"
	}
	decID := cfg.Decision.ModelID
	if decID == "" {
		decID = "apx/auto"
	}
	decVal := fmt.Sprintf("off (%s)", decID)
	if cfg.Decision.Enabled {
		decVal = fmt.Sprintf("ON (%s -> fast=%s coding=%s reasoning=%s)", decID, cfg.Decision.FastModel, cfg.Decision.CodingModel, cfg.Decision.ReasoningModel)
	}
	waitVal := "off (switch accounts when exhausted)"
	if cfg.Sessions.WaitForResetWhenExhausted {
		waitVal = "ON (wait for quota reset on same account)"
	}

	rows := []struct {
		label string
		value string
	}{
		{"Port", strconv.Itoa(cfg.Server.Port)},
		{"Bind Address", cfg.Server.Bind},
		{"Local Key", keyDisp},
		{"Theme", cfg.UI.Theme},
		{"ACP Tool Activity", cfg.ACP.ToolActivity},
		{"Data Directory", a.srv.DataDir() + "  (" + a.srv.Secrets().Backend() + ")"},
		{"Background Service", svcVal},
		{"Auto-Start on Login", autoStartVal},
		{"Health Check", healthVal},
		{"API Keys", fmt.Sprintf("1 primary + %d client key(s)", len(cfg.Server.APIKeys))},
		{"Remote Access", "Tailscale Serve & Cloudflare Tunnel"},
		{"Privacy Filter", privVal},
		{"Alerts", alertsVal},
		{"Alerts Webhook", webhookVal},
		{"Quota Alert %", quotaVal},
		{"Low Balance Alert", balanceVal},
		{"Balance Reminder", reminderVal},
		{"Response Cache", cacheVal},
		{"Decision Model", decVal},
		{"Wait on Quota", waitVal},
	}

	var lines []string
	lines = append(lines, a.styles.Title.Render("Settings"))
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
	lines = append(lines, "")
	// Scroll so the selected row stays visible on short terminals (the card
	// border and the three header lines take the rest of the height).
	visible := min(max(bodyHeight-7, 1), len(rows))
	first := max(a.settingCursor-visible+1, 0)
	for i := first; i < first+visible; i++ {
		r := rows[i]
		line := fmt.Sprintf("%-20s %s", r.label, truncate(r.value, max(innerW-24, 16)))
		if i == a.settingCursor {
			lines = append(lines, a.styles.Selected.Width(innerW).Render("> "+line))
		} else {
			lines = append(lines, "  "+line)
		}
	}
	return a.styles.Card.Width(fullW).Height(bodyHeight - 2).Render(strings.Join(lines, "\n"))
}

func formatHealthInterval(sec int) string {
	if sec <= 0 {
		return "off"
	}
	if sec%60 == 0 {
		return fmt.Sprintf("%dm", sec/60)
	}
	return fmt.Sprintf("%ds", sec)
}
