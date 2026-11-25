package acpbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/descriptors"
	"github.com/bberka/acpx/internal/gateway"
	"github.com/bberka/acpx/internal/secrets"
)

func TestACPBridgeStdioServerEndToEnd(t *testing.T) {
	upA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"from-account-A\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upA.Close()

	upB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"from-account-B\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upB.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.AccountConfig{
		{ID: "mock-a", Name: "a", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, BaseURL: upA.URL + "/v1", AuthStyle: config.AuthNone, ExposeMode: config.ExposeBoth, ModelPrefix: "a-", Models: []string{"gemini-4-neon"}, MaxConcurrency: 8},
		{ID: "mock-b", Name: "b", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, BaseURL: upB.URL + "/v1", AuthStyle: config.AuthNone, ExposeMode: config.ExposeBoth, ModelPrefix: "b-", Models: []string{"gemini-4-neon"}, MaxConcurrency: 8},
	}
	cfg.Routing = []config.RoutingRule{
		{Pattern: "*", Strategy: config.StrategyFailover, AllowCrossAccountRetryOn429: true},
	}
	sec := secrets.NewFileOnly(dir)
	reg, _ := descriptors.LoadRegistry(dir)
	gw := gateway.New(dir, cfg, sec, reg, nil)

	// Send initialize, session/new, set model to b-gemini-4-neon, and prompt over stdio
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/set_config_option","params":{"sessionId":"acpx-sess-1","configId":"model","value":"b-gemini-4-neon"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"session/prompt","params":{"sessionId":"acpx-sess-1","prompt":[{"type":"text","text":"hello"}]}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	bridge := New(gw)
	if err := bridge.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("bridge.Serve failed: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, `"a-gemini-4-neon"`) || !strings.Contains(got, `"b-gemini-4-neon"`) || !strings.Contains(got, `"gemini-4-neon"`) {
		t.Fatalf("expected both prefixed and pooled models in session/new, got:\n%s", got)
	}
	if !strings.Contains(got, "from-account-B") {
		t.Fatalf("expected b-gemini-4-neon routed to account B, got:\n%s", got)
	}

	// Verify every output line is valid JSON-RPC 2.0 and session/set_config_option returns configOptions
	foundSetConfigOpts := false
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("invalid JSON-RPC line %q: %v", line, err)
		}
		if id, ok := m["id"].(float64); ok && id == 3 {
			res, _ := m["result"].(map[string]any)
			opts, okOpts := res["configOptions"].([]any)
			if !okOpts || len(opts) == 0 {
				t.Fatalf("session/set_config_option must return non-empty configOptions (ACP v1 schema), got: %s", line)
			}
			first := opts[0].(map[string]any)
			if first["currentValue"] != "b-gemini-4-neon" {
				t.Fatalf("expected updated currentValue b-gemini-4-neon, got %v", first["currentValue"])
			}
			foundSetConfigOpts = true
		}
	}
	if !foundSetConfigOpts {
		t.Fatalf("missing response for session/set_config_option (id=3)")
	}
}

func TestACPBridgeThoughtLevelConfigOption(t *testing.T) {
	var seenModel string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok-high\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer up.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Catalog.HideBackendQualified = true
	cfg.Accounts = []config.AccountConfig{
		{
			ID:             "agy-bb",
			Name:           "bb",
			Kind:           config.KindAPIKey,
			Backend:        "agy",
			Enabled:        true,
			BaseURL:        up.URL + "/v1",
			AuthStyle:      config.AuthNone,
			ExposeMode:     config.ExposePool,
			Models:         []string{"gemini-4-neon", "gemini-4-neon-low", "gemini-4-neon-high"},
			MaxConcurrency: 4,
		},
	}
	sec := secrets.NewFileOnly(dir)
	reg, _ := descriptors.LoadRegistry(dir)
	gw := gateway.New(dir, cfg, sec, reg, nil)

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"session/set_config_option","params":{"sessionId":"acpx-sess-1","configId":"thought_level","value":"high"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"session/prompt","params":{"sessionId":"acpx-sess-1","prompt":[{"type":"text","text":"think hard"}]}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	bridge := New(gw)
	if err := bridge.Serve(context.Background(), strings.NewReader(input), &out); err != nil {
		t.Fatalf("bridge.Serve failed: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, `"category":"thought_level"`) || !strings.Contains(got, `"currentValue":"high"`) {
		t.Fatalf("expected thought_level configOption with currentValue=high, got:\n%s", got)
	}
	if seenModel != "gemini-4-neon-high" {
		t.Fatalf("expected upstream model resolved to gemini-4-neon-high, got %q", seenModel)
	}
}
