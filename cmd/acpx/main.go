package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/descriptors"
	"github.com/bberka/acpx/internal/doctor"
	"github.com/bberka/acpx/internal/gateway"
	"github.com/bberka/acpx/internal/logbuf"
	"github.com/bberka/acpx/internal/router"
	"github.com/bberka/acpx/internal/secrets"
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
		case "add":
			return cmdAdd(args[1:])
		case "list", "ls":
			return cmdList(args[1:])
		case "doctor":
			return cmdDoctor(args[1:])
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
  acpx [flags]                 Start gateway and open interactive TUI
  acpx --no-tui [flags]        Start gateway without TUI
  acpx serve [flags]           Start headless gateway
  acpx add [flags]             Add an ACP or API-key account
  acpx list [--json]           List configured accounts and models
  acpx doctor                  Check CLIs, profiles, storage, and config
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
		return err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Stop(ctx)
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

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	kind := fs.String("kind", "", "account kind: api_key or acp")
	backendID := fs.String("backend", "", "provider preset (e.g. deepseek, openai) or ACP tool (e.g. agy, gemini)")
	name := fs.String("name", "default", "account name")
	apiKey := fs.String("key", "", "API key (for api_key accounts)")
	baseURL := fs.String("base-url", "", "base URL override (for api_key accounts)")
	cwd := fs.String("cwd", "", "default working directory (for acp accounts)")
	perm := fs.String("permission", config.PermDeny, "ACP permission mode: deny, auto-approve, ask")
	customCmd := fs.String("command", "", "custom ACP command (space-separated)")
	modelsCSV := fs.String("models", "", "comma-separated model IDs")
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
		*kind = prompt("Account kind (api_key / acp)", config.KindAPIKey)
	}
	if *kind != config.KindAPIKey && *kind != config.KindACP {
		return fmt.Errorf("invalid kind %q (expected api_key or acp)", *kind)
	}

	if *backendID == "" {
		if *kind == config.KindAPIKey {
			*backendID = prompt("Provider preset (deepseek, openrouter, openai, anthropic, google, groq, ollama, custom)", "deepseek")
		} else {
			*backendID = prompt("ACP tool (agy, gemini, codex, claude, custom-acp)", "agy")
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
			Models:         models,
			MaxConcurrency: 8,
		}
	} else {
		desc, ok := reg.GetACP(*backendID)
		if !ok {
			return fmt.Errorf("unknown ACP backend %q", *backendID)
		}
		if _, err := config.EnsureProfileDir(dataDir, *backendID, *name); err != nil {
			return err
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
			Backend:        *backendID,
			Enabled:        true,
			DefaultCwd:     *cwd,
			PermissionMode: *perm,
			CustomCommand:  cmdSlice,
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
			acct.Models = discovered
		}
		fmt.Printf("Connection OK (%d models).\n", len(acct.Models))
	}

	cfg.Accounts = append(cfg.Accounts, acct)
	cfg.UI.OnboardingComplete = true
	if err := config.Save(dataDir, cfg); err != nil {
		return err
	}
	fmt.Printf("Added account %s (%s/%s, kind=%s).\n", acct.ID, acct.Backend, acct.Name, acct.Kind)
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
		fmt.Printf("  %-20s %-8s %-8s models=%d\n", a.Backend+"/"+a.Name, a.Kind, state, len(a.Models))
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

func cmdDoctor(args []string) error {
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
