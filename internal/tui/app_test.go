package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/gateway"
	"github.com/bberka/agentpassage/internal/hostauth"
	"github.com/bberka/agentpassage/internal/logbuf"
	"github.com/bberka/agentpassage/internal/secrets"
)

func TestTUIFullScreenScreensAndModals(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.UI.OnboardingComplete = true
	cfg.UI.HostAuthPromptDismissed = true
	cfg.Accounts = []config.AccountConfig{
		{ID: "agy-a", Name: "a", Kind: config.KindACP, Backend: "agy", Enabled: true, ExposeMode: config.ExposeBoth, ModelPrefix: "a-", Models: []string{"gemini-4-neon"}},
		{ID: "deepseek-main", Name: "main", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, BaseURL: "https://api.deepseek.com/v1", Models: []string{"deepseek-chat"}},
	}
	cfg.Routing = []config.RoutingRule{
		{Pattern: "agy/*", Strategy: config.StrategyFailover, AllowCrossAccountRetryOn429: true},
		{Pattern: "deepseek/*", Strategy: config.StrategyManual, AllowCrossAccountRetryOn429: true},
	}

	sec := secrets.NewFileOnly(dir)
	reg, _ := descriptors.LoadRegistry(dir)
	logs := logbuf.New(50, dir, sec.KnownSecrets)
	defer logs.Close()

	srv := gateway.New(dir, cfg, sec, reg, logs)
	app := New(srv)
	defer app.Close()

	// Test both compact 80x24 and wide 120x36 full-screen layouts
	for _, sz := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 120, Height: 36}} {
		app.Update(sz)
		for idx, key := range []string{"1", "2", "3", "4", "5", "6", "7"} {
			app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
			out := app.View()
			if len(strings.TrimSpace(out)) == 0 {
				t.Fatalf("size %dx%d screen %d rendered empty output", sz.Width, sz.Height, idx)
			}
		}
	}

	// Open ACP Registry & Installer modal (i) and verify moving cursor never wraps a row to 2 lines!
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	if app.modal != modalWelcome || !strings.Contains(app.View(), "ACP Registry") {
		t.Fatalf("expected ACP Registry modal on 'i'")
	}
	baseH := strings.Count(app.viewWelcomeModal(), "\n")
	for step := 0; step < 12; step++ {
		app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
		if gotH := strings.Count(app.viewWelcomeModal(), "\n"); gotH != baseH {
			t.Fatalf("welcome modal height changed at row %d: got %d lines, want %d (row wrapped!)", step+1, gotH, baseH)
		}
	}
	app.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// Open Connect CLIs & T3 Code modal (C)
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("C")})
	if app.modal != modalConnect || !strings.Contains(app.View(), "T3 Code") {
		t.Fatalf("expected Connect modal with T3 Code on 'C'")
	}
	app.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// Open Background Service & Auto-Start modal (S)
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	if app.modal != modalService || !strings.Contains(app.View(), "Background Service & Auto-Start") {
		t.Fatalf("expected Service modal on 'S', got modal=%v", app.modal)
	}
	app.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// Go to Routing (4): failover -> sticky -> manual cycle freely, then round-robin prompts with ban-risk warning!
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4")})
	for _, want := range []string{config.StrategySticky, config.StrategyManual} {
		app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
		if app.modal != modalNone || app.srv.Config().Routing[0].Strategy != want {
			t.Fatalf("expected clean switch to %q, got modal=%v strategy=%q", want, app.modal, app.srv.Config().Routing[0].Strategy)
		}
	}
	// Next 's' on subscription rule (agy/*) -> round-robin triggers Subscription Ban-Risk Warning!
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if app.modal != modalSubStrategyWarn || !strings.Contains(app.View(), "Subscription Ban-Risk Warning") {
		t.Fatalf("expected Subscription Ban-Risk Warning modal on agy/* round-robin, got modal=%v", app.modal)
	}
	// Pressing 'f' returns safely to primary-first failover.
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("f")})
	if app.modal != modalNone || app.srv.Config().Routing[0].Strategy != config.StrategyFailover {
		t.Fatalf("expected 'f' to keep failover, got modal=%v strategy=%q", app.modal, app.srv.Config().Routing[0].Strategy)
	}
	// Move to API-key rule (deepseek/*): pressing 's' from manual -> round-robin asks confirmation & reminds about subscription bans.
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if app.modal != modalSubStrategyWarn || !strings.Contains(app.View(), "API-key accounts") {
		t.Fatalf("expected API-key confirmation modal with subscription warning, got modal=%v", app.modal)
	}
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if app.modal != modalNone || app.srv.Config().Routing[1].Strategy != config.StrategyRoundRobin || app.srv.Config().Routing[1].SubscriptionWarningAcked {
		t.Fatalf("expected deepseek/* enabled as round-robin without setting SubscriptionWarningAcked, got %+v", app.srv.Config().Routing[1])
	}

	// Open Accounts (2) -> e opens structured Account Editor modal (not comma-separated model text!)
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("e")})
	if app.modal != modalEditAccount || !strings.Contains(app.View(), "Edit Account") {
		t.Fatalf("expected Account Editor modal on 'e', got modal=%v", app.modal)
	}
	app.Update(tea.KeyMsg{Type: tea.KeyEsc})

	// Safe rename moves profile dir & updates routing rules
	if _, err := config.EnsureProfileDir(dir, "agy", "a"); err != nil {
		t.Fatal(err)
	}
	if err := app.renameAccount("agy-a", "work"); err != nil {
		t.Fatalf("renameAccount failed: %v", err)
	}
	if app.srv.Config().FindAccount("agy-work") == nil {
		t.Fatalf("expected renamed account agy-work in config")
	}

	// Accounts -> M opens interactive Account Models Picker; Space toggles, Enter saves
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("M")})
	if app.modal != modalAccountModels || !strings.Contains(app.View(), "Models —") {
		t.Fatalf("expected Account Models Picker modal on 'M', got modal=%v", app.modal)
	}
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if app.modal != modalNone {
		t.Fatalf("expected modal closed after Enter")
	}
}

func TestAPISetupAndModelsPickerAutoFetch(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer sk-test-live" {
			http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "custom-model-alpha"},
				{"id": "custom-model-beta"},
			},
		})
	}))
	defer ts.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.UI.OnboardingComplete = true
	cfg.UI.HostAuthPromptDismissed = true
	sec := secrets.NewFileOnly(dir)
	reg, _ := descriptors.LoadRegistry(dir)
	logs := logbuf.New(50, dir, sec.KnownSecrets)
	defer logs.Close()

	srv := gateway.New(dir, cfg, sec, reg, logs)
	app := New(srv)
	defer app.Close()
	app.Update(tea.WindowSizeMsg{Width: 110, Height: 34})

	// 1. Add Account wizard: API Key -> Custom -> fill name/key/url -> ctrl+s must auto-fetch!
	app.openAddAccount()
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")}) // API Key -> step 1
	presets := srv.Registry().ListPresets()
	customIdx := -1
	for i, p := range presets {
		if p.ID == "custom" {
			customIdx = i
			break
		}
	}
	if customIdx < 0 {
		t.Fatalf("custom preset missing")
	}
	app.addListCur = customIdx
	app.Update(tea.KeyMsg{Type: tea.KeyEnter}) // step 1 -> step 2
	app.addInputs[0].SetValue("demo")
	app.addInputs[1].SetValue("sk-test-live")
	app.addInputs[2].SetValue(ts.URL + "/v1")

	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if app.addStep != 3 || app.modelPicker == nil || !app.modelPicker.loading {
		t.Fatalf("expected step 3 with loading=true, got step=%d picker=%v", app.addStep, app.modelPicker)
	}
	if cmd == nil {
		t.Fatalf("expected non-nil auto-fetch tea.Cmd on entering step 4")
	}
	// While loading with empty list, should NOT tell user to press 'f' yet
	if strings.Contains(app.viewAddAccountModal(), "Press 'f' to fetch") {
		t.Fatalf("unexpected 'Press f' prompt while loading")
	}

	// Execute auto-fetch command without pressing 'f'
	msg := cmd()
	app.Update(msg)
	if app.modelPicker.loading {
		t.Fatalf("expected loading=false after fetchModelsMsg")
	}
	if len(app.modelPicker.models) != 2 || app.modelPicker.enabledCount() != 2 {
		t.Fatalf("expected 2 auto-fetched models, got models=%v enabled=%d", app.modelPicker.models, app.modelPicker.enabledCount())
	}

	// Save account
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if app.modal != modalNone {
		t.Fatalf("expected wizard closed after save")
	}

	// 2. Fast-save before fetch returns: simulate saving another account before fetchModelsMsg arrives
	app.openAddAccount()
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	app.addListCur = customIdx
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	app.addInputs[0].SetValue("fast")
	app.addInputs[1].SetValue("sk-test-live")
	app.addInputs[2].SetValue(ts.URL + "/v1")
	_, fastCmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if fastCmd == nil {
		t.Fatalf("expected non-nil auto-fetch cmd")
	}
	// User hits Enter immediately before fastCmd completes
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	// Now fastCmd finishes in the background
	app.Update(fastCmd())
	fastAcct := srv.Config().FindAccount("custom-fast")
	if fastAcct == nil || len(fastAcct.Models) != 2 {
		t.Fatalf("expected background fetchModelsMsg to populate fast-saved account, got %+v", fastAcct)
	}

	// 3. Opening existing API account Models Picker (Enter/M on Accounts) must auto-fetch too
	// Disable one model first to verify auto-fetch preserves user's disabled selection
	fastAcct.Models = []string{"custom-model-alpha"}
	fastAcct.DisabledModels = []string{"custom-model-beta"}
	fastAcct.AvailableModels = []string{"custom-model-alpha"}
	_ = srv.ReloadConfig(srv.Config())

	openCmd := app.openAccountModelsModal(*fastAcct)
	if openCmd == nil || app.modelPicker == nil || !app.modelPicker.loading {
		t.Fatalf("expected openAccountModelsModal to auto-fetch for API account")
	}
	app.Update(openCmd())
	if app.modelPicker.loading {
		t.Fatalf("expected loading=false after openAccountModelsModal fetch")
	}
	if app.modelPicker.enabled["custom-model-beta"] {
		t.Fatalf("expected disabled model to stay disabled after auto-fetch")
	}
	if !app.modelPicker.enabled["custom-model-alpha"] {
		t.Fatalf("expected enabled model to stay enabled")
	}

	// 4. Add Account Step 2 provider search (API Key, ACP, CLI) + Connect modal search
	app.openAddAccount()
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")}) // API Key -> Step 1
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	for _, r := range "openrouter" {
		app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	app.Update(tea.KeyMsg{Type: tea.KeyEnter}) // exit search
	if got := app.filteredAddPresets(); len(got) != 1 || got[0].ID != "openrouter" {
		t.Fatalf("expected openrouter from provider search, got %+v", got)
	}
	app.Update(tea.KeyMsg{Type: tea.KeyEnter}) // select filtered provider -> step 2
	if app.addStep != 2 || app.addBackend != "openrouter" {
		t.Fatalf("expected step 2 with backend openrouter, got step=%d backend=%q", app.addStep, app.addBackend)
	}

	// ACP agent search
	app.openAddAccount()
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")}) // ACP -> Step 1
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	for _, r := range "muse" {
		app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := app.filteredAddACPs(); len(got) == 0 || got[0].ID != "muse-code" {
		t.Fatalf("expected muse-code from ACP search, got %+v", got)
	}

	// Connect modal search
	app.openConnectModal()
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	for _, r := range "zed" {
		app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := app.filteredConnectTargets(); len(got) != 1 || got[0].ID != "zed" {
		t.Fatalf("expected zed from connect search, got %+v", got)
	}
}

func TestUsageScreenShowsAllAndScrolls(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.UI.OnboardingComplete = true
	cfg.UI.HostAuthPromptDismissed = true
	for i := 1; i <= 12; i++ {
		name := string(rune('a' + i - 1))
		cfg.Accounts = append(cfg.Accounts, config.AccountConfig{
			ID:      "agy-" + name,
			Name:    name,
			Kind:    config.KindACP,
			Backend: "agy",
			Enabled: true,
		})
	}
	sec := secrets.NewFileOnly(dir)
	reg, _ := descriptors.LoadRegistry(dir)
	logs := logbuf.New(20, dir, sec.KnownSecrets)
	defer logs.Close()
	srv := gateway.New(dir, cfg, sec, reg, logs)
	app := New(srv)
	defer app.Close()

	// Tall terminal should display all 12 accounts with no truncation
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 38})
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("6")})
	view := app.View()
	if !strings.Contains(view, "agy/l") {
		t.Fatalf("expected 12th account agy/l visible on tall terminal")
	}

	// Short terminal should allow scrolling down to reveal the last account
	app.Update(tea.WindowSizeMsg{Width: 120, Height: 24})
	for i := 0; i < 10; i++ {
		app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	}
	scrolled := app.View()
	if !strings.Contains(scrolled, "agy/l") {
		t.Fatalf("expected scrolled usage screen to reveal agy/l")
	}
}

func TestHostAuthDetectionAndCopyModal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")

	// Create mock host logins for Claude and Codex
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"token":"host-claude"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"token":"host-codex"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	cfg := config.Default()
	cfg.UI.OnboardingComplete = true
	cfg.UI.HostAuthPromptDismissed = true
	sec := secrets.NewFileOnly(dir)
	reg, _ := descriptors.LoadRegistry(dir)
	logs := logbuf.New(20, dir, sec.KnownSecrets)
	defer logs.Close()
	srv := gateway.New(dir, cfg, sec, reg, logs)
	app := New(srv)
	defer app.Close()
	app.Update(tea.WindowSizeMsg{Width: 110, Height: 34})

	if !app.openStartupHostAuthIfNeeded(home) {
		t.Fatalf("expected startup host auth prompt to open when host logins exist")
	}
	if app.modal != modalHostAuth || !strings.Contains(app.View(), "Detected Existing CLI Logins") {
		t.Fatalf("expected modalHostAuth view, got modal=%v", app.modal)
	}

	// Confirm copy with 'c' -> creates isolated accounts and copies credentials into profile dirs
	app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c")})
	if app.modal != modalNone {
		t.Fatalf("expected modal closed after 'c' copy")
	}
	if srv.Config().FindAccount("claude-default") == nil || srv.Config().FindAccount("codex-default") == nil {
		t.Fatalf("expected claude-default and codex-default accounts created, got %+v", srv.Config().Accounts)
	}
	pClaude, _ := config.ProfileDir(dir, "claude", "default")
	pCodex, _ := config.ProfileDir(dir, "codex", "default")
	if !hostauth.ProfileHasAuth("claude", pClaude) || !hostauth.ProfileHasAuth("codex", pCodex) {
		t.Fatalf("expected copied credentials in isolated profile dirs")
	}
}
