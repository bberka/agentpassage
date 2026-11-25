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

	"github.com/bberka/acpx/internal/acpbridge"
	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/connect"
	"github.com/bberka/acpx/internal/descriptors"
	"github.com/bberka/acpx/internal/doctor"
	"github.com/bberka/acpx/internal/gateway"
	"github.com/bberka/acpx/internal/logbuf"
	"github.com/bberka/acpx/internal/router"
	"github.com/bberka/acpx/internal/secrets"
	"github.com/bberka/acpx/internal/service"
	"github.com/bberka/acpx/internal/tui"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "acpx: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		switch args[0] {
		case "serve":
			return cmdServe(args[1:])
		case "service":
			return cmdService(args[1:])
		case "acp":
			return cmdACPBridge(args[1:])
		case "add":
			return cmdAdd(args[1:])
		case "list", "ls":
			return cmdList(args[1:])
		case "models":
			return cmdModels(args[1:])
		case "doctor":
			return cmdDoctor(args[1:])
		case "install":
			return cmdInstall(args[1:])
		case "connect":
			return cmdConnect(args[1:])
		case "registry":
			return cmdRegistry(args[1:])
		case "export-env":
			return cmdExportEnv(args[1:])
		case "version":
			fmt.Printf("acpx %s\n", gateway.Version)
			return nil
		case "help", "-h", "--help":
			printUsage()
			return nil
		default:
			return fmt.Errorf("unknown command %q (run `acpx help`)", args[0])
		}
	}

	fs := flag.NewFlagSet("acpx", flag.ContinueOnError)
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
		fmt.Printf("acpx %s\n", gateway.Version)
		return nil
	}

	if *noTUI {
		return startHeadless(*port, *bind, *allowRemote)
	}
	return startTUI(*port, *bind, *allowRemote)
}

func printUsage() {
	fmt.Println(`acpx - ACP and API-key gateway with a TUI

Usage:
  acpx [flags]                 Start gateway and open full-screen TUI
  acpx --no-tui [flags]        Start gateway without TUI
  acpx serve [flags]           Start headless HTTP gateway
  acpx service <cmd>           Manage OS background service (install|uninstall|start|stop|restart|status|logs)
  acpx acp                     Run acpx as an ACP stdio server (for T3 Code, Zed, etc.)
  acpx add [flags]             Add an ACP or API-key account
  acpx list [--json]           List configured accounts and models
  acpx models [list|fetch|enable|disable]  Load, list, enable, or disable models per account
  acpx doctor                  Check CLIs, profiles, storage, and config
  acpx install <tool|all>      Install any ACP tool from the 45+ agent registry
  acpx connect [client]        Register acpx into T3 Code, OpenCode, Codex, Claude, Pi, Grok, Cursor, Cline
  acpx registry sync           Fetch latest official ACP Registry JSON
  acpx export-env [--shell sh] Print shell exports for OpenAI/Anthropic clients
  acpx version                 Print version

Flags (root / serve):
  --port <int>                 Override listening port (default 8787)
  --bind <addr>                Override bind address (default 127.0.0.1)
  --allow-remote               Allow binding to a non-loopback address`)
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
		// If another acpx daemon or background service is already listening on this port,
		// attach the TUI to it seamlessly instead of failing with address-in-use!
		if ok, attachErr := srv.AttachIfRunning(); !ok {
			return fmt.Errorf("%w (hint: port is held by another process; try `acpx --port 8788` — attach check: %v)", err, attachErr)
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
	fmt.Printf("acpx %s listening on http://%s/v1\n", gateway.Version, srv.Addr())
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
		unit, err := service.Install(dataDir)
		if err != nil {
			return err
		}
		fmt.Printf("Installed and started acpx background service (%s).\n", unit)
		fmt.Println("Run `acpx` anytime to open the TUI attached to the running service.")
		return nil
	case "uninstall", "remove":
		if err := service.Uninstall(); err != nil {
			return err
		}
		fmt.Println("Stopped and uninstalled acpx background service.")
		return nil
	case "start":
		if err := service.Start(); err != nil {
			return err
		}
		fmt.Println("Started acpx service.")
		return nil
	case "stop":
		if err := service.Stop(); err != nil {
			return err
		}
		fmt.Println("Stopped acpx service.")
		return nil
	case "restart":
		if err := service.Restart(); err != nil {
			return err
		}
		fmt.Println("Restarted acpx service.")
		return nil
	case "status":
		st := service.Status()
		fmt.Printf("Manager:   %s\n", st.Manager)
		fmt.Printf("Installed: %v\n", st.Installed)
		fmt.Printf("Running:   %v (%s)\n", st.Running, st.Detail)
		if st.UnitPath != "" {
			fmt.Printf("Unit:      %s\n", st.UnitPath)
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
		return fmt.Errorf("usage: acpx service <install|uninstall|start|stop|restart|status|logs>")
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
	perm := fs.String("permission", config.PermDeny, "ACP/CLI permission mode: deny, auto-approve, ask")
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
			return fmt.Errorf("unknown ACP backend %q (run `acpx install` to list all 45+ tools)", *backendID)
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
	fmt.Printf("Added account %s (%s/%s, kind=%s, expose=%s).\n", acct.ID, acct.Backend, acct.Name, acct.Kind, acct.ExposeMode)
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
			return config.Save(dataDir, cfg)
		}
		return nil

	case "enable", "disable":
		if len(args) < 3 {
			return fmt.Errorf("usage: acpx models %s <account> <model|all>", sub)
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
		fmt.Printf("Updated %s/%s: %d enabled / %d total models.\n", acct.Backend, acct.Name, len(acct.Models), len(acct.AvailableModels))
		return nil

	default:
		return fmt.Errorf("usage: acpx models [list | fetch [account] | enable <account> <model|all> | disable <account> <model|all>]")
	}
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
		fmt.Println("\nUsage: acpx install <id...>   (e.g. acpx install codex claude cursor opencode cline)")
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
				return fmt.Errorf("unknown ACP tool %q (run `acpx install` to see all available IDs)", id)
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

func cmdConnect(args []string) error {
	dryRun := false
	var rest []string
	for _, a := range args {
		if a == "--dry-run" || a == "-dry-run" {
			dryRun = true
			continue
		}
		rest = append(rest, a)
	}
	_, cfg, _, _, logs, err := loadRuntime(0, "", false)
	if err != nil {
		return err
	}
	defer logs.Close()

	if len(rest) == 0 {
		fmt.Println("Connect coding CLIs & T3 Code to acpx:")
		for _, t := range connect.ListTargets() {
			det := "not detected"
			if t.Detected {
				det = "detected"
			}
			fmt.Printf("  %-12s %-24s %-14s %s\n", t.ID, t.Name, det, t.Description)
		}
		fmt.Println("\nUsage: acpx connect <t3code|opencode|codex|claude|pi|grok|cursor|cline|agy> [--dry-run]")
		return nil
	}

	rtr := router.New(cfg)
	models := rtr.ListModels(false)

	for _, id := range rest {
		t, ok := connect.FindTarget(id)
		if !ok {
			return fmt.Errorf("unknown client %q (run `acpx connect` to list supported clients)", id)
		}
		res, err := connect.Apply(id, cfg, models, t.AutoApply && !dryRun)
		if err != nil {
			return err
		}
		if res.Message != "" {
			fmt.Println(res.Message)
		}
		if res.Backup != "" {
			fmt.Println("Backup saved:", res.Backup)
		}
		fmt.Println("\n" + res.Snippet)
	}
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
	fmt.Println("Usage: acpx registry sync")
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
