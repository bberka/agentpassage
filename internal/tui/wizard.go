package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/hostauth"
)

func (a *App) openWelcome() {
	a.detected = a.srv.Registry().DetectInstalled()
	a.detectCursor = 0
	a.detectSearching = false
	a.detectInstalledOnly = false
	a.detectSearch.SetValue("")
	a.detectSearch.Blur()
	a.detectSelected = make(map[string]bool)
	existingBackends := a.configuredBackendCounts()
	for _, d := range a.detected {
		if d.Found && existingBackends[d.Descriptor.ID] == 0 {
			a.detectSelected[d.Descriptor.ID] = true
		}
	}
	a.hostAuthItems = hostauth.DetectAll("")
	a.modal = modalWelcome
}

func (a *App) configuredBackendCounts() map[string]int {
	counts := make(map[string]int)
	for _, acct := range a.srv.Config().Accounts {
		if acct.Kind == config.KindACP || acct.Kind == config.KindCLI {
			counts[acct.Backend]++
		}
	}
	return counts
}

// openStartupHostAuthIfNeeded checks for existing host CLI logins on launch that either
// aren't added to apx yet or have an isolated account without local credentials yet.
func (a *App) openStartupHostAuthIfNeeded(homeDir string) bool {
	found := hostauth.DetectAll(homeDir)
	if len(found) == 0 {
		return false
	}
	cfg := a.srv.Config()
	var actionable []hostauth.DetectedLogin
	targets := make(map[string]string)
	for _, d := range found {
		var acctMatch *config.AccountConfig
		hasAuthedAccount := false
		for i := range cfg.Accounts {
			ac := &cfg.Accounts[i]
			if ac.Backend != d.Backend || (ac.Kind != config.KindACP && ac.Kind != config.KindCLI) {
				continue
			}
			if ac.UseHostProfile {
				hasAuthedAccount = true
				break
			}
			if pdir, err := config.ProfileDir(a.srv.DataDir(), ac.Backend, ac.Name); err == nil && hostauth.ProfileHasAuth(ac.Backend, pdir) {
				hasAuthedAccount = true
				break
			}
			if acctMatch == nil {
				acctMatch = ac
			}
		}
		if hasAuthedAccount {
			continue
		}
		actionable = append(actionable, d)
		if acctMatch != nil {
			targets[d.Backend] = acctMatch.ID
		}
	}
	if len(actionable) == 0 {
		return false
	}
	a.openHostAuthModal(actionable, targets, true)
	return true
}

func (a *App) openHostAuthModal(items []hostauth.DetectedLogin, targets map[string]string, fromInit bool) {
	a.hostAuthItems = items
	a.hostAuthSelected = make(map[string]bool, len(items))
	for _, d := range items {
		a.hostAuthSelected[d.Backend] = true
	}
	a.hostAuthCursor = 0
	a.hostAuthTargets = targets
	a.hostAuthFromInit = fromInit
	a.modal = modalHostAuth
}

func (a *App) updateHostAuthModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	items := a.hostAuthItems
	if len(items) == 0 {
		a.modal = modalNone
		return a, nil
	}
	if a.hostAuthCursor >= len(items) {
		a.hostAuthCursor = len(items) - 1
	}

	dismiss := func() {
		cfg := a.srv.Config()
		cfg.UI.HostAuthPromptDismissed = true
		cfg.UI.OnboardingComplete = true
		_ = a.srv.ReloadConfig(cfg)
		a.modal = modalNone
	}

	ensureAccountFor := func(cfg *config.Config, d hostauth.DetectedLogin) (*config.AccountConfig, bool) {
		if id := a.hostAuthTargets[d.Backend]; id != "" {
			if existing := cfg.FindAccount(id); existing != nil {
				return existing, false
			}
		}
		for i := range cfg.Accounts {
			if cfg.Accounts[i].Backend == d.Backend && (cfg.Accounts[i].Kind == config.KindACP || cfg.Accounts[i].Kind == config.KindCLI) {
				return &cfg.Accounts[i], false
			}
		}
		id := config.SanitizeSlug(d.Backend + "-default")
		if existing := cfg.FindAccount(id); existing != nil {
			return existing, false
		}
		models := []string{"default"}
		if desc, ok := a.srv.Registry().GetACP(d.Backend); ok && len(desc.DefaultModels) > 0 {
			models = append([]string(nil), desc.DefaultModels...)
		}
		_, _ = config.EnsureProfileDir(a.srv.DataDir(), d.Backend, "default")
		cfg.Accounts = append(cfg.Accounts, config.AccountConfig{
			ID:             id,
			Name:           "default",
			Kind:           config.KindACP,
			Backend:        d.Backend,
			Enabled:        true,
			PermissionMode: config.PermDeny,
			ExposeMode:     config.ExposePool,
			Models:         models,
			MaxConcurrency: 1,
		})
		return &cfg.Accounts[len(cfg.Accounts)-1], true
	}

	switch msg.String() {
	case "up", "k":
		if a.hostAuthCursor > 0 {
			a.hostAuthCursor--
		}
	case "down", "j":
		if a.hostAuthCursor < len(items)-1 {
			a.hostAuthCursor++
		}
	case " ":
		b := items[a.hostAuthCursor].Backend
		a.hostAuthSelected[b] = !a.hostAuthSelected[b]
	case "c", "enter": // Copy host auth into isolated apx profiles (or link keyring-only host logins)
		cfg := a.srv.Config()
		copied := 0
		linked := 0
		var testCmds []tea.Cmd
		var errs []string
		for _, d := range items {
			if !a.hostAuthSelected[d.Backend] {
				continue
			}
			acct, _ := ensureAccountFor(cfg, d)
			if d.CanCopyFiles {
				pdir, err := config.EnsureProfileDir(a.srv.DataDir(), acct.Backend, acct.Name)
				if err != nil {
					errs = append(errs, d.Label+": "+err.Error())
					continue
				}
				if _, err := hostauth.CopyToProfile(d, pdir); err != nil {
					errs = append(errs, d.Label+": "+err.Error())
					continue
				}
				acct.UseHostProfile = false
				a.srv.ACP().StopAccount(acct.ID)
				a.srv.Pool().ClearHealth(acct.ID)
				copied++
				testCmds = append(testCmds, a.testAccountCmd(*acct))
			} else if d.KeyringOnly {
				acct.UseHostProfile = true
				a.srv.ACP().StopAccount(acct.ID)
				a.srv.Pool().ClearHealth(acct.ID)
				linked++
				testCmds = append(testCmds, a.testAccountCmd(*acct))
			}
		}
		cfg.UI.HostAuthPromptDismissed = true
		cfg.UI.OnboardingComplete = true
		_ = a.srv.ReloadConfig(cfg)
		a.modal = modalNone
		a.screen = ScreenAccounts
		if len(errs) > 0 && copied == 0 && linked == 0 {
			a.setStatus("Auth copy failed: "+strings.Join(errs, "; "), true)
		} else {
			var parts []string
			if copied > 0 {
				parts = append(parts, fmt.Sprintf("copied %d isolated profile(s)", copied))
			}
			if linked > 0 {
				parts = append(parts, fmt.Sprintf("linked %d keyring host login(s)", linked))
			}
			if len(parts) == 0 {
				a.setStatus("No accounts selected.", false)
			} else {
				a.setStatus("Imported host logins ("+strings.Join(parts, ", ")+") — testing...", false)
			}
		}
		return a, tea.Batch(testCmds...)

	case "h": // Reuse host CLI logins directly (UseHostProfile = true)
		cfg := a.srv.Config()
		linked := 0
		var testCmds []tea.Cmd
		for _, d := range items {
			if !a.hostAuthSelected[d.Backend] {
				continue
			}
			acct, _ := ensureAccountFor(cfg, d)
			acct.UseHostProfile = true
			a.srv.ACP().StopAccount(acct.ID)
			a.srv.Pool().ClearHealth(acct.ID)
			linked++
			testCmds = append(testCmds, a.testAccountCmd(*acct))
		}
		cfg.UI.HostAuthPromptDismissed = true
		cfg.UI.OnboardingComplete = true
		_ = a.srv.ReloadConfig(cfg)
		a.modal = modalNone
		a.screen = ScreenAccounts
		a.setStatus(fmt.Sprintf("Configured %d account(s) to reuse host CLI login — testing...", linked), false)
		return a, tea.Batch(testCmds...)

	case "r": // Re-authenticate fresh: create/keep isolated accounts without copying host auth
		cfg := a.srv.Config()
		var firstTarget *config.AccountConfig
		for _, d := range items {
			if !a.hostAuthSelected[d.Backend] {
				continue
			}
			acct, _ := ensureAccountFor(cfg, d)
			acct.UseHostProfile = false
			if firstTarget == nil {
				cp := *acct
				firstTarget = &cp
			}
		}
		cfg.UI.HostAuthPromptDismissed = true
		cfg.UI.OnboardingComplete = true
		_ = a.srv.ReloadConfig(cfg)
		a.screen = ScreenAccounts
		if firstTarget != nil {
			a.setStatus("Kept isolated profiles — sign in fresh or press 'l' on any account.", false)
			a.openLoginPrompt(*firstTarget)
			return a, nil
		}
		a.modal = modalNone
		return a, nil

	case "s", "esc", "q":
		dismiss()
		a.setStatus("Skipped host login import (available anytime via 'i' Setup or 'l' Login).", false)
	}
	return a, nil
}

func (a *App) viewHostAuthModal() string {
	cardW := clamp(a.width-10, 62, 88)
	innerW := cardW - 6

	var lines []string
	lines = append(lines, a.styles.Title.Render(fmt.Sprintf("◆ Detected Existing CLI Logins (%d)", len(a.hostAuthItems))))
	lines = append(lines, a.styles.Muted.Render("Copy your existing logins into isolated apx profiles (0600, host files untouched), reuse host logins, or sign in fresh."))
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))

	nameW := 20
	modeW := 16
	srcW := max(innerW-2-3-2-nameW-2-modeW-2, 12)
	for i, d := range a.hostAuthItems {
		mark := "[ ]"
		if a.hostAuthSelected[d.Backend] {
			mark = "[x]"
		}
		modePlain := "copy 0600 file"
		modeStyle := a.styles.StateReady
		if d.KeyringOnly {
			modePlain = "host keyring"
			modeStyle = a.styles.StateWarn
		}
		nStr := truncate(d.Label, nameW)
		sStr := truncate(d.SourceDesc, srcW)
		if i == a.hostAuthCursor {
			plain := fmt.Sprintf("> %s  %-*s  %-*s  %s", mark, nameW, nStr, modeW, modePlain, sStr)
			lines = append(lines, a.styles.Selected.Width(innerW).MaxWidth(innerW).Render(truncate(plain, innerW)))
		} else {
			lines = append(lines, a.styles.JoinRow(2,
				a.surfaceText("  "+mark),
				a.styles.PadCell(a.surfaceText(nStr), nameW),
				a.styles.PadCell(modeStyle.Render(modePlain), modeW),
				a.styles.Muted.Render(sStr),
			))
		}
	}

	lines = append(lines, "")
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
	lines = append(lines, a.styles.JoinHints(
		a.styles.KeyHint("space", "toggle"),
		a.styles.KeyHint("c / enter", "copy to isolated profiles"),
		a.styles.KeyHint("h", "reuse host login"),
	))
	lines = append(lines, a.styles.JoinHints(
		a.styles.KeyHint("r", "re-authenticate fresh"),
		a.styles.KeyHint("esc", "skip (don't ask again)"),
	))
	return a.styles.Modal.Width(cardW).Render(strings.Join(lines, "\n"))
}

func (a *App) filteredDetected() []descriptors.DetectedCLI {
	q := strings.ToLower(strings.TrimSpace(a.detectSearch.Value()))
	if q == "" && !a.detectInstalledOnly {
		return a.detected
	}
	var out []descriptors.DetectedCLI
	for _, d := range a.detected {
		if a.detectInstalledOnly && !d.Found {
			continue
		}
		if q != "" {
			hay := strings.ToLower(d.Descriptor.ID + " " + d.Descriptor.Name + " " + d.Descriptor.Description + " " + d.Descriptor.Detect)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		out = append(out, d)
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
	case "f":
		a.detectInstalledOnly = !a.detectInstalledOnly
		a.detectCursor = 0
	case " ":
		if a.detectCursor >= 0 && a.detectCursor < len(items) {
			d := items[a.detectCursor]
			if d.Found {
				a.detectSelected[d.Descriptor.ID] = !a.detectSelected[d.Descriptor.ID]
			} else if d.Descriptor.CanInstall() {
				a.setStatus(d.Descriptor.Name+" is not installed yet — press 'i' to install it first.", false)
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
	case "c":
		found := hostauth.DetectAll("")
		if len(found) == 0 {
			a.setStatus("No existing host CLI logins detected on this machine.", false)
			return a, nil
		}
		a.openHostAuthModal(found, nil, false)
		return a, nil
	case "enter":
		cfg := a.srv.Config()
		added := 0
		var addedHostLogins []hostauth.DetectedLogin
		addedTargets := make(map[string]string)
		for _, d := range a.detected {
			if !a.detectSelected[d.Descriptor.ID] {
				continue
			}
			acctName := "default"
			id := config.SanitizeSlug(d.Descriptor.ID + "-" + acctName)
			for n := 2; cfg.FindAccount(id) != nil; n++ {
				acctName = fmt.Sprintf("acct%d", n)
				id = config.SanitizeSlug(d.Descriptor.ID + "-" + acctName)
			}
			_, _ = config.EnsureProfileDir(a.srv.DataDir(), d.Descriptor.ID, acctName)
			cfg.Accounts = append(cfg.Accounts, config.AccountConfig{
				ID:             id,
				Name:           acctName,
				Kind:           config.KindACP,
				Backend:        d.Descriptor.ID,
				Enabled:        true,
				PermissionMode: config.PermDeny,
				ExposeMode:     config.ExposePool,
				Models:         append([]string(nil), d.Descriptor.DefaultModels...),
				MaxConcurrency: 1,
			})
			added++
			if hl, ok := hostauth.Detect(d.Descriptor.ID, ""); ok {
				addedHostLogins = append(addedHostLogins, hl)
				addedTargets[d.Descriptor.ID] = id
			}
		}
		cfg.UI.OnboardingComplete = true
		_ = a.srv.ReloadConfig(cfg)
		if len(addedHostLogins) > 0 {
			a.screen = ScreenAccounts
			a.openHostAuthModal(addedHostLogins, addedTargets, false)
			a.setStatus(fmt.Sprintf("Added %d account(s) — existing host login(s) detected!", added), false)
			return a, nil
		}
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

	installedTotal := 0
	for _, d := range a.detected {
		if d.Found {
			installedTotal++
		}
	}
	existingCounts := a.configuredBackendCounts()

	var lines []string
	title := fmt.Sprintf("◆ ACP Registry & CLI Installer (%d installed / %d total)", installedTotal, len(a.detected))
	if a.detectInstalledOnly {
		title += "  [installed only]"
	}
	lines = append(lines, a.styles.Title.Render(title))
	lines = append(lines, a.styles.Muted.Render("Press Space + Enter to add installed agents, 'i' to install new ones, or 'f' for installed only."))
	if len(a.hostAuthItems) > 0 {
		names := make([]string, 0, len(a.hostAuthItems))
		for _, hl := range a.hostAuthItems {
			names = append(names, hl.Label)
		}
		lines = append(lines, a.styles.StateReady.Render("● Existing logins detected: "+truncate(strings.Join(names, ", "), max(innerW-40, 14)))+"  "+a.styles.Muted.Render("(press 'c' to copy/use)"))
	}
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

	nameW := 18
	statW := 16
	descW := max(innerW-2-3-2-nameW-2-statW-2, 10)

	for i := start; i < end; i++ {
		d := items[i]
		mark := " · "
		if d.Found {
			if a.detectSelected[d.Descriptor.ID] {
				mark = "[x]"
			} else {
				mark = "[ ]"
			}
		}

		statusPlain := "○ manual"
		statusStyle := a.styles.Muted
		if d.Found {
			if n := existingCounts[d.Descriptor.ID]; n > 0 {
				statusPlain = fmt.Sprintf("● installed (%d)", n)
			} else {
				statusPlain = "● installed"
			}
			statusStyle = a.styles.StateReady
		} else if d.Descriptor.BinaryArchive != "" {
			statusPlain = "○ binary"
			statusStyle = a.styles.StateWarn
		} else if d.Descriptor.InstallPackage != "" {
			statusPlain = "○ npm"
			statusStyle = a.styles.StateWarn
		} else if d.Descriptor.UVXPackage != "" {
			statusPlain = "○ uvx"
			statusStyle = a.styles.StateWarn
		}

		desc := d.Descriptor.Description
		if d.Found && desc == "" {
			desc = d.BinaryPath
		}
		nameStr := truncate(d.Descriptor.Name, nameW)
		descStr := truncate(desc, descW)

		if i == a.detectCursor {
			plainRow := fmt.Sprintf("> %s  %-*s  %-*s  %s",
				mark,
				nameW, nameStr,
				statW, truncate(statusPlain, statW),
				descStr,
			)
			lines = append(lines, a.styles.Selected.Width(innerW).MaxWidth(innerW).Render(truncate(plainRow, innerW)))
		} else {
			row := a.styles.JoinRow(2,
				a.surfaceText("  "+mark),
				a.styles.PadCell(a.surfaceText(nameStr), nameW),
				a.styles.PadCell(statusStyle.Render(truncate(statusPlain, statW)), statW),
				a.styles.Muted.Render(descStr),
			)
			lines = append(lines, row)
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

	filterLabel := "installed only"
	if a.detectInstalledOnly {
		filterLabel = "show all"
	}
	lines = append(lines, a.styles.JoinHints(
		a.styles.KeyHint("/", "search"),
		a.styles.KeyHint("f", filterLabel),
		a.styles.KeyHint("i", "install"),
		a.styles.KeyHint("space", "select"),
		a.styles.KeyHint("enter", "add selected"),
	))
	lines = append(lines, a.styles.JoinHints(
		a.styles.KeyHint("R", "sync registry"),
		a.styles.KeyHint("c", "copy logins"),
		a.styles.KeyHint("a", "API key"),
		a.styles.KeyHint("C", "connect CLIs"),
		a.styles.KeyHint("esc", "close"),
	))
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
	case "c":
		if hl, ok := hostauth.Detect(acct.Backend, ""); ok && hl.CanCopyFiles {
			pdir, err := config.EnsureProfileDir(a.srv.DataDir(), acct.Backend, acct.Name)
			if err != nil {
				a.setStatus("Failed to prepare profile dir: "+err.Error(), true)
				return a, nil
			}
			if _, err := hostauth.CopyToProfile(hl, pdir); err != nil {
				a.setStatus("Copy failed: "+err.Error(), true)
				return a, nil
			}
			cfg := a.srv.Config()
			if ac := cfg.FindAccount(acct.ID); ac != nil {
				ac.UseHostProfile = false
				_ = a.srv.ReloadConfig(cfg)
			}
			a.srv.ACP().StopAccount(acct.ID)
			a.srv.Pool().ClearHealth(acct.ID)
			a.loginTarget = nil
			a.modal = modalNone
			a.setStatus("Copied host login into "+acct.Backend+"/"+acct.Name+" — testing...", false)
			return a, a.testAccountCmd(acct)
		}
	case "h":
		if _, ok := hostauth.Detect(acct.Backend, ""); ok {
			cfg := a.srv.Config()
			if ac := cfg.FindAccount(acct.ID); ac != nil {
				ac.UseHostProfile = true
				_ = a.srv.ReloadConfig(cfg)
				acct = *ac
			}
			a.srv.ACP().StopAccount(acct.ID)
			a.srv.Pool().ClearHealth(acct.ID)
			a.loginTarget = nil
			a.modal = modalNone
			a.setStatus("Switched "+acct.Backend+"/"+acct.Name+" to reuse host login — testing...", false)
			return a, a.testAccountCmd(acct)
		}
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
	hl, hasHostLogin := hostauth.Detect(acct.Backend, "")
	if hasHostLogin {
		lines = append(lines, a.styles.StateReady.Render("● Existing host login detected: "+truncate(hl.SourceDesc, max(innerW-34, 16))))
		if hl.CanCopyFiles {
			lines = append(lines, a.styles.Muted.Render("  Press 'c' to copy auth (0600) into this isolated profile, 'h' to reuse host directly, or Enter to sign in fresh."))
		} else {
			lines = append(lines, a.styles.Muted.Render("  Press 'h' to reuse host keyring login directly, or Enter to sign in fresh to an isolated profile."))
		}
		lines = append(lines, "")
	}
	lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))

	var hints []string
	if hasHostLogin && hl.CanCopyFiles {
		hints = append(hints, a.styles.KeyHint("c", "copy host auth"))
	}
	if hasHostLogin {
		hints = append(hints, a.styles.KeyHint("h", "reuse host"))
	}
	if binMissing && canInstall {
		hints = append(hints, a.styles.KeyHint("i", "install "+binName))
	} else if loginCmd != "" {
		hints = append(hints, a.styles.KeyHint("enter", "re-auth fresh"))
	}
	hints = append(hints, a.styles.KeyHint("s", "skip"))
	lines = append(lines, a.styles.JoinHints(hints...))
	return a.styles.Modal.Width(cardW).Render(strings.Join(lines, "\n"))
}

// --- Add Account Wizard ---

func (a *App) openAddAccount() {
	a.addStep = 0
	a.addKind = config.KindAPIKey
	a.addBackend = ""
	a.addListCur = 0
	a.resetAddSearch()
	a.addFocus = 0
	a.addTesting = false
	a.modal = modalAddAccount
}

func (a *App) resetAddSearch() {
	a.addSearching = false
	a.addSearch.SetValue("")
	a.addSearch.Blur()
}

func (a *App) filteredAddPresets() []descriptors.ProviderPreset {
	all := a.srv.Registry().ListPresets()
	q := strings.ToLower(strings.TrimSpace(a.addSearch.Value()))
	if q == "" {
		return all
	}
	var out []descriptors.ProviderPreset
	for _, p := range all {
		hay := strings.ToLower(p.ID + " " + p.Name + " " + p.BaseURL)
		if strings.Contains(hay, q) {
			out = append(out, p)
		}
	}
	return out
}

func (a *App) filteredAddCLIs() []descriptors.CLIDescriptor {
	all := a.srv.Registry().ListCLI()
	q := strings.ToLower(strings.TrimSpace(a.addSearch.Value()))
	if q == "" {
		return all
	}
	var out []descriptors.CLIDescriptor
	for _, c := range all {
		hay := strings.ToLower(c.ID + " " + c.Name + " " + c.Binary + " " + c.Description)
		if strings.Contains(hay, q) {
			out = append(out, c)
		}
	}
	return out
}

func (a *App) filteredAddACPs() []descriptors.ACPDescriptor {
	all := a.srv.Registry().ListACP()
	q := strings.ToLower(strings.TrimSpace(a.addSearch.Value()))
	if q == "" {
		return all
	}
	var out []descriptors.ACPDescriptor
	for _, d := range all {
		hay := strings.ToLower(d.ID + " " + d.Name + " " + d.Detect + " " + d.Description)
		if strings.Contains(hay, q) {
			out = append(out, d)
		}
	}
	return out
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
		return a, a.fetchModelsCmd(acct.ID, acct, apiKey)
	}
	if acct.Kind == config.KindCLI {
		if desc, ok := a.srv.Registry().GetCLI(acct.Backend); ok && len(desc.ModelsCmd) > 0 {
			a.modelPicker.loading = true
			a.setStatus("Loading live models from "+desc.Binary+"...", false)
			return a, a.fetchModelsCmd(acct.ID, acct, "")
		}
	}
	return a, nil
}

func (a *App) updateAddAccountModal(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.addStep == 1 && a.addSearching {
		switch msg.String() {
		case "esc", "enter":
			a.addSearching = false
			a.addSearch.Blur()
			a.addListCur = 0
			return a, nil
		default:
			var cmd tea.Cmd
			a.addSearch, cmd = a.addSearch.Update(msg)
			a.addListCur = 0
			return a, cmd
		}
	}

	if msg.String() == "esc" {
		if a.addStep == 1 && strings.TrimSpace(a.addSearch.Value()) != "" {
			a.resetAddSearch()
			a.addListCur = 0
			return a, nil
		}
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
			a.resetAddSearch()
		case "2":
			a.addKind = config.KindACP
			a.addStep = 1
			a.addListCur = 0
			a.resetAddSearch()
		case "3":
			a.addKind = config.KindCLI
			a.addStep = 1
			a.addListCur = 0
			a.resetAddSearch()
		case "enter":
			a.addStep = 1
			a.addListCur = 0
			a.resetAddSearch()
		case "q":
			a.modal = modalNone
		}
		return a, nil

	case 1:
		if msg.String() == "/" {
			a.addSearching = true
			a.addSearch.Focus()
			return a, textinput.Blink
		}
		if a.addKind == config.KindAPIKey {
			presets := a.filteredAddPresets()
			if a.addListCur >= len(presets) && len(presets) > 0 {
				a.addListCur = len(presets) - 1
			}
			switch msg.String() {
			case "up", "k":
				if a.addListCur > 0 {
					a.addListCur--
				}
			case "down", "j":
				if a.addListCur < len(presets)-1 {
					a.addListCur++
				}
			case "pgup":
				a.addListCur = max(a.addListCur-8, 0)
			case "pgdown":
				if len(presets) > 0 {
					a.addListCur = min(a.addListCur+8, len(presets)-1)
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
			clis := a.filteredAddCLIs()
			if a.addListCur >= len(clis) && len(clis) > 0 {
				a.addListCur = len(clis) - 1
			}
			switch msg.String() {
			case "up", "k":
				if a.addListCur > 0 {
					a.addListCur--
				}
			case "down", "j":
				if a.addListCur < len(clis)-1 {
					a.addListCur++
				}
			case "pgup":
				a.addListCur = max(a.addListCur-8, 0)
			case "pgdown":
				if len(clis) > 0 {
					a.addListCur = min(a.addListCur+8, len(clis)-1)
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
			descs := a.filteredAddACPs()
			if a.addListCur >= len(descs) && len(descs) > 0 {
				a.addListCur = len(descs) - 1
			}
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
				if len(descs) > 0 {
					a.addListCur = min(a.addListCur+8, len(descs)-1)
				}
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
		case "f", "F":
			acct, apiKey, err := a.buildDraftAccount()
			if err != nil {
				a.setStatus(err.Error(), true)
				return a, nil
			}
			p.loading = true
			p.fetchErr = ""
			return a, a.fetchModelsCmd(acct.ID, acct, apiKey)
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
		if a.addSearching {
			lines = append(lines, a.styles.Highlight.Render("Search: ")+a.addSearch.View()+" "+a.styles.Muted.Render("(enter/esc)"))
		} else if q := strings.TrimSpace(a.addSearch.Value()); q != "" {
			lines = append(lines, a.styles.Muted.Render(fmt.Sprintf("Search: %q  (/ edit, esc clear)", q)))
		}
		maxRows := clamp(a.height-17, 6, 13)
		if a.addKind == config.KindAPIKey {
			presets := a.filteredAddPresets()
			if len(presets) == 0 {
				lines = append(lines, a.styles.Muted.Render("  No matching API providers. Press '/' to edit or 'esc' to clear."))
			}
			start := 0
			if a.addListCur >= maxRows {
				start = a.addListCur - maxRows + 1
			}
			nameW := 22
			urlW := max(innerW-2-nameW-2, 12)
			for i := start; i < min(start+maxRows, len(presets)); i++ {
				p := presets[i]
				nStr := truncate(p.Name, nameW)
				uStr := truncate(p.BaseURL, urlW)
				if i == a.addListCur {
					plain := fmt.Sprintf("> %-*s  %s", nameW, nStr, uStr)
					lines = append(lines, a.styles.Selected.Width(innerW).MaxWidth(innerW).Render(truncate(plain, innerW)))
				} else {
					lines = append(lines, a.styles.JoinRow(2,
						a.styles.PadCell(a.surfaceText("  "+nStr), nameW+2),
						a.styles.Muted.Render(uStr),
					))
				}
			}
			lines = append(lines, "")
			lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
			lines = append(lines, a.styles.JoinHints(a.styles.KeyHint("/", "search"), a.styles.KeyHint("enter", "next"), a.styles.KeyHint("esc", "back")))
		} else if a.addKind == config.KindCLI {
			clis := a.filteredAddCLIs()
			if len(clis) == 0 {
				lines = append(lines, a.styles.Muted.Render("  No matching CLI tools. Press '/' to edit or 'esc' to clear."))
			}
			start := 0
			if a.addListCur >= maxRows {
				start = a.addListCur - maxRows + 1
			}
			nameW := 22
			badgeW := 10
			descW := max(innerW-2-nameW-2-badgeW-2, 10)
			for i := start; i < min(start+maxRows, len(clis)); i++ {
				c := clis[i]
				badgePlain := "custom"
				badgeStyle := a.styles.Muted
				if c.Binary != "" {
					if _, found := c.FoundOnPath(); found {
						badgePlain = "● found"
						badgeStyle = a.styles.StateReady
					} else {
						badgePlain = "○ missing"
						badgeStyle = a.styles.StateWarn
					}
				}
				nStr := truncate(c.Name, nameW)
				dStr := truncate(c.Description, descW)
				if i == a.addListCur {
					plain := fmt.Sprintf("> %-*s  %-*s  %s", nameW, nStr, badgeW, badgePlain, dStr)
					lines = append(lines, a.styles.Selected.Width(innerW).MaxWidth(innerW).Render(truncate(plain, innerW)))
				} else {
					lines = append(lines, a.styles.JoinRow(2,
						a.styles.PadCell(a.surfaceText("  "+nStr), nameW+2),
						a.styles.PadCell(badgeStyle.Render(badgePlain), badgeW),
						a.styles.Muted.Render(dStr),
					))
				}
			}
			lines = append(lines, "")
			lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
			lines = append(lines, a.styles.JoinHints(a.styles.KeyHint("/", "search"), a.styles.KeyHint("enter", "next"), a.styles.KeyHint("esc", "back")))
		} else {
			descs := a.filteredAddACPs()
			if len(descs) == 0 {
				lines = append(lines, a.styles.Muted.Render("  No matching ACP agents. Press '/' to edit or 'esc' to clear."))
			}
			start := 0
			if a.addListCur >= maxRows {
				start = a.addListCur - maxRows + 1
			}
			nameW := 20
			badgeW := 13
			descW := max(innerW-2-nameW-2-badgeW-2, 10)
			for i := start; i < min(start+maxRows, len(descs)); i++ {
				d := descs[i]
				badgePlain := "custom"
				badgeStyle := a.styles.Muted
				if d.ID != "custom-acp" {
					if _, found := a.srv.Registry().FindLocalBinary(d); found {
						badgePlain = "● installed"
						badgeStyle = a.styles.StateReady
					} else if d.CanInstall() {
						badgePlain = "○ i: install"
						badgeStyle = a.styles.StateWarn
					} else {
						badgePlain = "○ manual"
					}
				}
				nStr := truncate(d.Name, nameW)
				dStr := truncate(d.Description, descW)
				if i == a.addListCur {
					plain := fmt.Sprintf("> %-*s  %-*s  %s", nameW, nStr, badgeW, badgePlain, dStr)
					lines = append(lines, a.styles.Selected.Width(innerW).MaxWidth(innerW).Render(truncate(plain, innerW)))
				} else {
					lines = append(lines, a.styles.JoinRow(2,
						a.styles.PadCell(a.surfaceText("  "+nStr), nameW+2),
						a.styles.PadCell(badgeStyle.Render(badgePlain), badgeW),
						a.styles.Muted.Render(dStr),
					))
				}
			}
			lines = append(lines, "")
			lines = append(lines, a.styles.Divider.Render(strings.Repeat("─", innerW)))
			lines = append(lines, a.styles.JoinHints(
				a.styles.KeyHint("/", "search"),
				a.styles.KeyHint("enter", "next"),
				a.styles.KeyHint("i", "install CLI"),
				a.styles.KeyHint("pgup/dn", "scroll"),
				a.styles.KeyHint("esc", "back"),
			))
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
