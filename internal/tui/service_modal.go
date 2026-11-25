package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/agentpassage/internal/service"
)

type serviceStatusMsg service.StatusInfo

func (a *App) refreshServiceCmd() tea.Cmd {
	return func() tea.Msg {
		return serviceStatusMsg(service.Status())
	}
}

func (a *App) openServiceModal() tea.Cmd {
	a.modal = modalService
	return a.refreshServiceCmd()
}

func (a *App) updateServiceModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := a.svcStatus
	switch msg.String() {
	case "esc", "q":
		a.modal = modalNone
		return a, nil

	case "i": // Install + start + enable auto-start (graceful port handoff)
		wasLocal := !a.srv.Attached() && a.srv.Running()
		if wasLocal {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_ = a.srv.DetachAndStopLocal(ctx)
			cancel()
		}
		unit, err := service.Install(a.srv.DataDir(), true)
		if err != nil {
			if wasLocal {
				_ = a.srv.Start()
			}
			a.setStatus("Service install failed: "+err.Error(), true)
			return a, a.refreshServiceCmd()
		}
		time.Sleep(350 * time.Millisecond)
		_, _ = a.srv.AttachIfRunning()
		a.setStatus("Installed, enabled auto-start & attached ("+unit+")", false)
		return a, a.refreshServiceCmd()

	case "s": // Start or Stop
		if !st.Installed {
			a.setStatus("Service not installed yet — press 'i' to install.", true)
			return a, nil
		}
		if st.Running {
			if err := service.Stop(); err != nil {
				a.setStatus("Stop failed: "+err.Error(), true)
			} else {
				a.setStatus("Stopped background service", false)
			}
		} else {
			if !a.srv.Attached() && a.srv.Running() {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				_ = a.srv.DetachAndStopLocal(ctx)
				cancel()
			}
			if err := service.Start(); err != nil {
				a.setStatus("Start failed: "+err.Error(), true)
			} else {
				time.Sleep(350 * time.Millisecond)
				_, _ = a.srv.AttachIfRunning()
				a.setStatus("Started & attached to background service", false)
			}
		}
		return a, a.refreshServiceCmd()

	case "r": // Restart
		if !st.Installed {
			a.setStatus("Service not installed yet — press 'i' to install.", true)
			return a, nil
		}
		if err := service.Restart(); err != nil {
			a.setStatus("Restart failed: "+err.Error(), true)
		} else {
			time.Sleep(350 * time.Millisecond)
			_, _ = a.srv.AttachIfRunning()
			a.setStatus("Restarted background service", false)
		}
		return a, a.refreshServiceCmd()

	case "a": // Toggle Auto-Start on login/boot
		next := !st.Enabled
		if err := service.SetAutoStart(a.srv.DataDir(), next); err != nil {
			a.setStatus("Auto-start toggle failed: "+err.Error(), true)
		} else if next {
			a.setStatus("Auto-start on login enabled", false)
		} else {
			a.setStatus("Auto-start on login disabled", false)
		}
		return a, a.refreshServiceCmd()

	case "u": // Uninstall
		if !st.Installed {
			a.setStatus("Service is not installed.", false)
			return a, nil
		}
		if err := service.Uninstall(); err != nil {
			a.setStatus("Uninstall failed: "+err.Error(), true)
		} else {
			a.setStatus("Stopped & uninstalled background service", false)
		}
		return a, a.refreshServiceCmd()
	}
	return a, nil
}

func (a *App) viewServiceModal() string {
	st := a.svcStatus
	cardW := clamp(a.width-12, 58, 82)
	innerW := cardW - 6

	installBadge := a.styles.Muted.Render("○ not installed")
	if st.Installed {
		installBadge = a.styles.StateReady.Render("● installed")
	}

	runBadge := a.styles.Muted.Render("○ stopped")
	if st.Running {
		runBadge = a.styles.StateReady.Render("● " + st.Detail)
	} else if st.Installed && st.Detail != "" {
		runBadge = a.styles.StateWarn.Render("○ " + st.Detail)
	}

	autoBadge := a.styles.Muted.Render("○ disabled")
	if st.Enabled {
		autoBadge = a.styles.StateReady.Render("● enabled (starts on login)")
	}

	modeStr := "embedded in this TUI"
	if a.srv.Attached() {
		modeStr = a.styles.StateReady.Render("attached to background service (hot-reload active)")
	}

	mgr := st.Manager
	if mgr == "" {
		mgr = "systemd / launchd"
	}

	var lines []string
	lines = append(lines, a.styles.Title.Render("◆ Background Service & Auto-Start"))
	lines = append(lines, a.styles.Muted.Render("Keep apx running silently in the background; open `apx` anytime to configure live."))
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("  %-14s %s", "Manager", mgr))
	lines = append(lines, fmt.Sprintf("  %-14s %s", "Installed", installBadge))
	lines = append(lines, fmt.Sprintf("  %-14s %s", "Status", runBadge))
	lines = append(lines, fmt.Sprintf("  %-14s %s", "Auto-Start", autoBadge))
	lines = append(lines, fmt.Sprintf("  %-14s %s", "TUI Mode", modeStr))
	if st.UnitPath != "" {
		lines = append(lines, fmt.Sprintf("  %-14s %s", "Unit File", a.styles.Muted.Render(truncate(st.UnitPath, max(innerW-18, 16)))))
	}
	lines = append(lines, "")
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))

	var hints []string
	if !st.Installed {
		hints = append(hints, a.styles.KeyHint("i", "install & start (auto-start on)"))
	} else {
		if st.Running {
			hints = append(hints, a.styles.KeyHint("s", "stop"))
		} else {
			hints = append(hints, a.styles.KeyHint("s", "start"))
		}
		hints = append(hints,
			a.styles.KeyHint("r", "restart"),
			a.styles.KeyHint("a", "toggle auto-start"),
			a.styles.KeyHint("u", "uninstall"),
		)
	}
	hints = append(hints, a.styles.KeyHint("esc", "close"))
	lines = append(lines, strings.Join(hints, "  "))

	return a.styles.Modal.Width(cardW).Render(strings.Join(lines, "\n"))
}

func (a *App) renderServiceSummaryInline() string {
	st := a.svcStatus
	if st.Running {
		auto := "auto:off"
		if st.Enabled {
			auto = "auto:on"
		}
		return a.styles.StateReady.Render("● "+st.Manager+" active") + " " + a.styles.Muted.Render("("+auto+")")
	}
	if st.Installed {
		return a.styles.StateWarn.Render("○ "+st.Manager+" stopped")
	}
	return a.styles.Muted.Render("○ not installed")
}
