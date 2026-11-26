package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/agentpassage/internal/config"
)

type accountEditorState struct {
	accountID      string
	origName       string
	kind           string
	backend        string
	focus          int // 0=name, 1=url/cwd, 2=prefix, 3=concurrency, 4=expose(cycle), 5=perm(cycle), 6=profile(cycle)
	inputs         []textinput.Model
	exposeMode     string
	permissionMode string
	useHostProfile bool
}

func (a *App) openAccountEditor(acct config.AccountConfig) tea.Cmd {
	makeInput := func(val, ph string) textinput.Model {
		ti := textinput.New()
		ti.Prompt = ""
		ti.CharLimit = 256
		ti.Placeholder = ph
		ti.SetValue(val)
		ti.CursorEnd()
		return ti
	}

	urlOrCwd := acct.BaseURL
	urlOrCwdPH := "https://api.openai.com/v1"
	if acct.Kind != config.KindAPIKey {
		urlOrCwd = acct.DefaultCwd
		urlOrCwdPH = "default working directory (optional)"
	}

	expose := acct.ExposeMode
	if expose == "" {
		expose = config.ExposePool
	}

	dailyStr := ""
	if acct.DailyBudgetUSD > 0 {
		dailyStr = strconv.FormatFloat(acct.DailyBudgetUSD, 'f', -1, 64)
	}
	monthlyStr := ""
	if acct.MonthlyBudgetUSD > 0 {
		monthlyStr = strconv.FormatFloat(acct.MonthlyBudgetUSD, 'f', -1, 64)
	}

	inputs := []textinput.Model{
		makeInput(acct.Name, "account name (e.g. a, b, work)"),
		makeInput(urlOrCwd, urlOrCwdPH),
		makeInput(acct.ModelPrefix, "model prefix (e.g. a-, b-)"),
		makeInput(strconv.Itoa(acct.MaxConcurrency), "max concurrent requests"),
		makeInput(dailyStr, "daily USD budget (blank = unlimited)"),
		makeInput(monthlyStr, "monthly USD budget (blank = unlimited)"),
	}
	inputs[0].Focus()

	a.acctEditor = &accountEditorState{
		accountID:      acct.ID,
		origName:       acct.Name,
		kind:           acct.Kind,
		backend:        acct.Backend,
		focus:          0,
		inputs:         inputs,
		exposeMode:     expose,
		permissionMode: acct.PermissionMode,
		useHostProfile: acct.UseHostProfile,
	}
	a.modal = modalEditAccount
	return textinput.Blink
}

func (ed *accountEditorState) fieldCount() int {
	if ed.kind == config.KindAPIKey {
		return 7 // name, baseURL, prefix, concurrency, dailyBudget, monthlyBudget, exposeMode
	}
	return 9 // name, cwd, prefix, concurrency, dailyBudget, monthlyBudget, exposeMode, permMode, hostProfile
}

func (a *App) updateAccountEditorModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	ed := a.acctEditor
	if ed == nil {
		a.modal = modalNone
		return a, nil
	}
	total := ed.fieldCount()

	moveFocus := func(delta int) tea.Cmd {
		if ed.focus < len(ed.inputs) {
			ed.inputs[ed.focus].Blur()
		}
		ed.focus = (ed.focus + delta + total) % total
		if ed.focus < len(ed.inputs) {
			ed.inputs[ed.focus].Focus()
			return textinput.Blink
		}
		return nil
	}

	switch msg.String() {
	case "esc":
		a.acctEditor = nil
		a.modal = modalNone
		return a, nil

	case "tab", "down":
		return a, moveFocus(1)

	case "shift+tab", "up":
		return a, moveFocus(-1)

	case "ctrl+s":
		return a.saveAccountEditor()

	case "M":
		// Jump directly from Account Editor into the interactive Model Picker (not text!)
		if ed.focus >= len(ed.inputs) {
			cfg := a.srv.Config()
			if acct := cfg.FindAccount(ed.accountID); acct != nil {
				a.acctEditor = nil
				return a, a.openAccountModelsModal(*acct)
			}
		}

	case " ", "left", "right":
		if ed.focus >= len(ed.inputs) {
			switch ed.focus {
			case 6: // exposeMode
				switch ed.exposeMode {
				case config.ExposePool:
					ed.exposeMode = config.ExposePrefix
				case config.ExposePrefix:
					ed.exposeMode = config.ExposeBoth
				default:
					ed.exposeMode = config.ExposePool
				}
			case 7: // permissionMode
				switch ed.permissionMode {
				case config.PermDeny:
					ed.permissionMode = config.PermAsk
				case config.PermAsk:
					ed.permissionMode = config.PermAutoApprove
				default:
					ed.permissionMode = config.PermDeny
				}
			case 8: // useHostProfile
				ed.useHostProfile = !ed.useHostProfile
			}
			return a, nil
		}

	case "enter":
		if ed.focus < total-1 {
			return a, moveFocus(1)
		}
		return a.saveAccountEditor()
	}

	if ed.focus < len(ed.inputs) {
		var cmd tea.Cmd
		ed.inputs[ed.focus], cmd = ed.inputs[ed.focus].Update(msg)
		return a, cmd
	}
	return a, nil
}

func (a *App) saveAccountEditor() (tea.Model, tea.Cmd) {
	ed := a.acctEditor
	if ed == nil {
		a.modal = modalNone
		return a, nil
	}

	newName := strings.TrimSpace(ed.inputs[0].Value())
	if err := config.ValidateName(newName); err != nil {
		a.setStatus(err.Error(), true)
		return a, nil
	}

	urlOrCwd := strings.TrimSpace(ed.inputs[1].Value())
	if ed.kind == config.KindAPIKey && urlOrCwd == "" {
		a.setStatus("Base URL cannot be empty", true)
		return a, nil
	}

	prefix := strings.TrimSpace(ed.inputs[2].Value())
	maxConc, err := strconv.Atoi(strings.TrimSpace(ed.inputs[3].Value()))
	if err != nil || maxConc < 1 || maxConc > 64 {
		a.setStatus("Max concurrency must be between 1 and 64", true)
		return a, nil
	}
	var dailyBudget, monthlyBudget float64
	if s := strings.TrimSpace(ed.inputs[4].Value()); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || v < 0 {
			a.setStatus("Daily budget must be a non-negative number", true)
			return a, nil
		}
		dailyBudget = v
	}
	if s := strings.TrimSpace(ed.inputs[5].Value()); s != "" {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil || v < 0 {
			a.setStatus("Monthly budget must be a non-negative number", true)
			return a, nil
		}
		monthlyBudget = v
	}

	if newName != ed.origName {
		if err := a.renameAccount(ed.accountID, newName); err != nil {
			a.setStatus("Rename failed: "+err.Error(), true)
			return a, nil
		}
		ed.accountID = config.SanitizeSlug(ed.backend + "-" + newName)
	}

	var postCmd tea.Cmd
	cfg := a.srv.Config()
	if acct := cfg.FindAccount(ed.accountID); acct != nil {
		urlChanged := false
		if acct.Kind == config.KindAPIKey {
			urlChanged = acct.BaseURL != urlOrCwd
			acct.BaseURL = urlOrCwd
		} else {
			acct.DefaultCwd = urlOrCwd
			acct.PermissionMode = ed.permissionMode
			if acct.UseHostProfile != ed.useHostProfile && acct.Kind == config.KindACP {
				a.srv.ACP().StopAccount(acct.ID)
			}
			acct.UseHostProfile = ed.useHostProfile
		}
		acct.ModelPrefix = prefix
		acct.ExposeMode = ed.exposeMode
		acct.MaxConcurrency = maxConc
		acct.DailyBudgetUSD = dailyBudget
		acct.MonthlyBudgetUSD = monthlyBudget
		_ = a.srv.ReloadConfig(cfg)
		if urlChanged {
			a.setStatus(fmt.Sprintf("Saved %s/%s — fetching models from updated URL...", acct.Backend, acct.Name), false)
			postCmd = a.testAccountCmd(*acct)
		} else {
			a.setStatus(fmt.Sprintf("Saved account %s/%s", acct.Backend, acct.Name), false)
		}
	}

	a.acctEditor = nil
	a.modal = modalNone
	return a, postCmd
}

// renameAccount safely renames an account, moves its isolated profile dir on disk,
// migrates its secret store key, updates routing rule references, and reloads config.
func (a *App) renameAccount(accountID, newName string) error {
	if err := config.ValidateName(newName); err != nil {
		return err
	}
	cfg := a.srv.Config()
	acct := cfg.FindAccount(accountID)
	if acct == nil {
		return fmt.Errorf("account %q not found", accountID)
	}
	if acct.Name == newName {
		return nil
	}

	newID := config.SanitizeSlug(acct.Backend + "-" + newName)
	if existing := cfg.FindAccount(newID); existing != nil && existing.ID != acct.ID {
		return fmt.Errorf("account %q already exists", acct.Backend+"/"+newName)
	}

	oldID := acct.ID
	oldName := acct.Name

	// Stop live ACP worker before moving its profile directory
	if acct.Kind == config.KindACP {
		a.srv.ACP().StopAccount(oldID)
	}

	// Move isolated profile dir if it exists
	if acct.Kind == config.KindACP || acct.Kind == config.KindCLI {
		oldDir, err1 := config.ProfileDir(a.srv.DataDir(), acct.Backend, oldName)
		newDir, err2 := config.ProfileDir(a.srv.DataDir(), acct.Backend, newName)
		if err1 == nil && err2 == nil {
			if _, statErr := os.Stat(oldDir); statErr == nil {
				if err := os.Rename(oldDir, newDir); err != nil {
					return fmt.Errorf("move profile dir: %w", err)
				}
			}
		}
	}

	// Migrate API key secret under new account ID
	if acct.Kind == config.KindAPIKey {
		if secVal, err := a.srv.Secrets().Get(oldID); err == nil && secVal != "" {
			if err := a.srv.Secrets().Set(newID, secVal); err != nil {
				return fmt.Errorf("migrate API key: %w", err)
			}
			_ = a.srv.Secrets().Delete(oldID)
		}
	}

	// Update routing rules and model overrides referencing old ID/name
	for i := range cfg.Routing {
		for j, ref := range cfg.Routing[i].Accounts {
			if ref == oldID || ref == oldName || ref == acct.Backend+"/"+oldName {
				cfg.Routing[i].Accounts[j] = newID
			}
		}
	}
	oldPrefix := acct.Backend + "/" + oldName + "/"
	newPrefix := acct.Backend + "/" + newName + "/"
	for i := range cfg.Models {
		if strings.HasPrefix(cfg.Models[i].ID, oldPrefix) {
			cfg.Models[i].ID = newPrefix + strings.TrimPrefix(cfg.Models[i].ID, oldPrefix)
		}
	}

	acct.ID = newID
	acct.Name = newName
	return a.srv.ReloadConfig(cfg)
}

func (a *App) viewAccountEditorModal() string {
	ed := a.acctEditor
	if ed == nil {
		return ""
	}
	cardW := clamp(a.width-12, 58, 82)
	innerW := cardW - 6

	urlLabel := "Base URL"
	if ed.kind != config.KindAPIKey {
		urlLabel = "Default Cwd"
	}

	var lines []string
	lines = append(lines, a.styles.Title.Render("◆ Edit Account — "+ed.backend+"/"+ed.origName)+"  "+a.styles.Muted.Render("("+ed.kind+")"))
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
	lines = append(lines, "")

	inputLabels := []string{"Name", urlLabel, "Model Prefix", "Concurrency", "Daily Budget $", "Monthly Budget $"}
	for i, lbl := range inputLabels {
		prefix := "  "
		lStyle := a.styles.Muted
		if ed.focus == i {
			prefix = a.styles.Highlight.Render("› ")
			lStyle = a.styles.Highlight
		}
		lines = append(lines, fmt.Sprintf("%s%-16s %s", prefix, lStyle.Render(lbl+":"), ed.inputs[i].View()))
	}

	renderToggle := func(idx int, lbl, val, hint string) {
		prefix := "  "
		lStyle := a.styles.Muted
		vStyle := a.styles.Title
		if ed.focus == idx {
			prefix = a.styles.Highlight.Render("› ")
			lStyle = a.styles.Highlight
			vStyle = a.styles.Highlight
		}
		lines = append(lines, fmt.Sprintf("%s%-16s %s  %s", prefix, lStyle.Render(lbl+":"), vStyle.Render("◀ "+val+" ▶"), a.styles.Muted.Render(hint)))
	}

	renderToggle(6, "Expose Mode", ed.exposeMode, "(space: pool / prefix / both)")
	if ed.kind != config.KindAPIKey {
		renderToggle(7, "Permissions", ed.permissionMode, "(space: deny / ask / auto-approve)")
		prof := "isolated profile"
		if ed.useHostProfile {
			prof = "host CLI login"
		}
		renderToggle(8, "Profile", prof, "(space: isolated / host)")
	}

	lines = append(lines, "")
	lines = append(lines, a.styles.Muted.Render("Tip: Press M or Enter on Accounts to manage models with the interactive picker."))
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
	lines = append(lines, a.styles.JoinHints(
		a.styles.KeyHint("tab", "next field"),
		a.styles.KeyHint("space", "toggle"),
		a.styles.KeyHint("ctrl+s", "save"),
		a.styles.KeyHint("esc", "cancel"),
	))
	return a.styles.Modal.Width(cardW).Render(strings.Join(lines, "\n"))
}
