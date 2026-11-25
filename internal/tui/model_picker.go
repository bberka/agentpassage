package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/acpx/internal/config"
)

type fetchModelsMsg struct {
	accountID string // empty when fetching during Add Account wizard Step 4
	models    []string
	err       error
}

type fetchAllModelsMsg struct {
	updatedAccounts int
	totalModels     int
	errors          []string
}

type modelPickerState struct {
	accountID   string // non-empty when editing an existing account; empty during Add Account wizard
	title       string
	models      []string        // full sorted catalog
	enabled     map[string]bool // modelID -> enabled
	cursor      int
	loading     bool
	fetchErr    string
	searching   bool
	searchInput textinput.Model
	addingModel bool
	addInput    textinput.Model
}

func newModelPicker(accountID, title string, catalog, enabledList []string) *modelPickerState {
	si := textinput.New()
	si.Prompt = ""
	si.Placeholder = "search models..."
	si.CharLimit = 64

	ai := textinput.New()
	ai.Prompt = ""
	ai.Placeholder = "enter model id (e.g. deepseek-chat)"
	ai.CharLimit = 128

	en := make(map[string]bool, len(enabledList))
	for _, m := range enabledList {
		if m = strings.TrimSpace(m); m != "" {
			en[m] = true
		}
	}

	seen := make(map[string]bool)
	var all []string
	for _, list := range [][]string{catalog, enabledList} {
		for _, m := range list {
			m = strings.TrimSpace(m)
			if m != "" && !seen[m] {
				seen[m] = true
				all = append(all, m)
			}
		}
	}
	sort.Strings(all)

	return &modelPickerState{
		accountID:   accountID,
		title:       title,
		models:      all,
		enabled:     en,
		searchInput: si,
		addInput:    ai,
	}
}

func (p *modelPickerState) mergeFetched(fetched []string, enableNew bool) {
	seen := make(map[string]bool, len(p.models))
	for _, m := range p.models {
		seen[m] = true
	}
	for _, m := range fetched {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if !seen[m] {
			seen[m] = true
			p.models = append(p.models, m)
			if enableNew {
				p.enabled[m] = true
			}
		}
	}
	sort.Strings(p.models)
}

func (p *modelPickerState) filtered() []string {
	q := strings.ToLower(strings.TrimSpace(p.searchInput.Value()))
	if q == "" {
		return p.models
	}
	var out []string
	for _, m := range p.models {
		if strings.Contains(strings.ToLower(m), q) {
			out = append(out, m)
		}
	}
	return out
}

func (p *modelPickerState) enabledCount() int {
	n := 0
	for _, m := range p.models {
		if p.enabled[m] {
			n++
		}
	}
	return n
}

func (p *modelPickerState) toLists() (catalog, enabled, disabled []string) {
	catalog = append([]string(nil), p.models...)
	for _, m := range p.models {
		if p.enabled[m] {
			enabled = append(enabled, m)
		} else {
			disabled = append(disabled, m)
		}
	}
	return catalog, enabled, disabled
}

func (a *App) fetchModelsCmd(accountID string, acct config.AccountConfig, apiKey string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		models, err := a.srv.FetchModelsWithKey(ctx, acct, apiKey)
		return fetchModelsMsg{accountID: accountID, models: models, err: err}
	}
}

func (a *App) fetchAllAccountsModelsCmd() tea.Cmd {
	return func() tea.Msg {
		cfg := a.srv.Config()
		updated := 0
		total := 0
		var errs []string
		for i := range cfg.Accounts {
			acct := &cfg.Accounts[i]
			if !acct.Enabled {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			models, err := a.srv.TestAccount(ctx, *acct)
			cancel()
			if err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", acct.Name, err))
				continue
			}
			if len(models) > 0 {
				acct.MergeDiscoveredModels(models)
				updated++
				total += len(models)
			}
		}
		if updated > 0 {
			_ = a.srv.ReloadConfig(cfg)
		}
		return fetchAllModelsMsg{updatedAccounts: updated, totalModels: total, errors: errs}
	}
}

func (a *App) openAccountModelsModal(acct config.AccountConfig) {
	catalog := acct.AvailableModels
	if len(catalog) == 0 {
		catalog = acct.Models
	}
	a.modelPicker = newModelPicker(acct.ID, "Models — "+acct.Backend+"/"+acct.Name, catalog, acct.Models)
	a.modal = modalAccountModels
}

func (a *App) updateAccountModelsModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := a.modelPicker
	if p == nil {
		a.modal = modalNone
		return a, nil
	}

	if p.addingModel {
		switch msg.String() {
		case "esc":
			p.addingModel = false
			p.addInput.Blur()
			return a, nil
		case "enter":
			val := strings.TrimSpace(p.addInput.Value())
			if val != "" {
				p.mergeFetched([]string{val}, true)
				p.enabled[val] = true
			}
			p.addingModel = false
			p.addInput.SetValue("")
			p.addInput.Blur()
			return a, nil
		default:
			var cmd tea.Cmd
			p.addInput, cmd = p.addInput.Update(msg)
			return a, cmd
		}
	}

	if p.searching {
		switch msg.String() {
		case "esc", "enter":
			p.searching = false
			p.searchInput.Blur()
			p.cursor = 0
			return a, nil
		default:
			var cmd tea.Cmd
			p.searchInput, cmd = p.searchInput.Update(msg)
			p.cursor = 0
			return a, cmd
		}
	}

	items := p.filtered()
	if p.cursor >= len(items) && len(items) > 0 {
		p.cursor = len(items) - 1
	}

	switch msg.String() {
	case "esc", "q":
		a.saveAccountModelsFromPicker()
		a.modelPicker = nil
		a.modal = modalNone
		return a, nil
	case "enter":
		a.saveAccountModelsFromPicker()
		a.modelPicker = nil
		a.modal = modalNone
		return a, nil
	case "/":
		p.searching = true
		p.searchInput.Focus()
		return a, textinput.Blink
	case "+":
		p.addingModel = true
		p.addInput.SetValue("")
		p.addInput.Focus()
		return a, textinput.Blink
	case "up", "k":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down", "j":
		if p.cursor < len(items)-1 {
			p.cursor++
		}
	case "pgup":
		p.cursor = max(p.cursor-10, 0)
	case "pgdown":
		if len(items) > 0 {
			p.cursor = min(p.cursor+10, len(items)-1)
		}
	case " ":
		if p.cursor >= 0 && p.cursor < len(items) {
			id := items[p.cursor]
			p.enabled[id] = !p.enabled[id]
		}
	case "a":
		// Enable all currently visible (or all) models
		for _, id := range items {
			p.enabled[id] = true
		}
		a.setStatus(fmt.Sprintf("Enabled %d model(s)", len(items)), false)
	case "n":
		// Disable all currently visible (or all) models
		for _, id := range items {
			p.enabled[id] = false
		}
		a.setStatus(fmt.Sprintf("Disabled %d model(s)", len(items)), false)
	case "x", "delete":
		// Remove selected model from this account's catalog
		if p.cursor >= 0 && p.cursor < len(items) {
			target := items[p.cursor]
			delete(p.enabled, target)
			kept := p.models[:0]
			for _, m := range p.models {
				if m != target {
					kept = append(kept, m)
				}
			}
			p.models = kept
			a.setStatus("Removed model "+target, false)
		}
	case "f":
		cfg := a.srv.Config()
		if acct := cfg.FindAccount(p.accountID); acct != nil {
			p.loading = true
			p.fetchErr = ""
			a.setStatus("Fetching all models from "+acct.Backend+"/"+acct.Name+"...", false)
			return a, a.fetchModelsCmd(acct.ID, *acct, "")
		}
	}
	return a, nil
}

func (a *App) saveAccountModelsFromPicker() {
	p := a.modelPicker
	if p == nil || p.accountID == "" {
		return
	}
	cfg := a.srv.Config()
	if acct := cfg.FindAccount(p.accountID); acct != nil {
		catalog, enabled, disabled := p.toLists()
		acct.AvailableModels = catalog
		acct.Models = enabled
		acct.DisabledModels = disabled
		_ = a.srv.ReloadConfig(cfg)
		a.setStatus(fmt.Sprintf("Saved %s models (%d enabled / %d total)", acct.Name, len(enabled), len(catalog)), false)
	}
}

func (a *App) renderModelPickerBody(p *modelPickerState, innerW, maxRows int) []string {
	var lines []string
	spinFrames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

	if p.loading {
		frame := spinFrames[a.spinTick%len(spinFrames)]
		lines = append(lines, a.styles.StateBusy.Render(frame+" Loading all models from provider..."))
	} else if p.fetchErr != "" {
		lines = append(lines, a.styles.StateWarn.Render("Fetch note: "+truncate(p.fetchErr, innerW-12)))
	}

	if p.addingModel {
		lines = append(lines, a.styles.Highlight.Render("Add model ID: ")+p.addInput.View()+" "+a.styles.Muted.Render("(enter/esc)"))
	} else if p.searching {
		lines = append(lines, a.styles.Highlight.Render("Search: ")+p.searchInput.View()+" "+a.styles.Muted.Render("(enter/esc)"))
	} else if q := strings.TrimSpace(p.searchInput.Value()); q != "" {
		lines = append(lines, a.styles.Muted.Render(fmt.Sprintf("Filter: %q  (/ edit)", q)))
	}

	items := p.filtered()
	if len(items) == 0 {
		lines = append(lines, "")
		lines = append(lines, a.styles.Muted.Render("  No models listed yet. Press 'f' to fetch from provider or '+' to add one manually."))
		for i := 2; i < maxRows; i++ {
			lines = append(lines, "")
		}
		return lines
	}

	start := 0
	if p.cursor >= maxRows {
		start = p.cursor - maxRows + 1
	}
	end := min(start+maxRows, len(items))

	for i := start; i < end; i++ {
		id := items[i]
		mark := a.styles.Muted.Render("[ ]")
		state := a.styles.Muted.Render("disabled")
		if p.enabled[id] {
			mark = a.styles.StateReady.Render("[x]")
			state = a.styles.StateReady.Render("enabled")
		}
		row := fmt.Sprintf("%s  %-44s %s", mark, truncate(id, max(innerW-20, 20)), state)
		if i == p.cursor {
			lines = append(lines, a.styles.Selected.Width(innerW).Render("> "+row))
		} else {
			lines = append(lines, "  "+row)
		}
	}
	for i := end - start; i < maxRows; i++ {
		lines = append(lines, "")
	}
	return lines
}

func (a *App) viewAccountModelsModal() string {
	p := a.modelPicker
	if p == nil {
		return ""
	}
	cardW := clamp(a.width-10, 62, 90)
	innerW := cardW - 6

	summary := fmt.Sprintf("%d enabled / %d total", p.enabledCount(), len(p.models))
	var lines []string
	lines = append(lines, a.styles.Title.Render("◆ "+p.title)+"   "+a.styles.Muted.Render(summary))
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))

	maxRows := clamp(a.height-16, 6, 15)
	lines = append(lines, a.renderModelPickerBody(p, innerW, maxRows)...)

	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
	lines = append(lines, strings.Join([]string{
		a.styles.KeyHint("space", "toggle"),
		a.styles.KeyHint("a", "enable all"),
		a.styles.KeyHint("n", "disable all"),
		a.styles.KeyHint("f", "fetch all"),
		a.styles.KeyHint("/", "search"),
		a.styles.KeyHint("+", "add"),
		a.styles.KeyHint("x", "remove"),
		a.styles.KeyHint("enter", "save"),
	}, "  "))
	return a.styles.Modal.Width(cardW).Render(strings.Join(lines, "\n"))
}
