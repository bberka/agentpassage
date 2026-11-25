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
		{Pattern: "*", Strategy: config.StrategyRoundRobin},
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

	// Verify every output line is valid JSON-RPC 2.0
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("invalid JSON-RPC line %q: %v", line, err)
		}
	}
}
