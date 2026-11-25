package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/acpx/internal/config"
)

func (a *App) openWelcome() {
	a.detected = a.srv.Registry().DetectInstalled()
	a.detectCursor = 0
	a.detectSelected = make(map[int]bool)
	for i, d := range a.detected {
		if d.Found {
			a.detectSelected[i] = true
		}
	}
	a.modal = modalWelcome
}

func (a *App) updateWelcomeModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if a.detectCursor > 0 {
			a.detectCursor--
		}
	case "down", "j":
		if a.detectCursor < len(a.detected)-1 {
			a.detectCursor++
		}
	case " ":
		if a.detectCursor >= 0 && a.detectCursor < len(a.detected) && a.detected[a.detectCursor].Found {
			a.detectSelected[a.detectCursor] = !a.detectSelected[a.detectCursor]
		}
	case "enter":
		cfg := a.srv.Config()
		added := 0
		for i, d := range a.detected {
			if !a.detectSelected[i] || !d.Found {
				continue
			}
			id := config.SanitizeSlug(d.Descriptor.ID + "-default")
			if cfg.FindAccount(id) != nil {
				continue
			}
			_, _ = config.EnsureProfileDir(a.srv.DataDir(), d.Descriptor.ID, "default")
			cfg.Accounts = append(cfg.Accounts, config.AccountConfig{
				ID:             id,
				Name:           "default",
				Kind:           config.KindACP,
				Backend:        d.Descriptor.ID,
				Enabled:        true,
				PermissionMode: config.PermDeny,
				Models:         append([]string(nil), d.Descriptor.DefaultModels...),
				MaxConcurrency: 1,
			})
			added++
		}
		cfg.UI.OnboardingComplete = true
		_ = a.srv.ReloadConfig(cfg)
		a.modal = modalNone
		if added > 0 {
			a.setStatus(fmt.Sprintf("Added %d detected ACP tool(s). Go to Accounts (2) and press 'l' to log in.", added), false)
		} else {
			a.setStatus("Welcome complete. Press 'a' to add an API-key or ACP account.", false)
		}
	case "a":
		cfg := a.srv.Config()
		cfg.UI.OnboardingComplete = true
		_ = a.srv.ReloadConfig(cfg)
		a.openAddAccount()
	case "esc", "q", "s":
		cfg := a.srv.Config()
		cfg.UI.OnboardingComplete = true
		_ = a.srv.ReloadConfig(cfg)
		a.modal = modalNone
	}
	return a, nil
}

func (a *App) viewWelcomeModal() string {
	var lines []string
	lines = append(lines, a.styles.Title.Render("Welcome to acpx — Detect ACP Tools"))
	lines = append(lines, a.styles.Muted.Render("Local OpenAI/Anthropic gateway for ACP subscriptions & API keys."))
	lines = append(lines, "")
	if len(a.detected) == 0 {
		lines = append(lines, a.styles.Muted.Render("No ACP descriptors available."))
	} else {
		for i, d := range a.detected {
			cursor := "  "
			if i == a.detectCursor {
				cursor = "> "
			}
			mark := "[ ]"
			if !d.Found {
				mark = "[-]"
			} else if a.detectSelected[i] {
				mark = "[x]"
			}
			status := a.styles.Muted.Render("not found")
			if d.Found {
				status = a.styles.StateReady.Render("found")
			} else if d.Hint != "" {
				status = a.styles.StateWarn.Render(truncate(d.Hint, 40))
			}
			lines = append(lines, fmt.Sprintf("%s%s %-20s %s", cursor, mark, d.Descriptor.Name, status))
		}
	}
	lines = append(lines, "")
	lines = append(lines, a.styles.Muted.Render("Space: toggle  Enter: add selected  a: add API key/account  s: skip"))
	return a.styles.Modal.Width(min(a.width-4, 74)).Render(strings.Join(lines, "\n"))
}

// --- Add Account Wizard ---

func (a *App) openAddAccount() {
	a.addStep = 0
	a.addKind = config.KindAPIKey
	a.addBackend = ""
	a.addListCur = 0
	a.addFocus = 0
	a.addTesting = false
	a.modal = modalAddAccount
}

func (a *App) initAddInputs() {
	a.addInputs = make([]textinput.Model, 4)
	for i := range a.addInputs {
		ti := textinput.New()
		ti.CharLimit = 256
		a.addInputs[i] = ti
	}
	a.addFocus = 0

	if a.addKind == config.KindAPIKey {
		preset, _ := a.srv.Registry().GetPreset(a.addBackend)
		a.addAuthStyle = preset.AuthStyle
		a.addInputs[0].Placeholder = "account name (e.g. personal)"
		a.addInputs[0].SetValue("default")
		a.addInputs[1].Placeholder = "API key (paste here)"
		a.addInputs[1].EchoMode = textinput.EchoPassword
		a.addInputs[2].Placeholder = "Base URL"
		a.addInputs[2].SetValue(preset.BaseURL)
		a.addInputs[3].Placeholder = "Models (comma-separated, optional)"
		a.addInputs[3].SetValue(strings.Join(preset.DefaultModels, ","))
	} else {
		desc, _ := a.srv.Registry().GetACP(a.addBackend)
		a.addInputs[0].Placeholder = "account name (e.g. work, personal)"
		a.addInputs[0].SetValue("default")
		a.addInputs[1].Placeholder = "Custom command (optional, space-separated)"
		if a.addBackend == "custom-acp" {
			a.addInputs[1].Placeholder = "ACP command (e.g. my-agent --acp)"
		}
		a.addInputs[2].Placeholder = "Default working directory (optional)"
		a.addInputs[3].Placeholder = "Models (comma-separated)"
		a.addInputs[3].SetValue(strings.Join(desc.DefaultModels, ","))
	}
	a.addInputs[0].Focus()
}

func (a *App) updateAddAccountModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "esc" {
		if a.addStep > 0 {
			a.addStep--
			return a, nil
		}
		a.modal = modalNone
		return a, nil
	}

	switch a.addStep {
	case 0: // Choose account kind
		switch msg.String() {
		case "up", "k", "down", "j", "tab":
			if a.addKind == config.KindAPIKey {
				a.addKind = config.KindACP
			} else {
				a.addKind = config.KindAPIKey
			}
		case "1":
			a.addKind = config.KindAPIKey
			a.addStep = 1
			a.addListCur = 0
		case "2":
			a.addKind = config.KindACP
			a.addStep = 1
			a.addListCur = 0
		case "enter":
			a.addStep = 1
			a.addListCur = 0
		case "q":
			a.modal = modalNone
		}
		return a, nil

	case 1: // Choose preset or ACP tool
		if a.addKind == config.KindAPIKey {
			presets := a.srv.Registry().ListPresets()
			switch msg.String() {
			case "up", "k":
				if a.addListCur > 0 {
					a.addListCur--
				}
			case "down", "j":
				if a.addListCur < len(presets)-1 {
					a.addListCur++
				}
			case "enter":
				if a.addListCur >= 0 && a.addListCur < len(presets) {
					a.addBackend = presets[a.addListCur].ID
					a.initAddInputs()
					a.addStep = 2
					return a, textinput.Blink
				}
			}
		} else {
			descs := a.srv.Registry().ListACP()
			switch msg.String() {
			case "up", "k":
				if a.addListCur > 0 {
					a.addListCur--
				}
			case "down", "j":
				if a.addListCur < len(descs)-1 {
					a.addListCur++
				}
			case "enter":
				if a.addListCur >= 0 && a.addListCur < len(descs) {
					a.addBackend = descs[a.addListCur].ID
					a.initAddInputs()
					a.addStep = 2
					return a, textinput.Blink
				}
			}
		}
		return a, nil

	case 2: // Enter account details
		switch msg.String() {
		case "tab", "down":
			a.addInputs[a.addFocus].Blur()
			a.addFocus = (a.addFocus + 1) % len(a.addInputs)
			a.addInputs[a.addFocus].Focus()
			return a, textinput.Blink
		case "shift+tab", "up":
			a.addInputs[a.addFocus].Blur()
			a.addFocus = (a.addFocus + len(a.addInputs) - 1) % len(a.addInputs)
			a.addInputs[a.addFocus].Focus()
			return a, textinput.Blink
		case "enter":
			if a.addFocus < len(a.addInputs)-1 {
				a.addInputs[a.addFocus].Blur()
				a.addFocus++
				a.addInputs[a.addFocus].Focus()
				return a, textinput.Blink
			}
			return a.submitAddAccount()
		case "ctrl+s":
			return a.submitAddAccount()
		default:
			var cmd tea.Cmd
			a.addInputs[a.addFocus], cmd = a.addInputs[a.addFocus].Update(msg)
			return a, cmd
		}
	}
	return a, nil
}

func (a *App) submitAddAccount() (tea.Model, tea.Cmd) {
	name := strings.TrimSpace(a.addInputs[0].Value())
	if err := config.ValidateName(name); err != nil {
		a.setStatus(err.Error(), true)
		return a, nil
	}

	cfg := a.srv.Config()
	id := config.SanitizeSlug(a.addBackend + "-" + name)
	if cfg.FindAccount(id) != nil {
		a.setStatus(fmt.Sprintf("Account %q already exists", id), true)
		return a, nil
	}

	var models []string
	for _, m := range strings.Split(a.addInputs[3].Value(), ",") {
		if m = strings.TrimSpace(m); m != "" {
			models = append(models, m)
		}
	}

	if a.addKind == config.KindAPIKey {
		apiKey := strings.TrimSpace(a.addInputs[1].Value())
		baseURL := strings.TrimSpace(a.addInputs[2].Value())
		if baseURL == "" {
			a.setStatus("Base URL is required", true)
			return a, nil
		}
		preset, _ := a.srv.Registry().GetPreset(a.addBackend)
		if !preset.KeyOptional && apiKey == "" {
			a.setStatus("API key is required for "+preset.Name, true)
			return a, nil
		}
		if apiKey != "" {
			if err := a.srv.Secrets().Set(id, apiKey); err != nil {
				a.setStatus("Failed to store API key: "+err.Error(), true)
				return a, nil
			}
		}
		acct := config.AccountConfig{
			ID:             id,
			Name:           name,
			Kind:           config.KindAPIKey,
			Backend:        a.addBackend,
			Enabled:        true,
			BaseURL:        baseURL,
			AuthStyle:      a.addAuthStyle,
			Models:         models,
			MaxConcurrency: 8,
		}
		cfg.Accounts = append(cfg.Accounts, acct)
		cfg.UI.OnboardingComplete = true
		_ = a.srv.ReloadConfig(cfg)
		a.modal = modalNone
		a.addTesting = true
		a.setStatus(fmt.Sprintf("Added %s/%s — testing connection...", acct.Backend, acct.Name), false)
		return a, a.testAccountCmd(acct)
	}

	// ACP account
	if _, err := config.EnsureProfileDir(a.srv.DataDir(), a.addBackend, name); err != nil {
		a.setStatus("Failed to create profile dir: "+err.Error(), true)
		return a, nil
	}
	var customCmd []string
	if raw := strings.TrimSpace(a.addInputs[1].Value()); raw != "" {
		customCmd = strings.Fields(raw)
	}
	if a.addBackend == "custom-acp" && len(customCmd) == 0 {
		a.setStatus("Custom ACP command is required", true)
		return a, nil
	}
	if len(models) == 0 {
		models = []string{"default"}
	}
	acct := config.AccountConfig{
		ID:             id,
		Name:           name,
		Kind:           config.KindACP,
		Backend:        a.addBackend,
		Enabled:        true,
		DefaultCwd:     strings.TrimSpace(a.addInputs[2].Value()),
		PermissionMode: config.PermDeny,
		CustomCommand:  customCmd,
		Models:         models,
		MaxConcurrency: 1,
	}
	cfg.Accounts = append(cfg.Accounts, acct)
	cfg.UI.OnboardingComplete = true
	_ = a.srv.ReloadConfig(cfg)
	a.modal = modalNone

	// Offer immediate login in isolated profile pane if descriptor defines a login command.
	if desc, ok := a.srv.Registry().GetACP(acct.Backend); ok && len(desc.Login) > 0 {
		a.setStatus(fmt.Sprintf("Added %s/%s — launching login...", acct.Backend, acct.Name), false)
		return a, a.loginAccountCmd(acct)
	}
	a.setStatus(fmt.Sprintf("Added ACP account %s/%s", acct.Backend, acct.Name), false)
	return a, nil
}

func (a *App) viewAddAccountModal() string {
	var lines []string
	lines = append(lines, a.styles.Title.Render("Add Account"))
	lines = append(lines, "")

	switch a.addStep {
	case 0:
		k1, k2 := "  ", "  "
		if a.addKind == config.KindAPIKey {
			k1 = "> "
		} else {
			k2 = "> "
		}
		lines = append(lines, k1+"1. API Key provider (DeepSeek, OpenRouter, OpenAI, Anthropic, Ollama...)")
		lines = append(lines, k2+"2. Subscription tool over ACP (agy, Gemini CLI, Codex, Claude, custom)")
		lines = append(lines, "")
		lines = append(lines, a.styles.Muted.Render("Enter: next   Esc: cancel"))

	case 1:
		if a.addKind == config.KindAPIKey {
			lines = append(lines, a.styles.Subtitle.Render("Select API-Key Provider:"))
			presets := a.srv.Registry().ListPresets()
			for i, p := range presets {
				cur := "  "
				if i == a.addListCur {
					cur = "> "
				}
				lines = append(lines, fmt.Sprintf("%s%-24s %s", cur, p.Name, a.styles.Muted.Render(p.BaseURL)))
			}
		} else {
			lines = append(lines, a.styles.Subtitle.Render("Select ACP Subscription Tool:"))
			descs := a.srv.Registry().ListACP()
			for i, d := range descs {
				cur := "  "
				if i == a.addListCur {
					cur = "> "
				}
				lines = append(lines, fmt.Sprintf("%s%-24s %s", cur, d.Name, a.styles.Muted.Render(strings.Join(d.Command, " "))))
			}
			if a.addListCur >= 0 && a.addListCur < len(descs) && descs[a.addListCur].TermsNote != "" {
				lines = append(lines, "")
				lines = append(lines, a.styles.Muted.Render(descs[a.addListCur].TermsNote))
			}
		}
		lines = append(lines, "")
		lines = append(lines, a.styles.Muted.Render("Enter: next   Esc: back"))

	case 2:
		labels := []string{"Name:", "API Key:", "Base URL:", "Models:"}
		if a.addKind == config.KindACP {
			labels = []string{"Name:", "Custom Cmd:", "Default Cwd:", "Models:"}
		}
		for i, lbl := range labels {
			prefix := "  "
			if i == a.addFocus {
				prefix = "> "
			}
			lines = append(lines, fmt.Sprintf("%s%-12s %s", prefix, lbl, a.addInputs[i].View()))
		}
		lines = append(lines, "")
		lines = append(lines, a.styles.Muted.Render("Tab: next field   Enter/Ctrl+S: save & test/login   Esc: back"))
	}

	return a.styles.Modal.Width(min(a.width-4, 74)).Render(strings.Join(lines, "\n"))
}
