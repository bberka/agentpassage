package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/acpx/internal/backend"
	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/descriptors"
	"github.com/bberka/acpx/internal/gateway"
	"github.com/bberka/acpx/internal/logbuf"
)

type Screen int

const (
	ScreenDashboard Screen = iota
	ScreenAccounts
	ScreenModels
	ScreenRouting
	ScreenLogs
	ScreenSettings
)

var screenNames = []string{
	"1:Dashboard",
	"2:Accounts",
	"3:Models",
	"4:Routing",
	"5:Logs",
	"6:Settings",
}

type modalKind int

const (
	modalNone modalKind = iota
	modalHelp
	modalWelcome
	modalAddAccount
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

// App is the root Bubble Tea model for acpx.
type App struct {
	srv    *gateway.Server
	styles Styles
	width  int
	height int

	screen Screen
	modal  modalKind

	// Status message shown in footer
	statusMsg string
	statusErr bool

	// Filter state (shared across filterable screens)
	filtering   bool
	filterInput textinput.Model
	filterText  string

	// Key reveal on dashboard/settings
	revealKey bool

	// Cursors per screen
	acctCursor    int
	modelCursor   int
	showHidden    bool
	routingCursor int
	logCursor     int
	logLevel      string // "", "INFO", "WARN", "ERROR"
	settingCursor int

	// Welcome / Detect wizard state
	detected       []descriptors.DetectedCLI
	detectCursor   int
	detectSelected map[int]bool

	// Add Account wizard state
	addStep      int // 0=choose kind, 1=choose backend/preset, 2=enter details
	addKind      string
	addBackend   string
	addListCur   int
	addFocus     int
	addInputs    []textinput.Model // name, key/custom-cmd, baseURL/cwd, models
	addAuthStyle string
	addTesting   bool

	// Generic single-line text edit modal (rename, alias, port, bind, pattern)
	editTitle    string
	editHint     string
	editInput    textinput.Model
	editOnSubmit func(val string) error

	// Delete confirmation target
	deleteTarget *config.AccountConfig

	// Subscription strategy warning confirmation
	warnRuleIdx  int
	warnStrategy string

	// Live permission approval prompt (when ACP account permission_mode == "ask")
	activePerm *backend.PermissionRequest
	permCh     chan backend.PermissionRequest

	// Live log subscription
	logSubID int
	logSubCh <-chan logbuf.Entry
}

func New(srv *gateway.Server) *App {
	cfg := srv.Config()
	fi := textinput.New()
	fi.Placeholder = "filter..."
	fi.CharLimit = 64

	ei := textinput.New()
	ei.CharLimit = 256

	subID, subCh := srv.Logs().Subscribe()

	a := &App{
		srv:            srv,
		styles:         NewStyles(cfg.UI.Theme),
		width:          80,
		height:         24,
		screen:         ScreenDashboard,
		filterInput:    fi,
		editInput:      ei,
		detectSelected: make(map[int]bool),
		permCh:         make(chan backend.PermissionRequest, 8),
		logSubID:       subID,
		logSubCh:       subCh,
	}

	// Hook ACP interactive permission handler into TUI.
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

	// First run: show Welcome & Detect wizard.
	if !cfg.UI.OnboardingComplete && len(cfg.Accounts) == 0 {
		a.openWelcome()
	}

	return a
}

func (a *App) Close() {
	a.srv.Logs().Unsubscribe(a.logSubID)
	a.srv.ACP().SetPermissionHandler(nil)
}

func (a *App) Init() tea.Cmd {
	return tea.Batch(
		tickCmd(),
		waitLogCmd(a.logSubCh),
		waitPermCmd(a.permCh),
	)
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
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
		a.setStatus(fmt.Sprintf("%s: %s (clipboard unavailable: %v)", label, text, err), false)
		return
	}
	a.setStatus(fmt.Sprintf("Copied %s to clipboard", label), false)
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = max(msg.Width, 40)
		a.height = max(msg.Height, 16)
		return a, nil

	case tickMsg:
		return a, tickCmd()

	case logEntryMsg:
		return a, waitLogCmd(a.logSubCh)

	case permReqMsg:
		req := backend.PermissionRequest(msg)
		a.activePerm = &req
		a.modal = modalPermission
		return a, waitPermCmd(a.permCh)

	case testResultMsg:
		a.addTesting = false
		if msg.err != nil {
			a.setStatus("Test failed: "+msg.err.Error(), true)
			return a, nil
		}
		cfg := a.srv.Config()
		if acct := cfg.FindAccount(msg.accountID); acct != nil {
			if len(msg.models) > 0 {
				acct.Models = msg.models
			}
			a.srv.Pool().ClearHealth(acct.ID)
			_ = a.srv.ReloadConfig(cfg)
			a.setStatus(fmt.Sprintf("Account %s OK (%d models)", acct.Name, len(acct.Models)), false)
		} else {
			a.setStatus(fmt.Sprintf("Connection OK (%d models)", len(msg.models)), false)
		}
		return a, nil

	case loginDoneMsg:
		if msg.err != nil {
			a.setStatus("Login exited with error: "+msg.err.Error(), true)
		} else {
			a.srv.Pool().ClearHealth(msg.accountID)
			a.setStatus("Login finished. Press 't' on Accounts to test.", false)
		}
		return a, nil

	case tea.KeyMsg:
		// Global quit on Ctrl+C
		if msg.String() == "ctrl+c" {
			return a, tea.Quit
		}

		// Active modal handles keys first
		if a.modal != modalNone {
			return a.updateModal(msg)
		}

		// Filter bar input mode
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

		// Shared keys everywhere
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
			a.switchScreen(ScreenSettings)
			return a, nil
		case "tab":
			a.switchScreen(Screen((int(a.screen) + 1) % len(screenNames)))
			return a, nil
		case "shift+tab":
			a.switchScreen(Screen((int(a.screen) + len(screenNames) - 1) % len(screenNames)))
			return a, nil
		}

		// Dispatch to active screen
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

	case modalAddAccount:
		return a.updateAddAccountModal(msg)

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
				cfg.Routing[a.warnRuleIdx].SubscriptionWarningAcked = true
				_ = a.srv.ReloadConfig(cfg)
				a.setStatus("Updated routing strategy to "+a.warnStrategy+" (warning acknowledged)", false)
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

func (a *App) View() string {
	var b strings.Builder

	// Top tab bar (fits within 80 columns)
	tabs := make([]string, 0, len(screenNames))
	for i, name := range screenNames {
		if Screen(i) == a.screen {
			tabs = append(tabs, a.styles.TabActive.Render(name))
		} else {
			tabs = append(tabs, a.styles.TabInactive.Render(name))
		}
	}
	header := a.styles.Header.Render("acpx") + " " + strings.Join(tabs, "")
	b.WriteString(header + "\n")

	// Health / security banners
	for _, ban := range a.srv.Pool().Banners() {
		b.WriteString(a.styles.BannerCrit.Render(fmt.Sprintf("! %s: %s", ban.AccountName, ban.Message)) + "\n")
	}
	if a.srv.Secrets().UsingFileFallback() {
		b.WriteString(a.styles.BannerWarn.Render("Keychain unavailable — API keys stored in 0600 file") + "\n")
	}

	// Optional filter bar
	if a.filtering || a.filterText != "" {
		if a.filtering {
			b.WriteString("Filter: " + a.filterInput.View() + " (enter/esc done)\n")
		} else {
			b.WriteString(a.styles.Muted.Render(fmt.Sprintf("Filter: %q  (/ edit)", a.filterText)) + "\n")
		}
	}

	// Main content or modal
	bodyHeight := max(a.height-5, 8)
	var content string
	if a.modal != modalNone {
		content = a.viewModal()
	} else {
		switch a.screen {
		case ScreenDashboard:
			content = a.viewDashboard(bodyHeight)
		case ScreenAccounts:
			content = a.viewAccounts(bodyHeight)
		case ScreenModels:
			content = a.viewModels(bodyHeight)
		case ScreenRouting:
			content = a.viewRouting(bodyHeight)
		case ScreenLogs:
			content = a.viewLogs(bodyHeight)
		case ScreenSettings:
			content = a.viewSettings(bodyHeight)
		}
	}
	b.WriteString(content)

	// Footer status bar
	b.WriteString("\n")
	if a.statusMsg != "" {
		st := a.styles.StateReady
		if a.statusErr {
			st = a.styles.StateErr
		}
		b.WriteString(st.Render(truncate(a.statusMsg, a.width-2)) + "\n")
	}
	footer := a.styles.StatusBar.Render("1-6/Tab:screens  ?:help  /:filter  q:back/quit")
	b.WriteString(truncate(footer, a.width))
	return b.String()
}

func (a *App) viewModal() string {
	switch a.modal {
	case modalHelp:
		return a.styles.Modal.Width(min(a.width-4, 74)).Render(strings.Join([]string{
			a.styles.Title.Render("acpx Keyboard Shortcuts"),
			"",
			"Global:   1-6 or Tab switch screen   ? help   / filter   q back/quit",
			"Dashboard: c copy URL   k copy key   v reveal key   a add account   W detect",
			"Accounts:  a add  e edit  r rename  d disable  x delete  l login  t test  p perm",
			"Models:    r rename  a set alias  h hide/unhide  H toggle hidden",
			"Routing:   a add rule  s cycle strategy  4 toggle 429 retry  x delete",
			"Logs:      / filter  l cycle level (ALL/INFO/WARN/ERROR)",
			"Settings:  enter edit selected  v reveal key  c copy key  g rotate key",
			"",
			a.styles.Muted.Render("Press ? or Esc to close"),
		}, "\n"))

	case modalWelcome:
		return a.viewWelcomeModal()

	case modalAddAccount:
		return a.viewAddAccountModal()

	case modalConfirmDelete:
		name := ""
		extra := ""
		if a.deleteTarget != nil {
			name = a.deleteTarget.Backend + "/" + a.deleteTarget.Name
			if a.deleteTarget.Kind == config.KindACP {
				extra = "\nThis will also permanently delete its isolated profile directory."
			}
		}
		return a.styles.Modal.Width(min(a.width-4, 68)).Render(
			a.styles.StateErr.Render("Confirm Account Deletion") + "\n\n" +
				fmt.Sprintf("Delete account %q?%s\n\n", name, extra) +
				"[y] Delete   [n] Cancel",
		)

	case modalSubStrategyWarn:
		return a.styles.Modal.Width(min(a.width-4, 72)).Render(strings.Join([]string{
			a.styles.StateWarn.Render("Subscription Pool Warning"),
			"",
			fmt.Sprintf("Strategy %q spreads traffic across multiple subscription accounts.", a.warnStrategy),
			"Pooling subscription accounts can violate provider terms of service",
			"and lead to account restrictions or bans.",
			"",
			"Only proceed if you understand the risk and opt in explicitly.",
			"",
			"[y] I understand, enable " + a.warnStrategy + "   [n] Cancel",
		}, "\n"))

	case modalEditText:
		return a.styles.Modal.Width(min(a.width-4, 68)).Render(
			a.styles.Title.Render(a.editTitle) + "\n" +
				a.styles.Muted.Render(a.editHint) + "\n\n" +
				a.editInput.View() + "\n\n" +
				a.styles.Muted.Render("Enter: save   Esc: cancel"),
		)

	case modalPermission:
		if a.activePerm == nil {
			return ""
		}
		p := a.activePerm
		return a.styles.Modal.Width(min(a.width-4, 74)).Render(strings.Join([]string{
			a.styles.StateWarn.Render("ACP Agent Permission Request"),
			fmt.Sprintf("Account: %s", p.AccountName),
			fmt.Sprintf("Action:  %s (%s)", p.Title, p.ToolName),
			"Args:    " + truncate(p.Arguments, 60),
			"",
			"[y] Approve once   [n] Deny",
		}, "\n"))
	}
	return ""
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
	cmd, _, err := a.srv.ACP().LoginCommand(acct)
	if err != nil {
		return func() tea.Msg { return loginDoneMsg{accountID: acct.ID, err: err} }
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return loginDoneMsg{accountID: acct.ID, err: err}
	})
}
