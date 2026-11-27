package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/agentpassage/internal/acpbridge"
	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/connect"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/doctor"
	"github.com/bberka/agentpassage/internal/gateway"
	"github.com/bberka/agentpassage/internal/health"
	"github.com/bberka/agentpassage/internal/logbuf"
	"github.com/bberka/agentpassage/internal/quota"
	"github.com/bberka/agentpassage/internal/router"
	"github.com/bberka/agentpassage/internal/secrets"
	"github.com/bberka/agentpassage/internal/service"
	"github.com/bberka/agentpassage/internal/tui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "apx: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "status":
			return cmdStatus(args[1:])
		case "serve":
			return cmdServe(args[1:])
		case "service":
			return cmdService(args[1:])
		case "acp":
			return cmdACPBridge(args[1:])
		case "accounts", "account":
			return cmdAccounts(args[1:])
		case "add":
			return cmdAdd(args[1:])
		case "list", "ls":
			return cmdList(args[1:])
		case "models":
			return cmdModels(args[1:])
		case "routing", "route":
			return cmdRouting(args[1:])
		case "privacy":
			return cmdPrivacy(args[1:])
		case "embed", "embeddings":
			return cmdEmbed(args[1:])
		case "rerank":
			return cmdRerank(args[1:])
		case "usage":
			return cmdUsage(args[1:])
		case "health":
			return cmdHealth(args[1:])
		case "doctor":
			return cmdDoctor(args[1:])
		case "install":
			return cmdInstall(args[1:])
		case "connect":
			return cmdConnect(args[1:])
		case "disconnect":
			return cmdDisconnect(args[1:])
		case "uninstall":
			return cmdUninstall(args[1:])
		case "keys":
			return cmdKeys(args[1:])
		case "remote":
			return cmdRemote(args[1:])
		case "bench":
			return cmdBench(args[1:])
		case "decision":
			return cmdDecision(args[1:])
		case "registry":
			return cmdRegistry(args[1:])
		case "export-env":
			return cmdExportEnv(args[1:])
		case "export-config":
			return cmdExportConfig(args[1:])
		case "import-config":
			return cmdImportConfig(args[1:])
		case "agent-guide":
			return cmdAgentGuide()
		case "version":
			fmt.Printf("AgentPassage (apx) %s\n", gateway.Version)
			return nil
		case "help", "-h", "--help":
			printUsage()
			return nil
		default:
			return fmt.Errorf("unknown command %q (run `apx help`)", args[0])
		}
	}

	fs := flag.NewFlagSet("apx", flag.ContinueOnError)
	noTUI := fs.Bool("no-tui", false, "run the HTTP gateway only without opening the TUI")
	port := fs.Int("port", 0, "override server port")
	bind := fs.String("bind", "", "override bind address (default 127.0.0.1)")
	allowRemote := fs.Bool("allow-remote", false, "allow binding to non-loopback address")
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.Usage = printUsage
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Printf("apx %s\n", gateway.Version)
		return nil
	}

	if *noTUI {
		return startHeadless(*port, *bind, *allowRemote)
	}
	return startTUI(*port, *bind, *allowRemote)
}

func printUsage() {
	fmt.Println(`AgentPassage (apx) — multi-account ACP, CLI, and API-key gateway

Usage:
  apx [flags]                     Start gateway and open full-screen TUI
  apx status [--json]             Show gateway, service, catalog, and privacy status
  apx serve [flags]               Start headless HTTP gateway
  apx service <cmd>               Manage OS background service (install|uninstall|start|stop|restart|status|logs)
  apx acp                         Run apx as an ACP stdio bridge (for T3 Code, Zed, JetBrains...)
  apx accounts <cmd>              Manage accounts (list|add|enable|disable|edit|remove)
  apx models <cmd>                Manage models (list|fetch|enable|disable)
  apx routing <cmd>               Manage routing, project rules & model fallbacks (list|add|edit|remove|explain)
  apx privacy <cmd>               Configure & test reversible privacy filter (status|enable|disable|config|test)
  apx keys <cmd>                  Manage scoped client/admin/project API keys (list|create|enable|disable|revoke)
  apx usage [--period 1h|24h|7d|30d|all] [--refresh] [--json]
                                  Print usage, estimated costs, and live subscription limits
  apx health [--json] [acct]      Run live health probes across configured accounts
  apx embed --model <m> <text>    Call POST /v1/embeddings through the gateway
  apx rerank --model <m> --query <q> <docs...>
                                  Call POST /v1/rerank through the gateway
  apx connect [client...]         Connect IDEs & coding CLIs (T3 Code, Zed, VS Code, JetBrains, OpenCode...)
  apx disconnect <client|all>     Cleanly remove apx from connected IDEs & CLIs
  apx uninstall [--yes]           Disconnect all IDEs/CLIs and uninstall the background service
  apx install <tool|all>          Install an ACP agent from the 45+ agent registry
  apx remote                      Show Tailscale Serve & Cloudflare Tunnel setup guide
  apx doctor                      Check CLIs, profiles, storage, and config
  apx registry sync               Fetch latest official ACP Registry JSON
  apx export-env [--shell sh]     Print shell exports for OpenAI/Anthropic clients
  apx agent-guide                 Print compact CLI automation reference for AI agents
  apx version                     Print version

Flags (root / serve):
  --no-tui                        Start headless HTTP gateway without TUI
  --port <int>                    Override listening port (default 8787)
  --bind <addr>                   Override bind address (default 127.0.0.1)
  --allow-remote                  Allow binding to a non-loopback address`)
}

func loadRuntime(port int, bind string, allowRemote bool) (string, *config.Config, *secrets.Store, *descriptors.Registry, *logbuf.Buffer, error) {
	dataDir, err := config.DataDir()
	if err != nil {
		return "", nil, nil, nil, nil, err
	}
	cfg, _, err := config.Load(dataDir)
	if err != nil {
		return "", nil, nil, nil, nil, err
	}
	if port > 0 {
		cfg.Server.Port = port
	}
	if bind != "" {
		cfg.Server.Bind = bind
	}
	if allowRemote {
		cfg.Server.AllowRemote = true
	}
	sec := secrets.New(dataDir)
	reg, loadErrs := descriptors.LoadRegistry(dataDir)
	logs := logbuf.New(1000, dataDir, sec.KnownSecrets)
	for _, e := range loadErrs {
		logs.Warn("system", "descriptor load warning: "+e.Error())
	}
	return dataDir, cfg, sec, reg, logs, nil
}

func startTUI(port int, bind string, allowRemote bool) error {
	dataDir, cfg, sec, reg, logs, err := loadRuntime(port, bind, allowRemote)
	if err != nil {
		return err
	}
	defer logs.Close()

	srv := gateway.New(dataDir, cfg, sec, reg, logs)
	if err := srv.Start(); err != nil {
		// If another apx daemon or background service is already listening on this port,
		// attach the TUI to it seamlessly instead of failing with address-in-use!
		if ok, attachErr := srv.AttachIfRunning(); !ok {
			return fmt.Errorf("%w (hint: port is held by another process; try `apx --port 8788` — attach check: %v)", err, attachErr)
		}
	}
	defer func() {
		if !srv.Attached() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Stop(ctx)
		}
	}()

	app := tui.New(srv)
	defer app.Close()

	p := tea.NewProgram(app, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

func startHeadless(port int, bind string, allowRemote bool) error {
	dataDir, cfg, sec, reg, logs, err := loadRuntime(port, bind, allowRemote)
	if err != nil {
		return err
	}
	defer logs.Close()

	if !cfg.IsLoopbackBind() && !cfg.Server.AllowRemote {
		return fmt.Errorf("refusing to bind to non-loopback address %q without --allow-remote", cfg.Server.Bind)
	}
	if !cfg.IsLoopbackBind() {
		fmt.Fprintf(os.Stderr, "WARNING: binding to non-loopback address %s:%d — ensure your firewall protects this port\n", cfg.Server.Bind, cfg.Server.Port)
	}

	srv := gateway.New(dataDir, cfg, sec, reg, logs)
	if err := srv.Start(); err != nil {
		return err
	}
	fmt.Printf("apx %s listening on http://%s/v1\n", gateway.Version, srv.Addr())
	fmt.Printf("Local API key: %s (secrets backend: %s)\n", secrets.Mask(cfg.Server.LocalKey), sec.Backend())
	if sec.UsingFileFallback() {
		fmt.Fprintf(os.Stderr, "Notice: OS keychain unavailable; API keys are stored in a 0600 file in %s\n", dataDir)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Stop(ctx)
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	port := fs.Int("port", 0, "override server port")
	bind := fs.String("bind", "", "override bind address")
	allowRemote := fs.Bool("allow-remote", false, "allow non-loopback bind")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return startHeadless(*port, *bind, *allowRemote)
}

func cmdService(args []string) error {
	dataDir, _, _, _, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	sub := "status"
	if len(args) > 0 {
		sub = args[0]
	}

	switch sub {
	case "install":
		autoStart := true
		for _, a := range args[1:] {
			if a == "--no-autostart" || a == "--no-auto-start" {
				autoStart = false
			}
		}
		unit, err := service.Install(dataDir, autoStart)
		if err != nil {
			return err
		}
		fmt.Printf("Installed and started apx background service (%s).\n", unit)
		if autoStart {
			fmt.Println("Auto-start on login: enabled (toggle anytime with `apx service autostart off`).")
		} else {
			fmt.Println("Auto-start on login: disabled (enable anytime with `apx service autostart on`).")
		}
		fmt.Println("Run `apx` anytime to open the TUI attached to the running service.")
		return nil
	case "autostart":
		if len(args) < 2 {
			st := service.Status()
			fmt.Printf("Auto-start: %v\nUsage: apx service autostart <on|off>\n", st.Enabled)
			return nil
		}
		switch strings.ToLower(args[1]) {
		case "on", "enable", "true", "1":
			if err := service.SetAutoStart(dataDir, true); err != nil {
				return err
			}
			fmt.Println("Enabled auto-start on login for apx service.")
			return nil
		case "off", "disable", "false", "0":
			if err := service.SetAutoStart(dataDir, false); err != nil {
				return err
			}
			fmt.Println("Disabled auto-start on login for apx service.")
			return nil
		default:
			return fmt.Errorf("usage: apx service autostart <on|off>")
		}
	case "enable":
		if err := service.SetAutoStart(dataDir, true); err != nil {
			return err
		}
		fmt.Println("Enabled auto-start on login for apx service.")
		return nil
	case "disable":
		if err := service.SetAutoStart(dataDir, false); err != nil {
			return err
		}
		fmt.Println("Disabled auto-start on login for apx service.")
		return nil
	case "uninstall", "remove":
		if err := service.Uninstall(); err != nil {
			return err
		}
		fmt.Println("Stopped and uninstalled apx background service.")
		return nil
	case "start":
		if err := service.Start(); err != nil {
			return err
		}
		fmt.Println("Started apx service.")
		return nil
	case "stop":
		if err := service.Stop(); err != nil {
			return err
		}
		fmt.Println("Stopped apx service.")
		return nil
	case "restart":
		if err := service.Restart(); err != nil {
			return err
		}
		fmt.Println("Restarted apx service.")
		return nil
	case "status":
		st := service.Status()
		fmt.Printf("Manager:    %s\n", st.Manager)
		fmt.Printf("Installed:  %v\n", st.Installed)
		fmt.Printf("Auto-start: %v\n", st.Enabled)
		fmt.Printf("Running:    %v (%s)\n", st.Running, st.Detail)
		if st.UnitPath != "" {
			fmt.Printf("Unit:       %s\n", st.UnitPath)
		}
		return nil
	case "logs":
		out, err := service.Logs(dataDir, 50)
		if err != nil {
			return err
		}
		fmt.Println(out)
		return nil
	default:
		return fmt.Errorf("usage: apx service <install [--no-autostart]|uninstall|autostart <on|off>|start|stop|restart|status|logs>")
	}
}

func cmdACPBridge(_ []string) error {
	dataDir, cfg, sec, reg, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	srv := gateway.New(dataDir, cfg, sec, reg, logs)
	defer srv.ACP().Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv.FollowQuota(ctx)
	bridge := acpbridge.New(srv)
	return bridge.Serve(ctx, os.Stdin, os.Stdout)
}

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	kind := fs.String("kind", "", "account kind: api_key, acp, or cli (direct CLI proxy)")
	backendID := fs.String("backend", "", "provider preset, ACP tool, or direct CLI (claude, codex, agy, opencode, gemini, grok, cursor)")
	name := fs.String("name", "default", "account name")
	apiKey := fs.String("key", "", "API key (for api_key accounts)")
	baseURL := fs.String("base-url", "", "base URL override (for api_key accounts)")
	cwd := fs.String("cwd", "", "default working directory (for acp/cli accounts)")
	perm := fs.String("permission", config.PermAutoApprove, "ACP/CLI permission mode: auto-approve, ask, deny")
	hostProfile := fs.Bool("host-profile", false, "reuse existing host CLI login (~/.claude, ~/.codex, ~/.gemini) instead of isolated profile")
	customCmd := fs.String("command", "", "custom ACP/CLI command (space-separated)")
	modelsCSV := fs.String("models", "", "comma-separated model IDs")
	expose := fs.String("expose", config.ExposePool, "model exposure mode: pool, prefix, or both")
	prefix := fs.String("prefix", "", "model prefix when expose=prefix|both (e.g. a-, b-)")
	noTest := fs.Bool("no-test", false, "skip live connection test")
	if err := fs.Parse(args); err != nil {
		return err
	}

	dataDir, cfg, sec, reg, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	reader := bufio.NewReader(os.Stdin)
	prompt := func(label, def string) string {
		if def != "" {
			fmt.Printf("%s [%s]: ", label, def)
		} else {
			fmt.Printf("%s: ", label)
		}
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return def
		}
		return line
	}

	if *kind == "" {
		*kind = prompt("Account kind (api_key / acp / cli)", config.KindAPIKey)
	}
	if *kind != config.KindAPIKey && *kind != config.KindACP && *kind != config.KindCLI {
		return fmt.Errorf("invalid kind %q (expected api_key, acp, or cli)", *kind)
	}

	if *backendID == "" {
		if *kind == config.KindAPIKey {
			*backendID = prompt("Provider preset (deepseek, openrouter, openai, anthropic, google, groq, ollama, custom)", "deepseek")
		} else if *kind == config.KindCLI {
			*backendID = prompt("Direct CLI (claude, codex, agy, opencode, gemini, grok, cursor, custom-cli)", "claude")
		} else {
			*backendID = prompt("ACP tool (agy, codex, claude, cursor, grok, opencode, muse-code, pi, cline, gemini...)", "agy")
		}
	}

	if err := config.ValidateName(*name); err != nil {
		return err
	}
	id := config.SanitizeSlug(*backendID + "-" + *name)
	if cfg.FindAccount(id) != nil {
		return fmt.Errorf("account %q already exists", id)
	}

	var models []string
	for _, m := range strings.Split(*modelsCSV, ",") {
		if m = strings.TrimSpace(m); m != "" {
			models = append(models, m)
		}
	}

	exposeMode := *expose
	if *prefix != "" && exposeMode == config.ExposePool {
		exposeMode = config.ExposePrefix
	}

	var acct config.AccountConfig
	if *kind == config.KindAPIKey {
		preset, ok := reg.GetPreset(*backendID)
		if !ok {
			preset, _ = reg.GetPreset("custom")
		}
		url := *baseURL
		if url == "" {
			url = preset.BaseURL
		}
		if url == "" {
			url = prompt("Base URL", "https://api.openai.com/v1")
		}
		key := *apiKey
		if key == "" && !preset.KeyOptional {
			key = prompt("API key", "")
		}
		if key != "" {
			if err := sec.Set(id, key); err != nil {
				return fmt.Errorf("store secret: %w", err)
			}
		}
		if len(models) == 0 {
			models = append([]string(nil), preset.DefaultModels...)
		}
		acct = config.AccountConfig{
			ID:             id,
			Name:           *name,
			Kind:           config.KindAPIKey,
			Backend:        *backendID,
			Enabled:        true,
			BaseURL:        url,
			AuthStyle:      preset.AuthStyle,
			ExposeMode:     exposeMode,
			ModelPrefix:    *prefix,
			Models:         models,
			MaxConcurrency: 8,
		}
	} else if *kind == config.KindCLI {
		desc, ok := reg.GetCLI(*backendID)
		if !ok {
			return fmt.Errorf("unknown direct CLI backend %q (available: agy, claude, codex, opencode, gemini, grok, cursor, custom-cli)", *backendID)
		}
		if !*hostProfile {
			if _, err := config.EnsureProfileDir(dataDir, desc.ID, *name); err != nil {
				return err
			}
		}
		if len(models) == 0 {
			models = append([]string(nil), desc.DefaultModels...)
		}
		var cmdSlice []string
		if *customCmd != "" {
			cmdSlice = strings.Fields(*customCmd)
		}
		acct = config.AccountConfig{
			ID:             id,
			Name:           *name,
			Kind:           config.KindCLI,
			Backend:        desc.ID,
			Enabled:        true,
			DefaultCwd:     *cwd,
			PermissionMode: *perm,
			UseHostProfile: *hostProfile,
			CustomCommand:  cmdSlice,
			ExposeMode:     exposeMode,
			ModelPrefix:    *prefix,
			Models:         models,
			MaxConcurrency: 1,
		}
	} else {
		desc, ok := reg.GetACP(*backendID)
		if !ok {
			return fmt.Errorf("unknown ACP backend %q (run `apx install` to list all 45+ tools)", *backendID)
		}
		if !*hostProfile {
			if _, err := config.EnsureProfileDir(dataDir, desc.ID, *name); err != nil {
				return err
			}
		}
		if len(models) == 0 {
			models = append([]string(nil), desc.DefaultModels...)
		}
		var cmdSlice []string
		if *customCmd != "" {
			cmdSlice = strings.Fields(*customCmd)
		}
		acct = config.AccountConfig{
			ID:             id,
			Name:           *name,
			Kind:           config.KindACP,
			Backend:        desc.ID,
			Enabled:        true,
			DefaultCwd:     *cwd,
			PermissionMode: *perm,
			UseHostProfile: *hostProfile,
			CustomCommand:  cmdSlice,
			ExposeMode:     exposeMode,
			ModelPrefix:    *prefix,
			Models:         models,
			MaxConcurrency: 1,
		}
	}

	srv := gateway.New(dataDir, cfg, sec, reg, logs)
	if !*noTest && acct.Kind == config.KindAPIKey {
		fmt.Printf("Testing %s/%s...\n", acct.Backend, acct.Name)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		discovered, err := srv.TestAccount(ctx, acct)
		cancel()
		if err != nil {
			return fmt.Errorf("connection test failed: %w (re-run with --no-test to add anyway)", err)
		}
		if len(discovered) > 0 {
			acct.MergeDiscoveredModels(discovered)
		}
		fmt.Printf("Connection OK (%d enabled / %d total models).\n", len(acct.Models), len(acct.AvailableModels))
	}

	cfg.Accounts = append(cfg.Accounts, acct)
	cfg.UI.OnboardingComplete = true
	if err := config.Save(dataDir, cfg); err != nil {
		return err
	}
	reloaded := ""
	if gateway.NotifyRunningDaemon(cfg) {
		reloaded = " (hot-reloaded running service)"
	}
	fmt.Printf("Added account %s (%s/%s, kind=%s, expose=%s)%s.\n", acct.ID, acct.Backend, acct.Name, acct.Kind, acct.ExposeMode, reloaded)
	return nil
}

func cmdList(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, cfg, _, _, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	rtr := router.New(cfg)
	models := rtr.ListModels(false)

	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"accounts": cfg.Accounts,
			"models":   models,
		})
	}

	fmt.Printf("Accounts (%d):\n", len(cfg.Accounts))
	if len(cfg.Accounts) == 0 {
		fmt.Println("  (none)")
	}
	for _, a := range cfg.Accounts {
		state := "enabled"
		if !a.Enabled {
			state = "disabled"
		}
		fmt.Printf("  %-20s %-8s %-8s expose=%-8s models=%d\n", a.Backend+"/"+a.Name, a.Kind, state, a.ExposeMode, len(a.Models))
	}

	fmt.Printf("\nModels (%d):\n", len(models))
	if len(models) == 0 {
		fmt.Println("  (none)")
	}
	for _, m := range models {
		fmt.Printf("  %-36s (%s)\n", m.ID, m.Kind)
	}
	return nil
}

func cmdModels(args []string) error {
	dataDir, cfg, sec, reg, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}

	switch sub {
	case "list", "ls":
		if len(cfg.Accounts) == 0 {
			fmt.Println("No accounts configured.")
			return nil
		}
		for _, a := range cfg.Accounts {
			disabled := make(map[string]bool, len(a.DisabledModels))
			for _, m := range a.DisabledModels {
				disabled[m] = true
			}
			catalog := a.AvailableModels
			if len(catalog) == 0 {
				catalog = a.Models
			}
			fmt.Printf("%s/%s (%d enabled / %d total):\n", a.Backend, a.Name, len(a.Models), len(catalog))
			for _, m := range catalog {
				st := "enabled "
				if disabled[m] {
					st = "disabled"
				}
				fmt.Printf("  [%s] %s\n", st, m)
			}
		}
		return nil

	case "import-local":
		return cmdModelsImportLocal(dataDir, cfg)

	case "fetch", "refresh":
		srv := gateway.New(dataDir, cfg, sec, reg, logs)
		defer srv.ACP().Close()
		filter := ""
		if len(args) > 1 {
			filter = args[1]
		}
		updated := 0
		for i := range cfg.Accounts {
			a := &cfg.Accounts[i]
			if filter != "" && a.ID != filter && a.Name != filter && a.Backend+"/"+a.Name != filter {
				continue
			}
			fmt.Printf("Fetching models for %s/%s...\n", a.Backend, a.Name)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			discovered, err := srv.TestAccount(ctx, *a)
			cancel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "  failed: %v\n", err)
				continue
			}
			a.MergeDiscoveredModels(discovered)
			updated++
			fmt.Printf("  loaded %d models (%d enabled / %d total)\n", len(discovered), len(a.Models), len(a.AvailableModels))
		}
		if updated > 0 {
			if err := config.Save(dataDir, cfg); err != nil {
				return err
			}
			if gateway.NotifyRunningDaemon(cfg) {
				fmt.Println("Hot-reloaded running apx service.")
			}
		}
		return nil

	case "enable", "disable":
		if len(args) < 3 {
			return fmt.Errorf("usage: apx models %s <account> <model|all>", sub)
		}
		acct := cfg.FindAccount(args[1])
		if acct == nil {
			return fmt.Errorf("account %q not found", args[1])
		}
		enable := sub == "enable"
		if args[2] == "all" {
			acct.SetAllModelsEnabled(enable)
		} else {
			for _, m := range args[2:] {
				acct.SetModelEnabled(m, enable)
			}
		}
		if err := config.Save(dataDir, cfg); err != nil {
			return err
		}
		reloaded := ""
		if gateway.NotifyRunningDaemon(cfg) {
			reloaded = " (hot-reloaded running service)"
		}
		fmt.Printf("Updated %s/%s: %d enabled / %d total models%s.\n", acct.Backend, acct.Name, len(acct.Models), len(acct.AvailableModels), reloaded)
		return nil

	default:
		return fmt.Errorf("usage: apx models [list | fetch [account] | enable <account> <model|all> | disable <account> <model|all>]")
	}
}

func cmdUsage(args []string) error {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "output JSON")
	refresh := fs.Bool("refresh", false, "force live subscription quota refresh")
	period := fs.String("period", "all", "time period: 1h, 24h, 7d, 30d, all")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dataDir, cfg, sec, reg, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	srv := gateway.New(dataDir, cfg, sec, reg, logs)
	attached, _ := srv.AttachIfRunning()
	if !attached {
		defer srv.ACP().Close()
	}

	var qrep quota.Report
	if attached && !*refresh {
		qrep = srv.LiveQuota()
		if qrep.UpdatedAt.IsZero() {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			if r, err := srv.RefreshQuotaNow(ctx, ""); err == nil {
				qrep = r
			}
			cancel()
		}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if r, err := srv.RefreshQuotaNow(ctx, ""); err == nil {
			qrep = r
		} else {
			qrep = srv.LiveQuota()
		}
		cancel()
	}

	sum := srv.LiveUsagePeriod(*period)

	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"usage": sum,
			"quota": qrep,
		})
	}

	hubURL := strings.TrimSuffix(cfg.BaseURL(), "/v1") + "/usage"
	fmtCost := func(c float64) string {
		if c <= 0 {
			return "$0.00"
		}
		if c < 0.01 {
			return fmt.Sprintf("~$%.4f", c)
		}
		return fmt.Sprintf("~$%.2f", c)
	}
	fmt.Printf("AgentPassage (apx) Usage Report [period=%s] — %s\n", sum.Period, hubURL)
	fmt.Printf("Requests: %d total (24h: %d, 7d: %d) | Success: %.1f%% (%d errors)\n",
		sum.TotalRequests, sum.Requests24h, sum.Requests7d, sum.SuccessRate, sum.ErrorRequests)
	fmt.Printf("Tokens:   %d total (in: %d, out: %d, 24h: %d) | Est. Cost: %s (ref rates) | Avg latency: %dms\n\n",
		sum.TotalTokens, sum.PromptTokens, sum.CompletionTokens, sum.Tokens24h, fmtCost(sum.EstimatedCostUSD), sum.AvgLatencyMs)

	fmt.Println("Subscription Limits (Hourly / Weekly):")
	if len(qrep.Accounts) == 0 {
		fmt.Println("  (no accounts configured)")
	} else {
		now := time.Now()
		fmt.Printf("  %-18s %-8s %-28s %-26s %s\n", "ACCOUNT", "PLAN", "PRIMARY / HOURLY", "WEEKLY / SECONDARY", "APX 5h/7d")
		fmtWin := func(w *quota.Window) string {
			if w == nil {
				return "—"
			}
			s := fmt.Sprintf("%s: %.0f%% left", w.Label, w.LeftPercent)
			if r := quota.FormatResetIn(w.ResetsAt, now); r != "" {
				s += " (resets " + r + ")"
			}
			return s
		}
		for _, q := range qrep.Accounts {
			plan := q.PlanType
			if plan == "" {
				plan = q.Source
			}
			prim := fmtWin(q.Primary)
			if q.Primary == nil && q.Credits != "" {
				prim = q.Credits
			}
			localCol := fmt.Sprintf("%d / %d reqs", q.Local5h, q.Local7d)
			if q.Tokens5h > 0 || q.Tokens7d > 0 {
				localCol = fmt.Sprintf("%d/%d reqs (%s/%s tok)", q.Local5h, q.Local7d, quota.FormatCompactTokens(q.Tokens5h), quota.FormatCompactTokens(q.Tokens7d))
			}
			fmt.Printf("  %-18s %-8s %-28s %-26s %s\n",
				q.AccountName, plan, prim, fmtWin(q.Secondary), localCol)
		}
	}

	fmt.Println("\nBy Account:")
	if len(sum.ByAccount) == 0 {
		fmt.Println("  (no traffic recorded yet)")
	}
	for _, a := range sum.ByAccount {
		fmt.Printf("  %-22s req=%-5d err=%-4d tokens=%-8d cost=%-9s avg=%dms\n", a.Account, a.Requests, a.Errors, a.TotalTokens, fmtCost(a.EstimatedCostUSD), a.AvgLatencyMs)
	}
	fmt.Println("\nBy Model:")
	if len(sum.ByModel) == 0 {
		fmt.Println("  (no traffic recorded yet)")
	}
	for _, m := range sum.ByModel {
		fmt.Printf("  %-28s req=%-5d err=%-4d tokens=%-8d cost=%-9s avg=%dms\n", m.Model, m.Requests, m.Errors, m.TotalTokens, fmtCost(m.EstimatedCostUSD), m.AvgLatencyMs)
	}
	if len(sum.ByKey) > 0 {
		fmt.Println("\nBy API Key:")
		for _, k := range sum.ByKey {
			fmt.Printf("  %-22s req=%-5d err=%-4d tokens=%-8d cost=%-9s avg=%dms\n", k.Key, k.Requests, k.Errors, k.TotalTokens, fmtCost(k.EstimatedCostUSD), k.AvgLatencyMs)
		}
	}
	if len(sum.ByProject) > 0 {
		fmt.Println("\nBy Project:")
		for _, p := range sum.ByProject {
			fmt.Printf("  %-22s req=%-5d err=%-4d tokens=%-8d cost=%-9s avg=%dms\n", p.Project, p.Requests, p.Errors, p.TotalTokens, fmtCost(p.EstimatedCostUSD), p.AvgLatencyMs)
		}
	}
	return nil
}

func cmdHealth(args []string) error {
	asJSON := false
	acctID := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			asJSON = true
		case "--account", "-a":
			if i+1 < len(args) {
				i++
				acctID = args[i]
			}
		default:
			if !strings.HasPrefix(args[i], "-") && acctID == "" {
				acctID = args[i]
			}
		}
	}

	dataDir, cfg, sec, reg, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	if acctID != "" {
		if found := cfg.FindAccount(acctID); found != nil {
			acctID = found.ID
		} else {
			for _, a := range cfg.Accounts {
				if a.Name == acctID || a.Backend+"/"+a.Name == acctID {
					acctID = a.ID
					break
				}
			}
		}
	}

	srv := gateway.New(dataDir, cfg, sec, reg, logs)
	attached, _ := srv.AttachIfRunning()
	if !attached {
		defer srv.ACP().Close()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	rep, err := srv.CheckHealthNow(ctx, acctID)
	if err != nil {
		return err
	}

	if asJSON {
		if err := json.NewEncoder(os.Stdout).Encode(rep); err != nil {
			return err
		}
		if rep.UnhealthyCount > 0 {
			return fmt.Errorf("%d account(s) degraded or unhealthy", rep.UnhealthyCount)
		}
		return nil
	}

	mode := "local probe"
	if attached {
		mode = "live service (" + srv.Addr() + ")"
	}
	fmt.Printf("apx Health Check — overall: %s (%d/%d healthy) [%s]\n", rep.Overall, rep.HealthyCount, rep.TotalCount, mode)
	if len(rep.Accounts) == 0 {
		fmt.Println("  (no accounts configured)")
		return nil
	}
	fmt.Printf("  %-22s %-8s %-10s %-8s %-7s %s\n", "ACCOUNT", "KIND", "STATUS", "LATENCY", "MODELS", "ERROR")
	for _, r := range rep.Accounts {
		if acctID != "" && r.AccountID != acctID {
			continue
		}
		lat := "—"
		if !r.CheckedAt.IsZero() {
			lat = fmt.Sprintf("%dms", r.LatencyMs)
		}
		errMsg := r.Error
		if errMsg == "" && r.Status == health.StatusUnknown {
			errMsg = "(disabled / not checked)"
		}
		fmt.Printf("  %-22s %-8s %-10s %-8s %-7d %s\n", r.AccountName, r.Kind, r.Status, lat, r.ModelsFound, errMsg)
	}
	if rep.UnhealthyCount > 0 {
		return fmt.Errorf("%d account(s) degraded or unhealthy", rep.UnhealthyCount)
	}
	return nil
}

func cmdDoctor(_ []string) error {
	dataDir, cfg, sec, reg, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	checks := doctor.Run(dataDir, cfg, sec, reg)
	hasFail := false
	for _, c := range checks {
		if c.Status == doctor.StatusFail {
			hasFail = true
		}
		fmt.Printf("[%-4s] %-24s %s\n", c.Status, c.Name, c.Detail)
	}
	if hasFail {
		return fmt.Errorf("one or more doctor checks failed")
	}
	return nil
}

func cmdInstall(args []string) error {
	dataDir, _, _, reg, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	if len(args) == 0 {
		fmt.Println("ACP tools (45+ from official ACP Registry):")
		for _, d := range reg.DetectInstalled() {
			status := "not installed"
			if d.Found {
				status = "installed (" + d.BinaryPath + ")"
			}
			method := "manual"
			if d.Descriptor.BinaryArchive != "" {
				method = "binary"
			} else if d.Descriptor.InstallPackage != "" {
				method = "npm:" + d.Descriptor.InstallPackage
			} else if d.Descriptor.UVXPackage != "" {
				method = "uvx:" + d.Descriptor.UVXPackage
			}
			fmt.Printf("  %-20s %-22s %-34s %s\n", d.Descriptor.ID, d.Descriptor.Name, method, status)
		}
		fmt.Println("\nUsage: apx install <id...>   (e.g. apx install codex claude cursor opencode cline)")
		return nil
	}

	var targets []descriptors.ACPDescriptor
	if args[0] == "all" {
		core := map[string]bool{"codex": true, "claude": true, "gemini": true, "opencode": true, "cline": true, "cursor": true, "grok": true, "pi": true, "muse-code": true}
		for _, d := range reg.DetectInstalled() {
			if core[d.Descriptor.ID] && !d.Found && d.Descriptor.CanInstall() {
				targets = append(targets, d.Descriptor)
			}
		}
		if len(targets) == 0 {
			fmt.Println("All primary installable ACP tools are already installed.")
			return nil
		}
	} else {
		for _, id := range args {
			d, ok := reg.GetACP(id)
			if !ok {
				return fmt.Errorf("unknown ACP tool %q (run `apx install` to see all available IDs)", id)
			}
			targets = append(targets, d)
		}
	}

	for _, d := range targets {
		argv, err := d.ResolveInstallCommand()
		if err != nil {
			return fmt.Errorf("%s: %w", d.Name, err)
		}
		if len(argv) == 0 && d.BinaryArchive != "" {
			fmt.Printf("Downloading %s binary...\n", d.Name)
			binPath, err := descriptors.InstallBinary(context.Background(), dataDir, d, func(msg string) {
				fmt.Println(" ", msg)
			})
			if err != nil {
				return fmt.Errorf("install %s failed: %w", d.Name, err)
			}
			fmt.Printf("Installed %s -> %s\n", d.Name, binPath)
			continue
		}
		fmt.Printf("Installing %s via: %s\n", d.Name, strings.Join(argv, " "))
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("install %s failed: %w", d.Name, err)
		}
		fmt.Printf("Installed %s successfully.\n", d.Name)
	}
	return nil
}

func confirmAction(prompt string) bool {
	fmt.Printf("%s [y/N]: ", prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && len(strings.TrimSpace(line)) == 0 {
		return false
	}
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes"
}

func cmdConnect(args []string) error {
	dryRun := false
	assumeYes := false
	var rest []string
	for _, a := range args {
		switch a {
		case "--dry-run", "-dry-run":
			dryRun = true
		case "-y", "--yes":
			assumeYes = true
		default:
			rest = append(rest, a)
		}
	}
	_, cfg, _, _, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	if len(rest) == 0 {
		fmt.Println("Connect IDEs, editors & coding CLIs to apx:")
		for _, t := range connect.ListTargets() {
			state := "not found"
			if t.Connected {
				state = "connected"
			} else if t.Detected {
				state = "detected"
			}
			fmt.Printf("  %-16s %-18s %-11s %s\n", t.ID, t.Name, state, t.Description)
		}
		fmt.Println("\nUsage:")
		fmt.Println("  apx connect <client...> [--yes] [--dry-run]  (asks before modifying files)")
		fmt.Println("  apx disconnect <client...|all> [--yes]       (cleanly remove apx from connected IDEs & CLIs)")
		return nil
	}

	rtr := router.New(cfg)
	models := rtr.ListModels(false)

	for _, id := range rest {
		t, ok := connect.FindTarget(id)
		if !ok {
			return fmt.Errorf("unknown client %q (run `apx connect` to list supported clients)", id)
		}
		// Always preview first before touching any external IDE/CLI config file.
		preview, err := connect.Apply(id, cfg, models, false)
		if err != nil {
			return err
		}
		if !t.AutoApply || dryRun {
			if preview.Message != "" {
				fmt.Println(preview.Message)
			}
			fmt.Println("\n" + preview.Snippet)
			continue
		}

		fmt.Printf("Target: %s (%s)\n", t.Name, t.ID)
		fmt.Printf("File:   %s\n", t.ConfigPath)
		fmt.Printf("Backup: %s.apx.bak\n\n", t.ConfigPath)
		fmt.Println(preview.Snippet)

		if !assumeYes && !confirmAction("Write apx config to "+t.ConfigPath+"?") {
			fmt.Println("Cancelled — no files modified.")
			continue
		}

		res, err := connect.Apply(id, cfg, models, true)
		if err != nil {
			return err
		}
		if res.Message != "" {
			fmt.Println(res.Message)
		}
		if res.Backup != "" {
			fmt.Println("Backup saved:", res.Backup)
		}
	}
	return nil
}

func cmdDisconnect(args []string) error {
	assumeYes := false
	var rest []string
	for _, a := range args {
		if a == "-y" || a == "--yes" {
			assumeYes = true
			continue
		}
		rest = append(rest, a)
	}

	if len(rest) == 0 {
		connected := 0
		fmt.Println("Currently connected IDEs & CLIs:")
		for _, t := range connect.ListTargets() {
			if t.Connected {
				connected++
				fmt.Printf("  %-16s %-18s %s\n", t.ID, t.Name, t.ConfigPath)
			}
		}
		if connected == 0 {
			fmt.Println("  (none)")
		}
		fmt.Println("\nUsage: apx disconnect <client...|all> [--yes]")
		return nil
	}

	if len(rest) == 1 && strings.EqualFold(rest[0], "all") {
		var toRemove []connect.Target
		for _, t := range connect.ListTargets() {
			if t.AutoApply && t.ConfigPath != "" && t.Connected {
				toRemove = append(toRemove, t)
			}
		}
		if len(toRemove) == 0 {
			fmt.Println("No connected IDEs or CLIs found.")
			return nil
		}
		fmt.Println("Will remove apx from:")
		for _, t := range toRemove {
			fmt.Printf("  - %-18s %s\n", t.Name, t.ConfigPath)
		}
		if !assumeYes && !confirmAction(fmt.Sprintf("Disconnect apx from all %d client(s)?", len(toRemove))) {
			fmt.Println("Cancelled — no files modified.")
			return nil
		}
		results, errs := connect.DisconnectAll()
		for _, r := range results {
			fmt.Println(r.Message)
		}
		if len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "  error: %v\n", e)
			}
			return fmt.Errorf("%d disconnect error(s)", len(errs))
		}
		return nil
	}

	for _, id := range rest {
		t, ok := connect.FindTarget(id)
		if !ok {
			return fmt.Errorf("unknown client %q (run `apx connect` to list supported clients)", id)
		}
		if !t.AutoApply || t.ConfigPath == "" {
			fmt.Printf("%s is configured manually in its GUI — remove the apx Base URL there.\n", t.Name)
			continue
		}
		fmt.Printf("Will remove apx from %s (%s)\n", t.Name, t.ConfigPath)
		if !assumeYes && !confirmAction("Proceed?") {
			fmt.Println("Cancelled — no files modified.")
			continue
		}
		res, err := connect.Disconnect(id)
		if err != nil {
			return err
		}
		fmt.Println(res.Message)
	}
	return nil
}

func cmdUninstall(args []string) error {
	assumeYes := false
	purge := false
	for _, a := range args {
		switch a {
		case "-y", "--yes":
			assumeYes = true
		case "--purge":
			purge = true
		}
	}

	dataDir, _, _, _, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	var connected []connect.Target
	for _, t := range connect.ListTargets() {
		if t.AutoApply && t.ConfigPath != "" && t.Connected {
			connected = append(connected, t)
		}
	}
	st := service.Status()

	fmt.Println("apx uninstall plan:")
	if len(connected) == 0 {
		fmt.Println("  - Connections: none connected")
	} else {
		fmt.Printf("  - Disconnect %d IDE/CLI(s):\n", len(connected))
		for _, t := range connected {
			fmt.Printf("      * %-18s %s\n", t.Name, t.ConfigPath)
		}
	}
	if st.Installed || st.Running {
		fmt.Printf("  - Background service: stop & uninstall (%s)\n", st.Manager)
	} else {
		fmt.Println("  - Background service: not installed")
	}
	if purge && dataDir != "" {
		fmt.Printf("  - Purge data directory: DELETE %s\n", dataDir)
	} else if dataDir != "" {
		fmt.Printf("  - Config & profiles: kept at %s (pass --purge to delete)\n", dataDir)
	}
	fmt.Println()

	if !assumeYes && !confirmAction("Proceed with uninstall?") {
		fmt.Println("Cancelled — nothing was changed.")
		return nil
	}

	// 1. Disconnect all registered IDEs & CLIs cleanly.
	results, errs := connect.DisconnectAll()
	if len(results) == 0 {
		fmt.Println("Connections: no connected IDEs/CLIs found.")
	} else {
		for _, r := range results {
			fmt.Println("Connections:", r.Message)
		}
	}
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "Connections warning: %v\n", e)
	}

	// 2. Stop & remove OS background service if installed.
	if st.Installed || st.Running {
		if err := service.Uninstall(); err != nil {
			fmt.Fprintf(os.Stderr, "Service uninstall warning: %v\n", err)
		} else {
			fmt.Println("Service: stopped and uninstalled background service.")
		}
	}

	// 3. Optionally purge data dir, and print binary removal hint.
	if purge && dataDir != "" {
		if err := os.RemoveAll(dataDir); err != nil {
			return fmt.Errorf("remove data dir %s: %w", dataDir, err)
		}
		fmt.Printf("Purged config & profiles directory: %s\n", dataDir)
	}
	if exe, err := os.Executable(); err == nil {
		fmt.Printf("To remove the binary itself: rm %s\n", exe)
	}
	return nil
}

func cmdKeys(args []string) error {
	dataDir, cfg, _, _, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	sub := "list"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub = args[0]
		args = args[1:]
	}

	switch sub {
	case "list", "ls":
		reveal := false
		asJSON := false
		for _, a := range args {
			if a == "--reveal" || a == "-r" {
				reveal = true
			} else if a == "--json" {
				asJSON = true
			}
		}
		if asJSON {
			type keyOut struct {
				Name     string   `json:"name"`
				Role     string   `json:"role"`
				Disabled bool     `json:"disabled"`
				Models   []string `json:"models,omitempty"`
				Key      string   `json:"key"`
			}
			out := []keyOut{{Name: "primary", Role: config.KeyRoleAdmin, Key: secrets.Mask(cfg.Server.LocalKey)}}
			if reveal {
				out[0].Key = cfg.Server.LocalKey
			}
			for _, k := range cfg.Server.APIKeys {
				val := secrets.Mask(k.Key)
				if reveal {
					val = k.Key
				}
				out = append(out, keyOut{Name: k.Name, Role: k.EffectiveRole(), Disabled: k.Disabled, Models: k.Models, Key: val})
			}
			return json.NewEncoder(os.Stdout).Encode(out)
		}

		fmtKey := func(k string) string {
			if reveal {
				return k
			}
			return secrets.Mask(k)
		}
		fmt.Printf("API Keys (%d total):\n", 1+len(cfg.Server.APIKeys))
		fmt.Printf("  %-18s %-8s %-10s %-26s %s\n", "NAME", "ROLE", "STATUS", "KEY", "MODELS")
		fmt.Printf("  %-18s %-8s %-10s %-26s %s\n", "primary", "admin", "active", fmtKey(cfg.Server.LocalKey), "all")
		for _, k := range cfg.Server.APIKeys {
			st := "active"
			if k.Disabled {
				st = "disabled"
			}
			mods := "all"
			if len(k.Models) > 0 {
				mods = strings.Join(k.Models, ",")
			}
			fmt.Printf("  %-18s %-8s %-10s %-26s %s\n", k.Name, k.EffectiveRole(), st, fmtKey(k.Key), mods)
		}
		return nil

	case "create", "add", "new":
		role := config.KeyRoleClient
		project := ""
		var models []string
		name := ""
		asJSON := false
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "--role":
				if i+1 < len(args) {
					i++
					role = args[i]
				}
			case "--project":
				if i+1 < len(args) {
					i++
					project = strings.TrimSpace(args[i])
				}
			case "--models":
				if i+1 < len(args) {
					i++
					for _, m := range strings.Split(args[i], ",") {
						if m = strings.TrimSpace(m); m != "" {
							models = append(models, m)
						}
					}
				}
			case "--json":
				asJSON = true
			default:
				if !strings.HasPrefix(args[i], "-") && name == "" {
					name = args[i]
				}
			}
		}
		if name == "" {
			return fmt.Errorf("usage: apx keys create <name> [--role client|admin] [--project name] [--models a,b] [--json]")
		}
		ck, err := cfg.AddAPIKey(name, role, models)
		if err != nil {
			return err
		}
		if project != "" {
			if stored := cfg.FindAPIKey(ck.ID); stored != nil {
				stored.Project = project
				ck.Project = project
			}
		}
		if err := config.Save(dataDir, cfg); err != nil {
			return err
		}
		reloaded := ""
		if gateway.NotifyRunningDaemon(cfg) {
			reloaded = " (hot-reloaded running service)"
		}
		if asJSON {
			return json.NewEncoder(os.Stdout).Encode(ck)
		}
		fmt.Printf("Created API key %q (role=%s project=%q)%s:\n", ck.Name, ck.Role, ck.Project, reloaded)
		fmt.Printf("  %s\n", ck.Key)
		if len(ck.Models) > 0 {
			fmt.Printf("  Allowed models: %s\n", strings.Join(ck.Models, ", "))
		}
		return nil

	case "enable", "disable":
		if len(args) == 0 {
			return fmt.Errorf("usage: apx keys %s <name>", sub)
		}
		ck := cfg.FindAPIKey(args[0])
		if ck == nil {
			return fmt.Errorf("API key %q not found", args[0])
		}
		ck.Disabled = sub == "disable"
		if err := config.Save(dataDir, cfg); err != nil {
			return err
		}
		gateway.NotifyRunningDaemon(cfg)
		fmt.Printf("API key %q %sd.\n", ck.Name, sub)
		return nil

	case "revoke", "delete", "rm":
		if len(args) == 0 {
			return fmt.Errorf("usage: apx keys revoke <name>")
		}
		if !cfg.DeleteAPIKey(args[0]) {
			return fmt.Errorf("API key %q not found", args[0])
		}
		if err := config.Save(dataDir, cfg); err != nil {
			return err
		}
		gateway.NotifyRunningDaemon(cfg)
		fmt.Printf("Revoked API key %q.\n", args[0])
		return nil

	default:
		return fmt.Errorf("usage: apx keys [list [--reveal|--json] | create <name> [--role client|admin] [--models a,b] | enable <name> | disable <name> | revoke <name>]")
	}
}

func cmdRemote(args []string) error {
	if len(args) > 0 && args[0] == "start" {
		return runRemoteStart(args[1:])
	}
	_, cfg, _, _, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	port := cfg.Server.Port
	fmt.Printf(`apx Remote Access Guide (listening locally on 127.0.0.1:%d)

Keep apx bound to 127.0.0.1 — let Tailscale or Cloudflare terminate HTTPS safely.

1. Create a dedicated client-role key per remote device (blocks /admin/* & /usage):
   apx keys create laptop --role client

2. Option A — Tailscale Serve (Private Tailnet HTTPS — Recommended):
   tailscale serve --bg --https=443 http://127.0.0.1:%d
   # Check your HTTPS URL:
   tailscale serve status
   # Point remote clients to: https://<host>.<tailnet>.ts.net/v1

3. Option B — Cloudflare Tunnel (Public HTTPS, Zero Open Ports):
   # Quick test tunnel:
   cloudflared tunnel --url http://127.0.0.1:%d

   # Permanent custom domain (e.g. https://ai.yourdomain.com/v1):
   cloudflared tunnel login
   cloudflared tunnel create apx
   cloudflared tunnel route dns apx ai.yourdomain.com
   # Add ingress in ~/.cloudflared/config.yml -> service: http://127.0.0.1:%d
   # Run as service: sudo cloudflared service install

Full step-by-step guide (including Cloudflare Access hardening & streaming config):
   docs/remote-access.md
`, port, port, port, port)
	return nil
}

func cmdRegistry(args []string) error {
	dataDir, _, _, _, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	if len(args) > 0 && args[0] == "sync" {
		n, err := descriptors.SyncRegistry(context.Background(), dataDir)
		if err != nil {
			return err
		}
		fmt.Printf("Synced %d agents from %s\n", n, descriptors.OfficialRegistryURL)
		return nil
	}
	fmt.Println("Usage: apx registry sync")
	return nil
}

func cmdExportEnv(args []string) error {
	fs := flag.NewFlagSet("export-env", flag.ContinueOnError)
	shell := fs.String("shell", "sh", "shell format: sh, fish, powershell")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_, cfg, _, _, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	baseURL := cfg.BaseURL()
	anthURL := strings.TrimSuffix(baseURL, "/v1")
	key := cfg.Server.LocalKey

	switch strings.ToLower(*shell) {
	case "fish":
		fmt.Printf("set -gx OPENAI_BASE_URL %q\n", baseURL)
		fmt.Printf("set -gx OPENAI_API_KEY %q\n", key)
		fmt.Printf("set -gx ANTHROPIC_BASE_URL %q\n", anthURL)
		fmt.Printf("set -gx ANTHROPIC_API_KEY %q\n", key)
	case "powershell", "pwsh":
		fmt.Printf("$env:OPENAI_BASE_URL = %q\n", baseURL)
		fmt.Printf("$env:OPENAI_API_KEY = %q\n", key)
		fmt.Printf("$env:ANTHROPIC_BASE_URL = %q\n", anthURL)
		fmt.Printf("$env:ANTHROPIC_API_KEY = %q\n", key)
	default:
		fmt.Printf("export OPENAI_BASE_URL=%q\n", baseURL)
		fmt.Printf("export OPENAI_API_KEY=%q\n", key)
		fmt.Printf("export ANTHROPIC_BASE_URL=%q\n", anthURL)
		fmt.Printf("export ANTHROPIC_API_KEY=%q\n", key)
	}
	return nil
}
