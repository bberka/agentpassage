package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/gateway"
)

type localServerSpec struct {
	Backend string
	Name    string
	BaseURL string
}

// cmdModelsImportLocal probes standard local model servers (Ollama, LM Studio, llama.cpp, vLLM),
// fetches their /v1/models list, and registers or updates them as local no-auth accounts in apx.
func cmdModelsImportLocal(dataDir string, cfg *config.Config) error {
	specs := []localServerSpec{
		{Backend: "ollama", Name: "local", BaseURL: "http://127.0.0.1:11434/v1"},
		{Backend: "lmstudio", Name: "local", BaseURL: "http://127.0.0.1:1234/v1"},
		{Backend: "llamacpp", Name: "local", BaseURL: "http://127.0.0.1:8080/v1"},
		{Backend: "vllm", Name: "local", BaseURL: "http://127.0.0.1:8000/v1"},
	}

	client := &http.Client{Timeout: 2 * time.Second}
	imported := 0
	totalModels := 0

	for _, sp := range specs {
		resp, err := client.Get(sp.BaseURL + "/models")
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}
		var parsed struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &parsed) != nil || len(parsed.Data) == 0 {
			continue
		}
		models := make([]string, 0, len(parsed.Data))
		for _, m := range parsed.Data {
			if id := strings.TrimSpace(m.ID); id != "" {
				models = append(models, id)
			}
		}
		if len(models) == 0 {
			continue
		}

		id := config.SanitizeSlug(sp.Backend + "-" + sp.Name)
		if existing := cfg.FindAccount(id); existing != nil {
			existing.BaseURL = sp.BaseURL
			existing.AuthStyle = config.AuthNone
			existing.MergeDiscoveredModels(models)
		} else {
			cfg.Accounts = append(cfg.Accounts, config.AccountConfig{
				ID:              id,
				Name:            sp.Name,
				Kind:            config.KindAPIKey,
				Backend:         sp.Backend,
				Enabled:         true,
				BaseURL:         sp.BaseURL,
				AuthStyle:       config.AuthNone,
				Models:          models,
				AvailableModels: append([]string(nil), models...),
				ExposeMode:      config.ExposePool,
				MaxConcurrency:  4,
			})
		}
		imported++
		totalModels += len(models)
		fmt.Printf("Imported %s (%s): %d model(s) [%s]\n", sp.Backend+"/"+sp.Name, sp.BaseURL, len(models), strings.Join(models, ", "))
	}

	if imported == 0 {
		fmt.Println("No running local model servers detected (checked Ollama :11434, LM Studio :1234, llama.cpp :8080, vLLM :8000).")
		return nil
	}
	if err := config.Save(dataDir, cfg); err != nil {
		return err
	}
	gateway.NotifyRunningDaemon(cfg)
	fmt.Printf("Saved %d local provider(s) with %d model(s).\n", imported, totalModels)
	return nil
}

// cmdBench benchmarks one or more models through the running apx gateway,
// reporting time-to-first-token (TTFT), total latency, and approximate tokens/sec.
func cmdBench(args []string) error {
	prompt := "Reply with exactly ten words about local AI gateways."
	var models []string
	for i := 0; i < len(args); i++ {
		if (args[i] == "--prompt" || args[i] == "-p") && i+1 < len(args) {
			i++
			prompt = args[i]
		} else if !strings.HasPrefix(args[i], "-") {
			models = append(models, args[i])
		}
	}
	if len(models) == 0 {
		return fmt.Errorf("usage: apx bench <model1> [model2...] [--prompt \"...\"]")
	}

	dataDir, err := config.DataDir()
	if err != nil {
		return err
	}
	cfg, _, err := config.Load(dataDir)
	if err != nil {
		return err
	}
	baseURL := fmt.Sprintf("http://%s:%d/v1/chat/completions", cfg.Server.Bind, cfg.Server.Port)
	client := &http.Client{Timeout: 60 * time.Second}

	fmt.Printf("%-32s  %8s  %8s  %8s  %s\n", "MODEL", "TTFT", "TOTAL", "TOK/S", "STATUS")
	for _, m := range models {
		payload, _ := json.Marshal(map[string]any{
			"model":    m,
			"stream":   true,
			"messages": []map[string]string{{"role": "user", "content": prompt}},
		})
		req, _ := http.NewRequest(http.MethodPost, baseURL, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)

		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("%-32s  %8s  %8s  %8s  ERR: %v\n", m, "—", "—", "—", err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			_ = resp.Body.Close()
			fmt.Printf("%-32s  %8s  %8s  %8s  HTTP %d: %s\n", m, "—", "—", "—", resp.StatusCode, strings.TrimSpace(string(b)))
			continue
		}

		var ttft time.Duration
		var chars int
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if json.Unmarshal([]byte(data), &chunk) == nil && len(chunk.Choices) > 0 {
				if txt := chunk.Choices[0].Delta.Content; txt != "" {
					if ttft == 0 {
						ttft = time.Since(start)
					}
					chars += len(txt)
				}
			}
		}
		_ = resp.Body.Close()
		total := time.Since(start)
		estToks := float64(chars) / 4.0
		tps := 0.0
		if total.Seconds() > 0 {
			tps = estToks / total.Seconds()
		}
		fmt.Printf("%-32s  %6dms  %6dms  %7.1f  OK\n", m, ttft.Milliseconds(), total.Milliseconds(), tps)
	}
	return nil
}

// cmdDecision configures the virtual auto-router decision model (`apx/auto`).
func cmdDecision(args []string) error {
	dataDir, err := config.DataDir()
	if err != nil {
		return err
	}
	cfg, _, err := config.Load(dataDir)
	if err != nil {
		return err
	}
	sub := "status"
	if len(args) > 0 {
		sub = args[0]
		args = args[1:]
	}
	switch sub {
	case "status":
		dc := cfg.Decision
		id := dc.ModelID
		if id == "" {
			id = "apx/auto"
		}
		fmt.Printf("Decision model (%s): enabled=%v\n  default:   %s\n  fast:      %s\n  coding:    %s\n  reasoning: %s\n  vision:    %s\n",
			id, dc.Enabled, dc.DefaultModel, dc.FastModel, dc.CodingModel, dc.ReasoningModel, dc.VisionModel)
		return nil
	case "enable":
		cfg.Decision.Enabled = true
		if cfg.Decision.ModelID == "" {
			cfg.Decision.ModelID = "apx/auto"
		}
	case "disable":
		cfg.Decision.Enabled = false
	case "set", "config":
		for i := 0; i < len(args); i++ {
			next := func() string {
				if i+1 < len(args) {
					i++
					return args[i]
				}
				return ""
			}
			switch args[i] {
			case "--id":
				cfg.Decision.ModelID = next()
			case "--default":
				cfg.Decision.DefaultModel = next()
			case "--fast":
				cfg.Decision.FastModel = next()
			case "--coding":
				cfg.Decision.CodingModel = next()
			case "--reasoning":
				cfg.Decision.ReasoningModel = next()
			case "--vision":
				cfg.Decision.VisionModel = next()
			}
		}
		cfg.Decision.Enabled = true
		if cfg.Decision.ModelID == "" {
			cfg.Decision.ModelID = "apx/auto"
		}
	default:
		return fmt.Errorf("usage: apx decision status|enable|disable|set [--id apx/auto] [--default m] [--fast m] [--coding m] [--reasoning m] [--vision m]")
	}
	if err := config.Save(dataDir, cfg); err != nil {
		return err
	}
	gateway.NotifyRunningDaemon(cfg)
	fmt.Printf("Saved decision model config (enabled=%v, id=%s).\n", cfg.Decision.Enabled, cfg.Decision.ModelID)
	return nil
}

// runRemoteStart auto-provisions a client-only API key (if none exists) and launches
// Tailscale Serve or Cloudflare Tunnel for safe remote access.
func runRemoteStart(args []string) error {
	provider := "tailscale"
	for _, a := range args {
		switch a {
		case "--tailscale", "tailscale":
			provider = "tailscale"
		case "--cloudflare", "--cloudflared", "cloudflare":
			provider = "cloudflare"
		}
	}
	dataDir, err := config.DataDir()
	if err != nil {
		return err
	}
	cfg, _, err := config.Load(dataDir)
	if err != nil {
		return err
	}

	// Ensure at least one scoped client-role API key exists for remote devices.
	var remoteKey string
	for _, k := range cfg.Server.APIKeys {
		if !k.Disabled && k.EffectiveRole() == config.KeyRoleClient {
			remoteKey = k.Key
			break
		}
	}
	if remoteKey == "" {
		newKey := config.ClientKey{
			ID:        "remote-client",
			Name:      "remote-client",
			Key:       config.GenerateLocalKey(),
			Role:      config.KeyRoleClient,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		cfg.Server.APIKeys = append(cfg.Server.APIKeys, newKey)
		if err := config.Save(dataDir, cfg); err != nil {
			return err
		}
		gateway.NotifyRunningDaemon(cfg)
		remoteKey = newKey.Key
		fmt.Printf("Created scoped client-only API key 'remote-client': %s\n", remoteKey)
	} else {
		fmt.Printf("Using existing client-only API key: %s\n", remoteKey)
	}

	target := fmt.Sprintf("http://127.0.0.1:%d", cfg.Server.Port)
	if provider == "cloudflare" {
		bin, err := exec.LookPath("cloudflared")
		if err != nil {
			return fmt.Errorf("cloudflared not found on PATH (install from https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/)")
		}
		fmt.Printf("Starting Cloudflare Tunnel -> %s (Ctrl+C to stop)...\n", target)
		cmd := exec.Command(bin, "tunnel", "--url", target)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}

	bin, err := exec.LookPath("tailscale")
	if err != nil {
		return fmt.Errorf("tailscale not found on PATH (install from https://tailscale.com/download)")
	}
	fmt.Printf("Configuring Tailscale Serve -> %s...\n", target)
	cmd := exec.Command(bin, "serve", "--bg", "--https=443", target)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}
	st := exec.Command(bin, "serve", "status")
	st.Stdout = os.Stdout
	st.Stderr = os.Stderr
	return st.Run()
}
