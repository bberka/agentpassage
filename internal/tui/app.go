package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/connect"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/gateway"
	"github.com/bberka/agentpassage/internal/health"
	"github.com/bberka/agentpassage/internal/logbuf"
	"github.com/bberka/agentpassage/internal/quota"
	"github.com/bberka/agentpassage/internal/service"
)

type Screen int

const (
	ScreenDashboard Screen = iota
	ScreenAccounts
	ScreenModels
	ScreenRouting
	ScreenLogs
	ScreenUsage
	ScreenSettings
)

var screenNames = []string{
	"1 Dashboard",
	"2 Accounts",
	"3 Models",
	"4 Routing",
	"5 Logs",
	"6 Usage",
	"7 Settings",
}

type modalKind int

const (
	modalNone modalKind = iota
	modalHelp
	modalWelcome
	modalAddAccount
	modalEditAccount
	modalAccountModels
	modalInstalling
	modalLoginPrompt
	modalConnect
	modalService
	modalAPIKeys
	modalRemoteGuide
	modalConfirmDelete
	modalSubStrategyWarn
	modalEditText
	modalPermission
)

type tickMsg time.Time
type logEntryMsg logbuf.Entry
type permReqMsg backend.PermissionRequest

type testResultMsg struct {
	accountID string
	models    []string
	err       error
}

type loginDoneMsg struct {
	accountID string
	err       error
}

type registrySyncMsg struct {
	count int
	err   error
}

type healthCheckDoneMsg struct {
	report health.Report
	err    error
}

type quotaRefreshDoneMsg struct {
	report quota.Report
	err    error
}

// App is the root Bubble Tea model for apx.
type App struct {
	srv      *gateway.Server
	styles   Styles
	width    int
	height   int
	spinTick int

	screen Screen
	modal  modalKind

	statusMsg string
	statusErr bool

	filtering   bool
	filterInput textinput.Model
	filterText  string

	revealKey bool

	acctCursor    int
	modelCursor   int
	showHidden    bool
	routingCursor int
	logCursor     int
	logLevel      string
	usageShow14d  bool
	usagePeriod   string
	settingCursor int

	// Welcome / ACP Registry Detect & Install modal
	detected       []descriptors.DetectedCLI
	detectCursor   int
	detectSelected map[string]bool
	detectSearch   textinput.Model
	detectSearching bool

	// Connect CLI / T3 Code modal
	connectTargets       []connect.Target
	connectCursor        int
	connectConfirmAction string // "", "connect", "disconnect", "disconnect_all"
	connectConfirmTarget connect.Target

	// API Keys modal cursor
	keyCursor int

	// Cached OS background service status (refreshed asynchronously)
	svcStatus service.StatusInfo

	// Live in-app ACP installer
	installer *installState

	// Pre-login confirmation target
	loginTarget *config.AccountConfig

	// Add Account wizard
	addStep      int
	addKind      string
	addBackend   string
	addListCur   int
	addFocus     int
	addInputs    []textinput.Model
	addAuthStyle string
	addTesting   bool

	// Interactive Model Picker & Account Editor modals
	modelPicker *modelPickerState
	acctEditor  *accountEditorState

	// Generic single-line text edit modal
	editTitle    string
	editHint     string
	editInput    textinput.Model
	editOnSubmit func(val string) error

	// Delete confirmation target
	deleteTarget *config.AccountConfig

	// Load-balancing strategy confirmation / subscription ban-risk warning
	warnRuleIdx  int
	warnStrategy string
	warnHasSub   bool

	// Live permission approval prompt
	activePerm *backend.PermissionRequest
	permCh     chan backend.PermissionRequest

	// Live log subscription
	logSubID int
	logSubCh <-chan logbuf.Entry
}

func New(srv *gateway.Server) *App {
	cfg := srv.Config()
	fi := textinput.New()
	fi.Prompt = ""
	fi.Placeholder = "type to filter..."
	fi.CharLimit = 64

	ds := textinput.New()
	ds.Prompt = ""
	ds.Placeholder = "search 45+ ACP agents (codex, claude, cursor, opencode, cline, muse...)"
	ds.CharLimit = 64

	ei := textinput.New()
	ei.Prompt = ""
	ei.CharLimit = 256

	subID, subCh := srv.Logs().Subscribe()

	a := &App{
		srv:            srv,
		styles:         NewStyles(cfg.UI.Theme),
		width:          100,
		height:         30,
		screen:         ScreenDashboard,
		filterInput:    fi,
		detectSearch:   ds,
		editInput:      ei,
		detectSelected: make(map[string]bool),
		permCh:         make(chan backend.PermissionRequest, 8),
		logSubID:       subID,
		logSubCh:       subCh,
	}

	srv.ACP().SetPermissionHandler(func(ctx context.Context, req backend.PermissionRequest) backend.PermissionDecision {
		req.ReplyCh = make(chan backend.PermissionDecision, 1)
		select {
		case a.permCh <- req:
		case <-ctx.Done():
			return backend.PermissionDecision{Approved: false}
		}
		select {
		case dec := <-req.ReplyCh:
			return dec
		case <-ctx.Done():
			return backend.PermissionDecision{Approved: false}
		}
	})

	if !cfg.UI.OnboardingComplete && len(cfg.Accounts) == 0 {
		a.openWelcome()
	}

	return a
}

func (a *App) Close() {
	if a.installer != nil && a.installer.cancel != nil {
		a.installer.cancel()
	}
	a.srv.Logs().Unsubscribe(a.logSubID)
	a.srv.ACP().SetPermissionHandler(nil)
}

func (a *App) Init() tea.Cmd {
	return tea.Batch(
		tickCmd(),
		waitLogCmd(a.logSubCh),
		waitPermCmd(a.permCh),
		a.refreshServiceCmd(),
	)
}

func tickCmd() tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func waitLogCmd(ch <-chan logbuf.Entry) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return nil
		}
		return logEntryMsg(e)
	}
}

func waitPermCmd(ch <-chan backend.PermissionRequest) tea.Cmd {
	return func() tea.Msg {
		req, ok := <-ch
		if !ok {
			return nil
		}
		return permReqMsg(req)
	}
}

func (a *App) setStatus(msg string, isErr bool) {
	a.statusMsg = msg
	a.statusErr = isErr
}

func (a *App) copyToClipboard(label, text string) {
	if err := clipboard.WriteAll(text); err != nil {
		a.setStatus(fmt.Sprintf("%s ready (clipboard unavailable)", label), false)
		return
	}
	a.setStatus(fmt.Sprintf("Copied %s to clipboard", label), false)
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = max(msg.Width, 60)
		a.height = max(msg.Height, 18)
		return a, nil

	case tickMsg:
		a.spinTick++
		var cmds []tea.Cmd
		cmds = append(cmds, tickCmd())
		if a.srv.Attached() && a.spinTick%10 == 0 {
			cmds = append(cmds, a.syncDaemonCmd())
		}
		if a.spinTick%35 == 0 {
			cmds = append(cmds, a.refreshServiceCmd())
		}
		return a, tea.Batch(cmds...)

	case serviceStatusMsg:
		a.svcStatus = service.StatusInfo(msg)
		return a, nil

	case logEntryMsg:
		return a, waitLogCmd(a.logSubCh)

	case installEventMsg:
		return a.handleInstallEvent(msg)

	case registrySyncMsg:
		if msg.err != nil {
			a.setStatus("Registry sync failed: "+msg.err.Error(), true)
			return a, nil
		}
		reg, _ := descriptors.LoadRegistry(a.srv.DataDir())
		a.srv.ACP().SetRegistry(reg)
		a.detected = reg.DetectInstalled()
		a.setStatus(fmt.Sprintf("Synced official ACP registry (%d agents)", msg.count), false)
		return a, nil

	case permReqMsg:
		req := backend.PermissionRequest(msg)
		a.activePerm = &req
		a.modal = modalPermission
		return a, waitPermCmd(a.permCh)

	case fetchModelsMsg:
		if a.modelPicker != nil {
			a.modelPicker.loading = false
			if msg.err != nil {
				a.modelPicker.fetchErr = msg.err.Error()
				a.setStatus("Model fetch failed: "+msg.err.Error(), true)
			} else {
				a.modelPicker.fetchErr = ""
				a.modelPicker.mergeFetched(msg.models, true)
				a.setStatus(fmt.Sprintf("Loaded %d models (%d enabled)", len(a.modelPicker.models), a.modelPicker.enabledCount()), false)
			}
		}
		return a, nil

	case fetchAllModelsMsg:
		if len(msg.errors) > 0 && msg.updatedAccounts == 0 {
			a.setStatus("Model fetch failed: "+strings.Join(msg.errors, "; "), true)
		} else {
			a.setStatus(fmt.Sprintf("Loaded %d models across %d account(s)", msg.totalModels, msg.updatedAccounts), false)
		}
		return a, nil

	case testResultMsg:
		a.addTesting = false
		if msg.err != nil {
			a.setStatus("Test failed: "+msg.err.Error(), true)
			return a, nil
		}
		cfg := a.srv.Config()
		if acct := cfg.FindAccount(msg.accountID); acct != nil {
			if len(msg.models) > 0 {
				acct.MergeDiscoveredModels(msg.models)
			}
			a.srv.Pool().ClearHealth(acct.ID)
			_ = a.srv.ReloadConfig(cfg)
			a.setStatus(fmt.Sprintf("Account %s OK (%d enabled / %d total models)", acct.Name, len(acct.Models), len(acct.AvailableModels)), false)
		} else {
			a.setStatus(fmt.Sprintf("Connection OK (%d models)", len(msg.models)), false)
		}
		return a, nil

	case loginDoneMsg:
		if msg.err != nil {
			a.setStatus("Login exited: "+msg.err.Error(), true)
		} else {
			a.srv.ACP().StopAccount(msg.accountID)
			a.srv.Pool().ClearHealth(msg.accountID)
			cfg := a.srv.Config()
			if acct := cfg.FindAccount(msg.accountID); acct != nil {
				if !acct.Enabled && strings.Contains(strings.ToLower(acct.DisabledReason), "auth") {
					acct.Enabled = true
					acct.DisabledReason = ""
				}
				_ = a.srv.ReloadConfig(cfg)
				a.setStatus("Login finished for "+acct.Backend+"/"+acct.Name+" — press 't' to test.", false)
			} else {
				a.setStatus("Login finished — press 't' on Accounts to test.", false)
			}
		}
		return a, nil

	case healthCheckDoneMsg:
		if msg.err != nil {
			a.setStatus("Health check failed: "+msg.err.Error(), true)
			return a, nil
		}
		rep := msg.report
		if rep.UnhealthyCount > 0 {
			a.setStatus(fmt.Sprintf("Health check: %d/%d healthy, %d unhealthy/degraded", rep.HealthyCount, rep.TotalCount, rep.UnhealthyCount), true)
		} else {
			a.setStatus(fmt.Sprintf("Health check: %d/%d accounts healthy", rep.HealthyCount, rep.TotalCount), false)
		}
		return a, nil

	case quotaRefreshDoneMsg:
		if msg.err != nil {
			a.setStatus("Quota refresh failed: "+msg.err.Error(), true)
			return a, nil
		}
		live := 0
		for _, q := range msg.report.Accounts {
			if strings.HasSuffix(q.Source, "-live") {
				live++
			}
		}
		a.setStatus(fmt.Sprintf("Updated subscription limits (%d live / %d accounts)", live, len(msg.report.Accounts)), false)
		return a, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}

		if a.modal != modalNone {
			return a.updateModal(msg)
		}

		if a.filtering {
			switch msg.String() {
			case "esc", "enter":
				a.filtering = false
				a.filterText = strings.TrimSpace(a.filterInput.Value())
				a.filterInput.Blur()
				return a, nil
			default:
				var cmd tea.Cmd
				a.filterInput, cmd = a.filterInput.Update(msg)
				a.filterText = strings.TrimSpace(a.filterInput.Value())
				return a, cmd
			}
		}

		switch msg.String() {
		case "?":
			a.modal = modalHelp
			return a, nil
		case "q":
			if a.screen != ScreenDashboard {
				a.screen = ScreenDashboard
				a.clearFilter()
				return a, nil
			}
			return a, tea.Quit
		case "/":
			if a.screen == ScreenAccounts || a.screen == ScreenModels || a.screen == ScreenRouting || a.screen == ScreenLogs {
				a.filtering = true
				a.filterInput.Focus()
				return a, textinput.Blink
			}
		case "i", "W":
			if a.screen == ScreenDashboard || a.screen == ScreenAccounts {
				a.openWelcome()
				return a, nil
			}
		case "C":
			a.openConnectModal()
			return a, nil
		case "S":
			if a.screen == ScreenDashboard {
				return a, a.openServiceModal()
			}
		case "T":
			if a.screen == ScreenDashboard || a.screen == ScreenAccounts {
				a.setStatus("Running health check across all accounts...", false)
				return a, a.runHealthCheckCmd("")
			}
		case "1":
			a.switchScreen(ScreenDashboard)
			return a, nil
		case "2":
			a.switchScreen(ScreenAccounts)
			return a, nil
		case "3":
			a.switchScreen(ScreenModels)
			return a, nil
		case "4":
			a.switchScreen(ScreenRouting)
			return a, nil
		case "5":
			a.switchScreen(ScreenLogs)
			return a, nil
		case "6":
			a.switchScreen(ScreenUsage)
			return a, nil
		case "7":
			a.switchScreen(ScreenSettings)
			return a, nil
		case "tab":
			a.switchScreen(Screen((int(a.screen) + 1) % len(screenNames)))
			return a, nil
		case "shift+tab":
			a.switchScreen(Screen((int(a.screen) + len(screenNames) - 1) % len(screenNames)))
			return a, nil
		}

		switch a.screen {
		case ScreenDashboard:
			return a.updateDashboard(msg)
		case ScreenAccounts:
			return a.updateAccounts(msg)
		case ScreenModels:
			return a.updateModels(msg)
		case ScreenRouting:
			return a.updateRouting(msg)
		case ScreenLogs:
			return a.updateLogs(msg)
		case ScreenUsage:
			return a.updateUsage(msg)
		case ScreenSettings:
			return a.updateSettings(msg)
		}
	}

	return a, nil
}

func (a *App) switchScreen(s Screen) {
	a.screen = s
	a.clearFilter()
}

func (a *App) clearFilter() {
	a.filtering = false
	a.filterText = ""
	a.filterInput.SetValue("")
	a.filterInput.Blur()
}

func (a *App) updateModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch a.modal {
	case modalHelp:
		if msg.String() == "?" || msg.String() == "q" || msg.String() == "esc" || msg.String() == "enter" {
			a.modal = modalNone
		}
		return a, nil

	case modalWelcome:
		return a.updateWelcomeModal(msg)

	case modalInstalling:
		return a.updateInstallModal(msg)

	case modalAddAccount:
		return a.updateAddAccountModal(msg)

	case modalEditAccount:
		return a.updateAccountEditorModal(msg)

	case modalAccountModels:
		return a.updateAccountModelsModal(msg)

	case modalLoginPrompt:
		return a.updateLoginPromptModal(msg)

	case modalConnect:
		return a.updateConnectModal(msg)

	case modalService:
		return a.updateServiceModal(msg)

	case modalAPIKeys:
		return a.updateKeysModal(msg)

	case modalRemoteGuide:
		return a.updateRemoteModal(msg)

	case modalConfirmDelete:
		switch msg.String() {
		case "y", "Y", "enter":
			if a.deleteTarget != nil {
				a.performDeleteAccount(*a.deleteTarget)
			}
			a.deleteTarget = nil
			a.modal = modalNone
		case "n", "N", "q", "esc":
			a.deleteTarget = nil
			a.modal = modalNone
		}
		return a, nil

	case modalSubStrategyWarn:
		switch msg.String() {
		case "y", "Y":
			cfg := a.srv.Config()
			if a.warnRuleIdx >= 0 && a.warnRuleIdx < len(cfg.Routing) {
				cfg.Routing[a.warnRuleIdx].Strategy = a.warnStrategy
				if a.warnHasSub {
					cfg.Routing[a.warnRuleIdx].SubscriptionWarningAcked = true
				}
				_ = a.srv.ReloadConfig(cfg)
				if a.warnHasSub {
					a.setStatus("Enabled "+a.warnStrategy+" (subscription ban-risk warning acknowledged)", false)
				} else {
					a.setStatus("Enabled "+a.warnStrategy+" for API-key rule", false)
				}
			}
			a.modal = modalNone
		case "f", "F":
			cfg := a.srv.Config()
			if a.warnRuleIdx >= 0 && a.warnRuleIdx < len(cfg.Routing) {
				cfg.Routing[a.warnRuleIdx].Strategy = config.StrategyFailover
				_ = a.srv.ReloadConfig(cfg)
				a.setStatus("Kept primary-first failover strategy", false)
			}
			a.modal = modalNone
		case "n", "N", "q", "esc":
			a.modal = modalNone
		}
		return a, nil

	case modalEditText:
		switch msg.String() {
		case "esc":
			a.modal = modalNone
			return a, nil
		case "enter":
			val := strings.TrimSpace(a.editInput.Value())
			fn := a.editOnSubmit
			a.modal = modalNone
			if fn != nil {
				if err := fn(val); err != nil {
					a.setStatus(err.Error(), true)
				}
			}
			return a, nil
		default:
			var cmd tea.Cmd
			a.editInput, cmd = a.editInput.Update(msg)
			return a, cmd
		}

	case modalPermission:
		if a.activePerm == nil {
			a.modal = modalNone
			return a, nil
		}
		switch msg.String() {
		case "y", "Y", "a":
			a.activePerm.ReplyCh <- backend.PermissionDecision{Approved: true}
			a.activePerm = nil
			a.modal = modalNone
			a.setStatus("Approved ACP tool permission", false)
		case "n", "N", "d", "esc", "q":
			a.activePerm.ReplyCh <- backend.PermissionDecision{Approved: false}
			a.activePerm = nil
			a.modal = modalNone
			a.setStatus("Denied ACP tool permission", false)
		}
		return a, nil
	}
	return a, nil
}

func (a *App) openEditModal(title, hint, initial string, onSubmit func(string) error) tea.Cmd {
	a.editTitle = title
	a.editHint = hint
	a.editInput.SetValue(initial)
	a.editInput.CursorEnd()
	a.editInput.Focus()
	a.editOnSubmit = onSubmit
	a.modal = modalEditText
	return textinput.Blink
}

func (a *App) renderTopBar() string {
	brand := a.styles.Brand.Render("◆ apx")

	tabs := make([]string, 0, len(screenNames))
	compact := a.width < 94
	for i, name := range screenNames {
		label := name
		if compact {
			parts := strings.SplitN(name, " ", 2)
			label = parts[0] + ":" + parts[1][:min(len(parts[1]), 4)]
		}
		if Screen(i) == a.screen {
			tabs = append(tabs, a.styles.TabActive.Render(label))
		} else {
			tabs = append(tabs, a.styles.TabInactive.Render(label))
		}
	}
	left := brand + " " + strings.Join(tabs, "")

	right := a.styles.PillStopped.Render("○ stopped")
	if a.srv.Running() {
		label := "● " + a.srv.Addr()
		if a.srv.Attached() {
			label += " (attached)"
		}
		right = a.styles.PillRunning.Render(label)
	}

	gap := max(a.width-lipgloss.Width(left)-lipgloss.Width(right)-2, 1)
	line := left + strings.Repeat(" ", gap) + right
	return a.styles.TopBar.Width(a.width).Render(line)
}

func (a *App) renderBanners() []string {
	var out []string
	for _, ban := range a.srv.LiveBanners() {
		out = append(out, a.styles.BannerCrit.Width(a.width).Render(fmt.Sprintf("! %s: %s", ban.AccountName, ban.Message)))
	}
	if a.srv.Secrets().UsingFileFallback() {
		out = append(out, a.styles.BannerWarn.Width(a.width).Render("Keychain unavailable — API keys stored in 0600 file ("+a.srv.DataDir()+"/secrets.json)"))
	}
	if a.filtering || a.filterText != "" {
		if a.filtering {
			out = append(out, " "+a.styles.Highlight.Render("Filter:")+" "+a.filterInput.View()+"  "+a.styles.Muted.Render("(enter/esc done)"))
		} else {
			out = append(out, " "+a.styles.Muted.Render(fmt.Sprintf("Active filter: %q  (/ edit, switch tab to clear)", a.filterText)))
		}
	}
	return out
}

func (a *App) renderFooter() string {
	var hints []string
	if a.modal != modalNone {
		hints = []string{
			a.styles.KeyHint("esc", "close"),
			a.styles.KeyHint("ctrl+c", "quit"),
		}
	} else {
		switch a.screen {
		case ScreenDashboard:
			hints = []string{
				a.styles.KeyHint("a", "add"),
				a.styles.KeyHint("i", "ACP registry"),
				a.styles.KeyHint("C", "connect"),
				a.styles.KeyHint("S", "service"),
				a.styles.KeyHint("T", "health"),
				a.styles.KeyHint("c", "copy URL"),
				a.styles.KeyHint("?", "help"),
				a.styles.KeyHint("q", "quit"),
			}
		case ScreenAccounts:
			hints = []string{
				a.styles.KeyHint("a", "add"),
				a.styles.KeyHint("e", "edit"),
				a.styles.KeyHint("r", "rename"),
				a.styles.KeyHint("M", "models"),
				a.styles.KeyHint("l", "login"),
				a.styles.KeyHint("t", "test"),
				a.styles.KeyHint("T", "health all"),
				a.styles.KeyHint("C", "connect"),
			}
		case ScreenModels:
			hints = []string{
				a.styles.KeyHint("space", "toggle"),
				a.styles.KeyHint("E", "enable all"),
				a.styles.KeyHint("X", "disable all"),
				a.styles.KeyHint("H", "show off"),
				a.styles.KeyHint("v", "names"),
				a.styles.KeyHint("t", "thinking"),
				a.styles.KeyHint("/", "filter"),
				a.styles.KeyHint("f", "fetch"),
			}
		case ScreenRouting:
			hints = []string{
				a.styles.KeyHint("a", "add rule"),
				a.styles.KeyHint("s", "strategy"),
				a.styles.KeyHint("e", "accounts"),
				a.styles.KeyHint("4", "429-retry"),
				a.styles.KeyHint("x", "del"),
			}
		case ScreenLogs:
			hints = []string{
				a.styles.KeyHint("j/k", "scroll"),
				a.styles.KeyHint("l", "level"),
				a.styles.KeyHint("/", "filter"),
				a.styles.KeyHint("q", "back"),
			}
		case ScreenUsage:
			hints = []string{
				a.styles.KeyHint("R", "refresh limits"),
				a.styles.KeyHint("o", "open web hub"),
				a.styles.KeyHint("u", "copy hub URL"),
				a.styles.KeyHint("t", "24h / 14d"),
				a.styles.KeyHint("q", "back"),
			}
		case ScreenSettings:
			hints = []string{
				a.styles.KeyHint("enter", "edit/toggle"),
				a.styles.KeyHint("v", "reveal key"),
				a.styles.KeyHint("c", "copy key"),
				a.styles.KeyHint("g", "rotate key"),
			}
		}
	}

	left := strings.Join(hints, "  ")
	right := ""
	if a.statusMsg != "" {
		st := a.styles.StateReady
		if a.statusErr {
			st = a.styles.StateErr
		}
		avail := max(a.width-lipgloss.Width(left)-6, 12)
		right = st.Render(truncate(a.statusMsg, avail))
	}
	gap := max(a.width-lipgloss.Width(left)-lipgloss.Width(right)-2, 1)
	return a.styles.FooterBar.Width(a.width).Render(left + strings.Repeat(" ", gap) + right)
}

func (a *App) View() string {
	topBar := a.renderTopBar()
	banners := a.renderBanners()
	footer := a.renderFooter()

	chromeLines := lipgloss.Height(topBar) + len(banners) + lipgloss.Height(footer)
	bodyHeight := max(a.height-chromeLines, 10)

	var body string
	if a.modal != modalNone {
		modalCard := a.viewModal()
		body = a.styles.Place(a.width, bodyHeight, lipgloss.Center, lipgloss.Center, modalCard)
	} else {
		switch a.screen {
		case ScreenDashboard:
			body = a.viewDashboard(bodyHeight)
		case ScreenAccounts:
			body = a.viewAccounts(bodyHeight)
		case ScreenModels:
			body = a.viewModels(bodyHeight)
		case ScreenRouting:
			body = a.viewRouting(bodyHeight)
		case ScreenLogs:
			body = a.viewLogs(bodyHeight)
		case ScreenUsage:
			body = a.viewUsage(bodyHeight)
		case ScreenSettings:
			body = a.viewSettings(bodyHeight)
		}
		body = a.styles.Place(a.width, bodyHeight, lipgloss.Left, lipgloss.Top, body)
	}

	parts := []string{topBar}
	parts = append(parts, banners...)
	parts = append(parts, body, footer)
	return strings.Join(parts, "\n")
}

func (a *App) viewModal() string {
	switch a.modal {
	case modalHelp:
		cardW := clamp(a.width-12, 56, 80)
		return a.styles.Modal.Width(cardW).Render(strings.Join([]string{
			a.styles.Title.Render("apx Keyboard Shortcuts"),
			a.styles.Divider.Render(strings.Repeat("─", cardW-6)),
			"",
			a.styles.Subtitle.Render("Navigation"),
			"  1–6 / Tab     Switch screens   C Connect CLIs/T3 Code   ? Help   q Back/Quit",
			"",
			a.styles.Subtitle.Render("Dashboard"),
			"  a Add account   i Browse & install 45+ ACP tools   T Health check   S Service   c/k Copy",
			"",
			a.styles.Subtitle.Render("Accounts"),
			"  a Add   i Install ACP   m Cycle pool/prefix/both   P Edit prefix (e.g. a-, b-)",
			"  l Login/Key   t Test   T Health all   p Perm   d Toggle   r Rename   e Edit   x Delete",
			"",
			a.styles.Subtitle.Render("Models & Routing"),
			"  Models:  space Toggle   E Enable all   X Disable all   H Show disabled   v Names   / Filter   f Fetch",
			"  Routing: a Add rule   s Cycle strategy (failover/sticky/manual/round-robin/least-busy)   4 429-retry",
			"",
			a.styles.KeyHint("?", "close") + "   " + a.styles.KeyHint("esc", "close"),
		}, "\n"))

	case modalWelcome:
		return a.viewWelcomeModal()

	case modalInstalling:
		return a.viewInstallModal()

	case modalAddAccount:
		return a.viewAddAccountModal()

	case modalEditAccount:
		return a.viewAccountEditorModal()

	case modalAccountModels:
		return a.viewAccountModelsModal()

	case modalLoginPrompt:
		return a.viewLoginPromptModal()

	case modalConnect:
		return a.viewConnectModal()

	case modalService:
		return a.viewServiceModal()

	case modalAPIKeys:
		return a.viewKeysModal()

	case modalRemoteGuide:
		return a.viewRemoteModal()

	case modalConfirmDelete:
		cardW := clamp(a.width-16, 48, 68)
		name := ""
		extra := ""
		if a.deleteTarget != nil {
			name = a.deleteTarget.Backend + "/" + a.deleteTarget.Name
			if a.deleteTarget.Kind == config.KindACP {
				extra = "\nIts isolated profile directory will also be permanently deleted."
			}
		}
		return a.styles.ModalDanger.Width(cardW).Render(strings.Join([]string{
			a.styles.StateErr.Render("Confirm Account Deletion"),
			"",
			fmt.Sprintf("Delete account %q?%s", name, extra),
			"",
			a.styles.KeyHint("y", "delete permanently") + "   " + a.styles.KeyHint("n", "cancel"),
		}, "\n"))

	case modalSubStrategyWarn:
		cardW := clamp(a.width-14, 54, 76)
		if a.warnHasSub {
			return a.styles.ModalDanger.Width(cardW).Render(strings.Join([]string{
				a.styles.StateErr.Render("Subscription Ban-Risk Warning"),
				"",
				fmt.Sprintf("Strategy %q spreads traffic across multiple accounts.", a.warnStrategy),
				"This rule includes subscription-based (ACP/CLI) accounts.",
				"Enabling round-robin or least-busy on subscription accounts can",
				"violate provider Terms of Service and cause account bans.",
				"",
				"Recommended for subscriptions: primary-first failover (press f).",
				"",
				a.styles.KeyHint("y", "enable anyway (accept risk)") + "   " + a.styles.KeyHint("f", "use failover") + "   " + a.styles.KeyHint("n", "cancel"),
			}, "\n"))
		}
		return a.styles.ModalWarn.Width(cardW).Render(strings.Join([]string{
			a.styles.StateWarn.Render("Confirm Load-Balancing Strategy (" + a.warnStrategy + ")"),
			"",
			fmt.Sprintf("Strategy %q is intended for API-key accounts.", a.warnStrategy),
			"WARNING: If used on subscription-based (ACP/CLI) accounts,",
			"spreading requests across accounts can cause provider bans.",
			"apx blocks subscription accounts unless you explicitly opt in.",
			"",
			a.styles.KeyHint("y", "enable "+a.warnStrategy) + "   " + a.styles.KeyHint("f", "use failover") + "   " + a.styles.KeyHint("n", "cancel"),
		}, "\n"))

	case modalEditText:
		cardW := clamp(a.width-14, 50, 72)
		return a.styles.Modal.Width(cardW).Render(strings.Join([]string{
			a.styles.Title.Render(a.editTitle),
			a.styles.Muted.Render(a.editHint),
			"",
			a.editInput.View(),
			"",
			a.styles.KeyHint("enter", "save") + "   " + a.styles.KeyHint("esc", "cancel"),
		}, "\n"))

	case modalPermission:
		if a.activePerm == nil {
			return ""
		}
		cardW := clamp(a.width-12, 54, 76)
		p := a.activePerm
		return a.styles.ModalWarn.Width(cardW).Render(strings.Join([]string{
			a.styles.StateWarn.Render("ACP Agent Permission Request"),
			"",
			fmt.Sprintf("Account: %s", p.AccountName),
			fmt.Sprintf("Action:  %s (%s)", p.Title, p.ToolName),
			"Args:    " + truncate(p.Arguments, cardW-16),
			"",
			a.styles.KeyHint("y", "approve once") + "   " + a.styles.KeyHint("n", "deny"),
		}, "\n"))
	}
	return ""
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	if maxLen <= 1 {
		return string(r[:maxLen])
	}
	return string(r[:maxLen-1]) + "…"
}

func (a *App) testAccountCmd(acct config.AccountConfig) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		models, err := a.srv.TestAccount(ctx, acct)
		return testResultMsg{accountID: acct.ID, models: models, err: err}
	}
}

func (a *App) loginAccountCmd(acct config.AccountConfig) tea.Cmd {
	if acct.Kind == config.KindCLI {
		cmd, _, err := a.srv.CLI().LoginCommand(acct)
		if err != nil {
			return func() tea.Msg { return loginDoneMsg{accountID: acct.ID, err: err} }
		}
		return tea.ExecProcess(cmd, func(err error) tea.Msg {
			return loginDoneMsg{accountID: acct.ID, err: err}
		})
	}
	cmd, _, err := a.srv.ACP().LoginCommand(acct)
	if err != nil {
		return func() tea.Msg { return loginDoneMsg{accountID: acct.ID, err: err} }
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return loginDoneMsg{accountID: acct.ID, err: err}
	})
}

func (a *App) syncRegistryCmd() tea.Cmd {
	return func() tea.Msg {
		count, err := descriptors.SyncRegistry(context.Background(), a.srv.DataDir())
		return registrySyncMsg{count: count, err: err}
	}
}

func (a *App) syncDaemonCmd() tea.Cmd {
	return func() tea.Msg {
		a.srv.SyncFromDaemon()
		return nil
	}
}

func (a *App) runHealthCheckCmd(accountID string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		rep, err := a.srv.CheckHealthNow(ctx, accountID)
		return healthCheckDoneMsg{report: rep, err: err}
	}
}

func (a *App) refreshQuotaCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		rep, err := a.srv.RefreshQuotaNow(ctx, "")
		return quotaRefreshDoneMsg{report: rep, err: err}
	}
}
