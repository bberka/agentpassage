package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
)

func (a *App) openWelcome() {
	a.detected = a.srv.Registry().DetectInstalled()
	a.detectCursor = 0
	a.detectSearching = false
	a.detectSearch.SetValue("")
	a.detectSearch.Blur()
	a.detectSelected = make(map[string]bool)
	for _, d := range a.detected {
		if d.Found {
			a.detectSelected[d.Descriptor.ID] = true
		}
	}
	a.modal = modalWelcome
}

func (a *App) filteredDetected() []descriptors.DetectedCLI {
	q := strings.ToLower(strings.TrimSpace(a.detectSearch.Value()))
	if q == "" {
		return a.detected
	}
	var out []descriptors.DetectedCLI
	for _, d := range a.detected {
		hay := strings.ToLower(d.Descriptor.ID + " " + d.Descriptor.Name + " " + d.Descriptor.Description + " " + d.Descriptor.Detect)
		if strings.Contains(hay, q) {
			out = append(out, d)
		}
	}
	return out
}

func (a *App) updateWelcomeModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.detectSearching {
		switch msg.String() {
		case "esc", "enter":
			a.detectSearching = false
			a.detectSearch.Blur()
			a.detectCursor = 0
			return a, nil
		default:
			var cmd tea.Cmd
			a.detectSearch, cmd = a.detectSearch.Update(msg)
			a.detectCursor = 0
			return a, cmd
		}
	}

	items := a.filteredDetected()
	if a.detectCursor >= len(items) && len(items) > 0 {
		a.detectCursor = len(items) - 1
	}

	switch msg.String() {
	case "/":
		a.detectSearching = true
		a.detectSearch.Focus()
		return a, textinput.Blink
	case "up", "k":
		if a.detectCursor > 0 {
			a.detectCursor--
		}
	case "down", "j":
		if a.detectCursor < len(items)-1 {
			a.detectCursor++
		}
	case "pgup":
		a.detectCursor = max(a.detectCursor-8, 0)
	case "pgdown":
		if len(items) > 0 {
			a.detectCursor = min(a.detectCursor+8, len(items)-1)
		}
	case " ":
		if a.detectCursor >= 0 && a.detectCursor < len(items) {
			d := items[a.detectCursor]
			if d.Found || d.Descriptor.NPXPackage != "" || d.Descriptor.UVXPackage != "" {
				a.detectSelected[d.Descriptor.ID] = !a.detectSelected[d.Descriptor.ID]
			}
		}
	case "i":
		if a.detectCursor >= 0 && a.detectCursor < len(items) {
			d := items[a.detectCursor]
			if d.Found {
				a.setStatus(d.Descriptor.Name+" is already installed at "+d.BinaryPath, false)
				return a, nil
			}
			if !d.Descriptor.CanInstall() {
				note := d.Descriptor.InstallNote
				if note == "" {
					note = "No automatic installer for " + d.Descriptor.Name
				}
				a.setStatus(note, true)
				return a, nil
			}
			return a, a.startInstall([]descriptors.ACPDescriptor{d.Descriptor}, modalWelcome)
		}
	case "R":
		a.setStatus("Syncing latest official ACP Registry...", false)
		return a, a.syncRegistryCmd()
	case "enter":
		cfg := a.srv.Config()
		added := 0
		for _, d := range a.detected {
			if !a.detectSelected[d.Descriptor.ID] {
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
				ExposeMode:     config.ExposePool,
				Models:         append([]string(nil), d.Descriptor.DefaultModels...),
				MaxConcurrency: 1,
			})
			added++
		}
		cfg.UI.OnboardingComplete = true
		_ = a.srv.ReloadConfig(cfg)
		a.modal = modalNone
		if added > 0 {
			a.screen = ScreenAccounts
			a.setStatus(fmt.Sprintf("Added %d ACP account(s). Press 'l' to log in or 't' to test.", added), false)
		} else {
			a.setStatus("Setup complete. Press 'a' to add an API-key or ACP account.", false)
		}
	case "a":
		cfg := a.srv.Config()
		cfg.UI.OnboardingComplete = true
		_ = a.srv.ReloadConfig(cfg)
		a.openAddAccount()
	case "C":
		cfg := a.srv.Config()
		cfg.UI.OnboardingComplete = true
		_ = a.srv.ReloadConfig(cfg)
		a.openConnectModal()
	case "esc", "q", "s":
		cfg := a.srv.Config()
		cfg.UI.OnboardingComplete = true
		_ = a.srv.ReloadConfig(cfg)
		a.modal = modalNone
	}
	return a, nil
}

func (a *App) viewWelcomeModal() string {
	cardW := clamp(a.width-8, 64, 94)
	innerW := cardW - 6
	items := a.filteredDetected()

	var lines []string
	lines = append(lines, a.styles.Title.Render(fmt.Sprintf("◆ ACP Registry & CLI Installer (%d agents)", len(items))))
	lines = append(lines, a.styles.Muted.Render("Install adapters right inside apx without exiting, or press Space + Enter to add agents."))
	if a.detectSearching {
		lines = append(lines, a.styles.Highlight.Render("Search: ")+a.detectSearch.View()+" "+a.styles.Muted.Render("(enter/esc)"))
	} else if q := strings.TrimSpace(a.detectSearch.Value()); q != "" {
		lines = append(lines, a.styles.Muted.Render(fmt.Sprintf("Search: %q  (/ edit)", q)))
	}
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))

	maxRows := clamp(a.height-16, 6, 15)
	start := 0
	if a.detectCursor >= maxRows {
		start = a.detectCursor - maxRows + 1
	}
	end := min(start+maxRows, len(items))

	for i := start; i < end; i++ {
		d := items[i]
		mark := "[ ]"
		if a.detectSelected[d.Descriptor.ID] {
			mark = "[x]"
		} else if !d.Found && d.Descriptor.NPXPackage == "" && d.Descriptor.UVXPackage == "" {
			mark = " · "
		}

		var status string
		if d.Found {
			status = a.styles.StateReady.Render("● installed")
		} else if d.Descriptor.BinaryArchive != "" {
			status = a.styles.StateWarn.Render("○ binary (i: install)")
		} else if d.Descriptor.InstallPackage != "" {
			status = a.styles.StateWarn.Render("○ npm (i: install)")
		} else if d.Descriptor.UVXPackage != "" {
			status = a.styles.StateWarn.Render("○ uvx (i: install)")
		} else {
			status = a.styles.Muted.Render("○ manual")
		}

		desc := d.Descriptor.Description
		if d.Found && desc == "" {
			desc = d.BinaryPath
		}
		row := fmt.Sprintf("%s %-20s %-22s %s",
			mark,
			truncate(d.Descriptor.Name, 20),
			status,
			a.styles.Muted.Render(truncate(desc, max(innerW-48, 12))),
		)
		if i == a.detectCursor {
			lines = append(lines, a.styles.Selected.Width(innerW).Render("> "+row))
		} else {
			lines = append(lines, "  "+row)
		}
	}
	for i := end - start; i < maxRows; i++ {
		lines = append(lines, "")
	}

	if a.detectCursor >= 0 && a.detectCursor < len(items) {
		cur := items[a.detectCursor]
		lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
		info := cur.Descriptor.Description
		if cur.Hint != "" && !cur.Found {
			info = cur.Hint + " — " + info
		} else if cur.Found {
			info = "Binary: " + cur.BinaryPath
		}
		lines = append(lines, a.styles.Muted.Render(truncate(info, innerW)))
	}

	lines = append(lines, strings.Join([]string{
		a.styles.KeyHint("/", "search"),
		a.styles.KeyHint("i", "install"),
		a.styles.KeyHint("space", "select"),
		a.styles.KeyHint("enter", "add selected"),
		a.styles.KeyHint("R", "sync registry"),
		a.styles.KeyHint("a", "API key"),
		a.styles.KeyHint("C", "connect CLIs"),
		a.styles.KeyHint("esc", "close"),
	}, "  "))
	return a.styles.Modal.Width(cardW).Render(strings.Join(lines, "\n"))
}

// --- Pre-login Prompt Modal ---

func (a *App) openLoginPrompt(acct config.AccountConfig) {
	cp := acct
	a.loginTarget = &cp
	a.modal = modalLoginPrompt
}

func (a *App) updateLoginPromptModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.loginTarget == nil {
		a.modal = modalNone
		return a, nil
	}
	acct := *a.loginTarget
	switch msg.String() {
	case "enter", "l", "y":
		if acct.Kind == config.KindCLI {
			if desc, ok := a.srv.Registry().GetCLI(acct.Backend); ok {
				if _, found := desc.FoundOnPath(); !found && desc.Binary != "" {
					a.setStatus(desc.Name+" binary ("+desc.Binary+") not found on PATH.", true)
					return a, nil
				}
			}
		} else if desc, ok := a.srv.Registry().GetACP(acct.Backend); ok {
			if _, found := a.srv.Registry().FindLocalBinary(desc); !found && desc.CanInstall() {
				a.setStatus(desc.Name+" binary not installed yet — press 'i' to install it first.", true)
				return a, nil
			}
		}
		a.loginTarget = nil
		a.modal = modalNone
		a.setStatus("Launching "+acct.Backend+" login (returns to apx when finished)...", false)
		return a, a.loginAccountCmd(acct)
	case "i":
		if acct.Kind != config.KindCLI {
			if desc, ok := a.srv.Registry().GetACP(acct.Backend); ok && desc.CanInstall() {
				return a, a.startInstall([]descriptors.ACPDescriptor{desc}, modalLoginPrompt)
			}
		}
	case "s", "n", "esc", "q":
		a.loginTarget = nil
		a.modal = modalNone
		a.setStatus("Skipped login for now — press 'l' on Accounts anytime.", false)
	}
	return a, nil
}

func (a *App) viewLoginPromptModal() string {
	if a.loginTarget == nil {
		return ""
	}
	acct := *a.loginTarget
	cardW := clamp(a.width-12, 56, 80)
	innerW := cardW - 6

	pdir, _ := config.ProfileDir(a.srv.DataDir(), acct.Backend, acct.Name)
	profileDisp := pdir
	if acct.UseHostProfile {
		profileDisp = "host CLI login (shared with your terminal)"
	}

	var loginCmd string
	var binMissing bool
	var binName string
	var canInstall bool
	var toolName string

	if acct.Kind == config.KindCLI {
		desc, _ := a.srv.Registry().GetCLI(acct.Backend)
		toolName = desc.Name
		binName = desc.Binary
		src := desc.LoginCmd
		if len(acct.CustomLogin) > 0 {
			src = acct.CustomLogin
		}
		expanded := make([]string, len(src))
		for i, s := range src {
			s = strings.ReplaceAll(s, "{profile}", pdir)
			s = strings.ReplaceAll(s, "{account}", acct.Name)
			expanded[i] = s
		}
		loginCmd = strings.Join(expanded, " ")
		_, found := desc.FoundOnPath()
		binMissing = !found && desc.Binary != ""
	} else {
		desc, _ := a.srv.Registry().GetACP(acct.Backend)
		toolName = desc.Name
		binName = desc.Detect
		canInstall = desc.CanInstall()
		loginCmd = strings.Join(desc.ExpandLogin(pdir, acct.CustomLogin), " ")
		_, found := a.srv.Registry().FindLocalBinary(desc)
		binMissing = !found && desc.Detect != ""
	}

	var lines []string
	lines = append(lines, a.styles.Title.Render("Sign In to "+acct.Backend+"/"+acct.Name))
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
	lines = append(lines, "")
	lines = append(lines, "Profile: "+a.styles.Highlight.Render(truncate(profileDisp, innerW-10)))
	if loginCmd != "" {
		lines = append(lines, "Command: "+a.styles.Muted.Render(truncate(loginCmd, innerW-10)))
	}
	lines = append(lines, "")
	if binMissing {
		lines = append(lines, a.styles.StateWarn.Render(fmt.Sprintf("Binary %q is not installed yet.", binName)))
		if canInstall {
			lines = append(lines, "Press 'i' to install "+toolName+" inside apx first.")
		}
	} else if loginCmd == "" {
		lines = append(lines, a.styles.StateWarn.Render("No login command configured for "+acct.Backend+"."))
		lines = append(lines, a.styles.Muted.Render("Set custom_login in config.toml or switch to Host CLI login (press 'H')."))
	} else {
		lines = append(lines, "The official CLI login will open in this terminal and automatically")
		lines = append(lines, "return you right back to apx once authentication finishes.")
	}
	lines = append(lines, "")
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))

	var hints []string
	if binMissing && canInstall {
		hints = append(hints, a.styles.KeyHint("i", "install "+binName))
	} else if loginCmd != "" {
		hints = append(hints, a.styles.KeyHint("enter", "launch login"))
	}
	hints = append(hints, a.styles.KeyHint("s", "skip for now"))
	lines = append(lines, a.styles.JoinHints(hints...))
	return a.styles.Modal.Width(cardW).Render(strings.Join(lines, "\n"))
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
	a.addInputs = make([]textinput.Model, 3)
	for i := range a.addInputs {
		ti := textinput.New()
		ti.Prompt = ""
		ti.CharLimit = 256
		a.addInputs[i] = ti
	}
	a.addFocus = 0
	a.modelPicker = nil

	if a.addKind == config.KindAPIKey {
		preset, _ := a.srv.Registry().GetPreset(a.addBackend)
		a.addAuthStyle = preset.AuthStyle
		a.addInputs[0].Placeholder = "account name (e.g. a, b, personal)"
		a.addInputs[0].SetValue("default")
		a.addInputs[1].Placeholder = "API key (paste here)"
		a.addInputs[1].EchoMode = textinput.EchoPassword
		a.addInputs[2].Placeholder = "Base URL"
		a.addInputs[2].SetValue(preset.BaseURL)
	} else if a.addKind == config.KindCLI {
		a.addInputs[0].Placeholder = "account name (e.g. host, work, personal)"
		a.addInputs[0].SetValue("default")
		a.addInputs[1].Placeholder = "Custom command (optional; e.g. claude -p {prompt})"
		if a.addBackend == "custom-cli" {
			a.addInputs[1].Placeholder = "CLI command (supports {prompt} {model} {session})"
		}
		a.addInputs[2].Placeholder = "Model prefix (optional, e.g. a- -> a-sonnet)"
	} else {
		a.addInputs[0].Placeholder = "account name (e.g. a, b, work, personal)"
		a.addInputs[0].SetValue("default")
		a.addInputs[1].Placeholder = "Custom command (optional)"
		if a.addBackend == "custom-acp" {
			a.addInputs[1].Placeholder = "ACP command (e.g. my-agent --acp)"
		}
		a.addInputs[2].Placeholder = "Model prefix (optional, e.g. a- -> a-gemini-4-neon)"
	}
	a.addInputs[0].Focus()
}

func (a *App) buildDraftAccount() (config.AccountConfig, string, error) {
	name := strings.TrimSpace(a.addInputs[0].Value())
	if err := config.ValidateName(name); err != nil {
		return config.AccountConfig{}, "", err
	}
	cfg := a.srv.Config()
	id := config.SanitizeSlug(a.addBackend + "-" + name)
	if cfg.FindAccount(id) != nil {
		return config.AccountConfig{}, "", fmt.Errorf("account %q already exists", id)
	}

	if a.addKind == config.KindAPIKey {
		apiKey := strings.TrimSpace(a.addInputs[1].Value())
		baseURL := strings.TrimSpace(a.addInputs[2].Value())
		if baseURL == "" {
			return config.AccountConfig{}, "", fmt.Errorf("base URL is required")
		}
		preset, _ := a.srv.Registry().GetPreset(a.addBackend)
		if !preset.KeyOptional && apiKey == "" {
			return config.AccountConfig{}, "", fmt.Errorf("API key is required for %s", preset.Name)
		}
		return config.AccountConfig{
			ID:             id,
			Name:           name,
			Kind:           config.KindAPIKey,
			Backend:        a.addBackend,
			Enabled:        true,
			BaseURL:        baseURL,
			AuthStyle:      a.addAuthStyle,
			ExposeMode:     config.ExposePool,
			Models:         append([]string(nil), preset.DefaultModels...),
			MaxConcurrency: 8,
		}, apiKey, nil
	}

	var customCmd []string
	if raw := strings.TrimSpace(a.addInputs[1].Value()); raw != "" {
		customCmd = strings.Fields(raw)
	}
	prefix := strings.TrimSpace(a.addInputs[2].Value())
	expose := config.ExposePool
	if prefix != "" {
		expose = config.ExposePrefix
	}

	if a.addKind == config.KindCLI {
		if a.addBackend == "custom-cli" && len(customCmd) == 0 {
			return config.AccountConfig{}, "", fmt.Errorf("custom CLI command is required")
		}
		desc, _ := a.srv.Registry().GetCLI(a.addBackend)
		return config.AccountConfig{
			ID:             id,
			Name:           name,
			Kind:           config.KindCLI,
			Backend:        a.addBackend,
			Enabled:        true,
			PermissionMode: config.PermDeny,
			UseHostProfile: true, // Direct CLI proxy reuses existing host CLI sign-in by default; press 'H' for isolated profile!
			CustomCommand:  customCmd,
			ExposeMode:     expose,
			ModelPrefix:    prefix,
			Models:         append([]string(nil), desc.DefaultModels...),
			MaxConcurrency: 1,
		}, "", nil
	}

	if a.addBackend == "custom-acp" && len(customCmd) == 0 {
		return config.AccountConfig{}, "", fmt.Errorf("custom ACP command is required")
	}
	desc, _ := a.srv.Registry().GetACP(a.addBackend)
	return config.AccountConfig{
		ID:             id,
		Name:           name,
		Kind:           config.KindACP,
		Backend:        a.addBackend,
		Enabled:        true,
		PermissionMode: config.PermDeny,
		CustomCommand:  customCmd,
		ExposeMode:     expose,
		ModelPrefix:    prefix,
		Models:         append([]string(nil), desc.DefaultModels...),
		MaxConcurrency: 1,
	}, "", nil
}

func (a *App) enterAddStep4Models() (tea.Model, tea.Cmd) {
	acct, apiKey, err := a.buildDraftAccount()
	if err != nil {
		a.setStatus(err.Error(), true)
		return a, nil
	}
	a.modelPicker = newModelPicker("", "Select Models — "+acct.Backend+"/"+acct.Name, acct.Models, acct.Models)
	a.addStep = 3
	// For API-key accounts (or Direct CLI accounts with a models command), immediately load models!
	if acct.Kind == config.KindAPIKey {
		a.modelPicker.loading = true
		a.setStatus("Loading all models from "+acct.BaseURL+"...", false)
		return a, a.fetchModelsCmd("", acct, apiKey)
	}
	if acct.Kind == config.KindCLI {
		if desc, ok := a.srv.Registry().GetCLI(acct.Backend); ok && len(desc.ModelsCmd) > 0 {
			a.modelPicker.loading = true
			a.setStatus("Loading live models from "+desc.Binary+"...", false)
			return a, a.fetchModelsCmd("", acct, "")
		}
	}
	return a, nil
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
	case 0:
		kinds := []string{config.KindAPIKey, config.KindACP, config.KindCLI}
		idx := 0
		for i, k := range kinds {
			if k == a.addKind {
				idx = i
				break
			}
		}
		switch msg.String() {
		case "up", "k":
			idx = (idx + len(kinds) - 1) % len(kinds)
			a.addKind = kinds[idx]
		case "down", "j", "tab":
			idx = (idx + 1) % len(kinds)
			a.addKind = kinds[idx]
		case "1":
			a.addKind = config.KindAPIKey
			a.addStep = 1
			a.addListCur = 0
		case "2":
			a.addKind = config.KindACP
			a.addStep = 1
			a.addListCur = 0
		case "3":
			a.addKind = config.KindCLI
			a.addStep = 1
			a.addListCur = 0
		case "enter":
			a.addStep = 1
			a.addListCur = 0
		case "q":
			a.modal = modalNone
		}
		return a, nil

	case 1:
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
		} else if a.addKind == config.KindCLI {
			clis := a.srv.Registry().ListCLI()
			switch msg.String() {
			case "up", "k":
				if a.addListCur > 0 {
					a.addListCur--
				}
			case "down", "j":
				if a.addListCur < len(clis)-1 {
					a.addListCur++
				}
			case "enter":
				if a.addListCur >= 0 && a.addListCur < len(clis) {
					a.addBackend = clis[a.addListCur].ID
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
			case "pgup":
				a.addListCur = max(a.addListCur-8, 0)
			case "pgdown":
				a.addListCur = min(a.addListCur+8, len(descs)-1)
			case "i":
				if a.addListCur >= 0 && a.addListCur < len(descs) {
					d := descs[a.addListCur]
					if d.CanInstall() {
						return a, a.startInstall([]descriptors.ACPDescriptor{d}, modalAddAccount)
					}
					a.setStatus("No automatic installer for "+d.Name, true)
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

	case 2:
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
			return a.enterAddStep4Models()
		case "ctrl+s":
			return a.enterAddStep4Models()
		default:
			var cmd tea.Cmd
			a.addInputs[a.addFocus], cmd = a.addInputs[a.addFocus].Update(msg)
			return a, cmd
		}

	case 3: // Step 4: Interactive Model Picker (load all, search, enable/disable, add custom)
		p := a.modelPicker
		if p == nil {
			a.addStep = 2
			return a, nil
		}
		if p.addingModel {
			switch msg.String() {
			case "esc":
				p.addingModel = false
				p.addInput.Blur()
				return a, nil
			case "enter":
				if val := strings.TrimSpace(p.addInput.Value()); val != "" {
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
			for _, id := range items {
				p.enabled[id] = true
			}
		case "n":
			for _, id := range items {
				p.enabled[id] = false
			}
		case "f":
			acct, apiKey, err := a.buildDraftAccount()
			if err != nil {
				a.setStatus(err.Error(), true)
				return a, nil
			}
			p.loading = true
			p.fetchErr = ""
			return a, a.fetchModelsCmd("", acct, apiKey)
		case "enter", "ctrl+s":
			return a.submitAddAccount()
		}
	}
	return a, nil
}

func (a *App) submitAddAccount() (tea.Model, tea.Cmd) {
	acct, apiKey, err := a.buildDraftAccount()
	if err != nil {
		a.setStatus(err.Error(), true)
		return a, nil
	}

	if a.modelPicker != nil {
		catalog, enabled, disabled := a.modelPicker.toLists()
		acct.AvailableModels = catalog
		acct.Models = enabled
		acct.DisabledModels = disabled
	}

	cfg := a.srv.Config()
	if acct.Kind == config.KindAPIKey {
		if apiKey != "" {
			if err := a.srv.Secrets().Set(acct.ID, apiKey); err != nil {
				a.setStatus("Failed to store API key: "+err.Error(), true)
				return a, nil
			}
		}
		cfg.Accounts = append(cfg.Accounts, acct)
		cfg.UI.OnboardingComplete = true
		_ = a.srv.ReloadConfig(cfg)
		a.modelPicker = nil
		a.modal = modalNone
		a.setStatus(fmt.Sprintf("Added %s/%s (%d enabled / %d total models)", acct.Backend, acct.Name, len(acct.Models), len(acct.AvailableModels)), false)
		return a, nil
	}

	if !acct.UseHostProfile {
		if _, err := config.EnsureProfileDir(a.srv.DataDir(), acct.Backend, acct.Name); err != nil {
			a.setStatus("Failed to create profile dir: "+err.Error(), true)
			return a, nil
		}
	}
	if len(acct.Models) == 0 && len(acct.AvailableModels) == 0 {
		acct.Models = []string{"default"}
		acct.AvailableModels = []string{"default"}
	}
	cfg.Accounts = append(cfg.Accounts, acct)
	cfg.UI.OnboardingComplete = true
	_ = a.srv.ReloadConfig(cfg)
	a.modelPicker = nil

	if acct.Kind == config.KindCLI {
		a.modal = modalNone
		a.setStatus(fmt.Sprintf("Added Direct CLI proxy %s/%s (%d models, host login=%v)", acct.Backend, acct.Name, len(acct.Models), acct.UseHostProfile), false)
		return a, nil
	}

	a.setStatus(fmt.Sprintf("Added ACP account %s/%s (%d models)", acct.Backend, acct.Name, len(acct.Models)), false)
	if !acct.UseHostProfile {
		if desc, ok := a.srv.Registry().GetACP(acct.Backend); ok && len(desc.Login) > 0 {
			a.openLoginPrompt(acct)
			return a, nil
		}
	}
	a.modal = modalNone
	return a, nil
}

func (a *App) viewAddAccountModal() string {
	cardW := clamp(a.width-10, 60, 86)
	innerW := cardW - 6

	stepLabel := func(idx int, text string) string {
		if idx == a.addStep {
			return a.styles.Highlight.Render("● " + text)
		}
		if idx < a.addStep {
			return a.styles.StateReady.Render("✓ " + text)
		}
		return a.styles.Muted.Render("○ " + text)
	}
	stepper := stepLabel(0, "1 Type") + " " + a.styles.Muted.Render("─") + " " +
		stepLabel(1, "2 Provider") + " " + a.styles.Muted.Render("─") + " " +
		stepLabel(2, "3 Configure") + " " + a.styles.Muted.Render("─") + " " +
		stepLabel(3, "4 Models")

	var lines []string
	lines = append(lines, a.styles.Title.Render("Add Account")+"    "+stepper)
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
	lines = append(lines, "")

	switch a.addStep {
	case 0:
		opts := []struct {
			kind  string
			label string
			desc  string
		}{
			{config.KindAPIKey, "1. API Key Provider", "DeepSeek, OpenRouter, OpenAI, Anthropic, Ollama, Custom"},
			{config.KindACP, "2. ACP Agent (45+)", "ACP JSON-RPC: agy, Codex, Claude, Cursor, OpenCode, Pi"},
			{config.KindCLI, "3. Direct CLI Proxy", "No adapter: claude -p, codex exec, agy, opencode run"},
		}
		descW := max(innerW-25, 16)
		for _, o := range opts {
			row := fmt.Sprintf("%-21s %s", o.label, truncate(o.desc, descW))
			if a.addKind == o.kind {
				lines = append(lines, a.styles.Selected.Width(innerW).Render("> "+truncate(row, innerW-2)))
			} else {
				lines = append(lines, "  "+truncate(row, innerW-2))
			}
		}
		lines = append(lines, "")
		lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
		lines = append(lines, a.styles.KeyHint("enter", "next")+"   "+a.styles.KeyHint("esc", "cancel"))

	case 1:
		maxRows := clamp(a.height-16, 6, 13)
		if a.addKind == config.KindAPIKey {
			presets := a.srv.Registry().ListPresets()
			start := 0
			if a.addListCur >= maxRows {
				start = a.addListCur - maxRows + 1
			}
			for i := start; i < min(start+maxRows, len(presets)); i++ {
				p := presets[i]
				row := fmt.Sprintf("%-24s %s", p.Name, a.styles.Muted.Render(truncate(p.BaseURL, max(innerW-28, 12))))
				if i == a.addListCur {
					lines = append(lines, a.styles.Selected.Width(innerW).Render("> "+row))
				} else {
					lines = append(lines, "  "+row)
				}
			}
			lines = append(lines, "")
			lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
			lines = append(lines, a.styles.KeyHint("enter", "next")+"   "+a.styles.KeyHint("esc", "back"))
		} else if a.addKind == config.KindCLI {
			clis := a.srv.Registry().ListCLI()
			for i, c := range clis {
				badge := a.styles.Muted.Render("custom")
				if c.Binary != "" {
					if _, found := c.FoundOnPath(); found {
						badge = a.styles.StateReady.Render("● found")
					} else {
						badge = a.styles.StateWarn.Render("○ missing")
					}
				}
				row := fmt.Sprintf("%-25s %-11s %s",
					truncate(c.Name, 25),
					badge,
					a.styles.Muted.Render(truncate(c.Description, max(innerW-40, 10))),
				)
				if i == a.addListCur {
					lines = append(lines, a.styles.Selected.Width(innerW).Render("> "+row))
				} else {
					lines = append(lines, "  "+row)
				}
			}
			lines = append(lines, "")
			lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
			lines = append(lines, a.styles.KeyHint("enter", "next")+"   "+a.styles.KeyHint("esc", "back"))
		} else {
			descs := a.srv.Registry().ListACP()
			start := 0
			if a.addListCur >= maxRows {
				start = a.addListCur - maxRows + 1
			}
			for i := start; i < min(start+maxRows, len(descs)); i++ {
				d := descs[i]
				badge := a.styles.Muted.Render("custom")
				if d.ID != "custom-acp" {
					if _, found := a.srv.Registry().FindLocalBinary(d); found {
						badge = a.styles.StateReady.Render("● installed")
					} else if d.CanInstall() {
						badge = a.styles.StateWarn.Render("○ i: install")
					} else {
						badge = a.styles.Muted.Render("○ manual")
					}
				}
				row := fmt.Sprintf("%-22s %-15s %s",
					truncate(d.Name, 22),
					badge,
					a.styles.Muted.Render(truncate(d.Description, max(innerW-41, 10))),
				)
				if i == a.addListCur {
					lines = append(lines, a.styles.Selected.Width(innerW).Render("> "+row))
				} else {
					lines = append(lines, "  "+row)
				}
			}
			lines = append(lines, "")
			lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
			lines = append(lines, a.styles.KeyHint("enter", "next")+"   "+a.styles.KeyHint("i", "install CLI")+"   "+a.styles.KeyHint("pgup/dn", "scroll")+"   "+a.styles.KeyHint("esc", "back"))
		}

	case 2:
		labels := []string{"Name", "API Key", "Base URL"}
		if a.addKind == config.KindACP || a.addKind == config.KindCLI {
			labels = []string{"Name", "Custom Cmd", "Model Prefix"}
		}
		for i, lbl := range labels {
			prefix := "  "
			lStyle := a.styles.Muted
			if i == a.addFocus {
				prefix = a.styles.Highlight.Render("› ")
				lStyle = a.styles.Highlight
			}
			lines = append(lines, fmt.Sprintf("%s%-14s %s", prefix, lStyle.Render(lbl+":"), a.addInputs[i].View()))
		}
		lines = append(lines, "")
		if a.addKind == config.KindAPIKey {
			lines = append(lines, a.styles.Muted.Render("Next step automatically fetches ALL available models so you can enable/disable them."))
		} else {
			lines = append(lines, a.styles.Muted.Render("Leave Model Prefix empty to pool/rotate across accounts, or set e.g. 'a-' to register 'a-<model>'."))
		}
		lines = append(lines, "")
		lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
		lines = append(lines, a.styles.KeyHint("tab", "next field")+"   "+a.styles.KeyHint("enter/ctrl+s", "load models →")+"   "+a.styles.KeyHint("esc", "back"))

	case 3:
		p := a.modelPicker
		if p != nil {
			summary := fmt.Sprintf("%d enabled / %d loaded", p.enabledCount(), len(p.models))
			lines = append(lines, a.styles.Subtitle.Render("Select Models to Enable")+"   "+a.styles.Muted.Render(summary))
			maxRows := clamp(a.height-18, 6, 13)
			lines = append(lines, a.renderModelPickerBody(p, innerW, maxRows)...)
		}
		lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
		lines = append(lines, strings.Join([]string{
			a.styles.KeyHint("space", "toggle"),
			a.styles.KeyHint("a", "all"),
			a.styles.KeyHint("n", "none"),
			a.styles.KeyHint("f", "refetch"),
			a.styles.KeyHint("/", "search"),
			a.styles.KeyHint("+", "add"),
			a.styles.KeyHint("enter", "save account"),
			a.styles.KeyHint("esc", "back"),
		}, "  "))
	}

	return a.styles.Modal.Width(cardW).Render(strings.Join(lines, "\n"))
}
