package acpbridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/gateway"
	"github.com/bberka/agentpassage/internal/secrets"
)

func runBridgeInteractive(t *testing.T, gw *gateway.Server, fn func(send func(string), readLine func() map[string]any)) {
	t.Helper()
	runBridge(t, New(gw), fn)
}

func runBridge(t *testing.T, bridge *Server, fn func(send func(string), readLine func() map[string]any)) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	done := make(chan error, 1)
	go func() {
		done <- bridge.Serve(context.Background(), inR, outW)
		_ = outW.Close()
	}()

	sc := bufio.NewScanner(outR)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	send := func(line string) {
		if _, err := io.WriteString(inW, line+"\n"); err != nil {
			t.Fatalf("write rpc line: %v", err)
		}
	}
	readLine := func() map[string]any {
		if !sc.Scan() {
			t.Fatalf("unexpected EOF reading bridge output (err=%v)", sc.Err())
		}
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("invalid JSON-RPC line %q: %v", sc.Text(), err)
		}
		return m
	}

	fn(send, readLine)
	_ = inW.Close()
	if err := <-done; err != nil {
		t.Fatalf("bridge.Serve failed: %v", err)
	}
}

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
	var allOut bytes.Buffer
	foundSetConfigOpts := false
	runBridgeInteractive(t, gw, func(send func(string), readLine func() map[string]any) {
		send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`)
		_ = readLine()

		send(`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp"}}`)
		newRes := readLine()
		rawNew, _ := json.Marshal(newRes)
		allOut.Write(rawNew)
		allOut.WriteByte('\n')
		resMap := newRes["result"].(map[string]any)
		sid, _ := resMap["sessionId"].(string)
		if sid == "" {
			t.Fatalf("expected non-empty sessionId in session/new, got: %s", string(rawNew))
		}

		send(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"session/set_config_option","params":{"sessionId":%q,"configId":"model","value":"b-gemini-4-neon"}}`, sid))
		setRes := readLine()
		rawSet, _ := json.Marshal(setRes)
		allOut.Write(rawSet)
		allOut.WriteByte('\n')
		res, _ := setRes["result"].(map[string]any)
		opts, okOpts := res["configOptions"].([]any)
		if !okOpts || len(opts) == 0 {
			t.Fatalf("session/set_config_option must return non-empty configOptions (ACP v1 schema), got: %s", string(rawSet))
		}
		first := opts[0].(map[string]any)
		if first["currentValue"] != "b-gemini-4-neon" {
			t.Fatalf("expected updated currentValue b-gemini-4-neon, got %v", first["currentValue"])
		}
		foundSetConfigOpts = true

		send(fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"session/prompt","params":{"sessionId":%q,"prompt":[{"type":"text","text":"hello"}]}}`, sid))
		for {
			m := readLine()
			raw, _ := json.Marshal(m)
			allOut.Write(raw)
			allOut.WriteByte('\n')
			if id, ok := m["id"].(float64); ok && id == 4 {
				break
			}
		}
	})

	got := allOut.String()
	if !strings.Contains(got, `"a-gemini-4-neon"`) || !strings.Contains(got, `"b-gemini-4-neon"`) || !strings.Contains(got, `"deepseek/gemini-4-neon"`) {
		t.Fatalf("expected both prefixed and provider-pooled models in session/new, got:\n%s", got)
	}
	if strings.Contains(got, `"modelId":"gemini-4-neon"`) {
		t.Fatalf("expected bare trimmed model hidden by default, got:\n%s", got)
	}
	if !strings.Contains(got, "from-account-B") {
		t.Fatalf("expected b-gemini-4-neon routed to account B, got:\n%s", got)
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
	cfg.ACP.ExposeModes = true
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

	var allOut bytes.Buffer
	var sid1, sid2 string
	runBridgeInteractive(t, gw, func(send func(string), readLine func() map[string]any) {
		send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`)
		_ = readLine()

		send(`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp"}}`)
		newRes := readLine()
		rawNew, _ := json.Marshal(newRes)
		if strings.Contains(string(rawNew), `"name":"Default"`) {
			t.Fatalf("expected synthetic Default thinking option hidden, got: %s", string(rawNew))
		}
		if !strings.Contains(string(rawNew), `"category":"mode"`) || !strings.Contains(string(rawNew), `"bypassPermissions"`) {
			t.Fatalf("expected mode configOption and modes in session/new, got: %s", string(rawNew))
		}
		sid1, _ = newRes["result"].(map[string]any)["sessionId"].(string)

		send(fmt.Sprintf(`{"jsonrpc":"2.0","id":6,"method":"session/set_mode","params":{"sessionId":%q,"modeId":"bypassPermissions"}}`, sid1))
		modeUpd := readLine()
		if modeUpd["method"] != "session/update" {
			t.Fatalf("expected session/update on session/set_mode, got %+v", modeUpd)
		}
		reply6 := readLine()
		if id, _ := reply6["id"].(float64); id != 6 {
			t.Fatalf("expected reply id=6 for session/set_mode, got %+v", reply6)
		}

		send(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"session/set_config_option","params":{"sessionId":%q,"configId":"thought_level","value":"high"}}`, sid1))
		setRes := readLine()
		rawSet, _ := json.Marshal(setRes)
		allOut.Write(rawSet)
		allOut.WriteByte('\n')

		send(fmt.Sprintf(`{"jsonrpc":"2.0","id":4,"method":"session/prompt","params":{"sessionId":%q,"prompt":[{"type":"text","text":"think hard"}]}}`, sid1))
		for {
			m := readLine()
			raw, _ := json.Marshal(m)
			allOut.Write(raw)
			allOut.WriteByte('\n')
			if id, ok := m["id"].(float64); ok && id == 4 {
				break
			}
		}

		// session/set_model should emit config_option_update notification before replying {}
		send(fmt.Sprintf(`{"jsonrpc":"2.0","id":5,"method":"session/set_model","params":{"sessionId":%q,"modelId":"gemini-4-neon"}}`, sid1))
		upd := readLine()
		if upd["method"] != "session/update" {
			t.Fatalf("expected session/update config_option_update on session/set_model, got %+v", upd)
		}
		reply5 := readLine()
		if id, _ := reply5["id"].(float64); id != 5 {
			t.Fatalf("expected reply id=5 after config_option_update, got %+v", reply5)
		}
	})

	// Second independent bridge instance must generate a distinct session ID.
	runBridgeInteractive(t, gw, func(send func(string), readLine func() map[string]any) {
		send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`)
		_ = readLine()
		send(`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp"}}`)
		newRes := readLine()
		sid2, _ = newRes["result"].(map[string]any)["sessionId"].(string)
	})
	if sid1 == "" || sid2 == "" || sid1 == sid2 {
		t.Fatalf("expected distinct session IDs across bridge instances, got sid1=%q sid2=%q", sid1, sid2)
	}

	got := allOut.String()
	if !strings.Contains(got, `"category":"thought_level"`) || !strings.Contains(got, `"currentValue":"high"`) {
		t.Fatalf("expected thought_level configOption with currentValue=high, got:\n%s", got)
	}
	if seenModel != "gemini-4-neon-high" {
		t.Fatalf("expected upstream model resolved to gemini-4-neon-high, got %q", seenModel)
	}
}

// With [acp] expose_modes off (the default), apx shows no Mode menu, starts
// sessions in "default" (forward permission prompts to the host), still honors
// a mode the host sets, and resets sessions saved with the old "auto" default.
func TestACPBridgeModesHiddenByDefault(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.AccountConfig{{
		ID: "a", Name: "a", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true,
		BaseURL: "http://127.0.0.1:1/v1", AuthStyle: config.AuthNone, ExposeMode: config.ExposePool,
		Models: []string{"m1"}, MaxConcurrency: 1,
	}}
	sec := secrets.NewFileOnly(dir)
	reg, _ := descriptors.LoadRegistry(dir)
	gw := gateway.New(dir, cfg, sec, reg, nil)

	// A session saved by an older apx with the old "auto" default.
	New(gw).saveSession(&bridgeSession{id: "legacy", model: "m1", mode: "auto"})

	var sid string
	bridge := New(gw)
	runBridge(t, bridge, func(send func(string), readLine func() map[string]any) {
		send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}`)
		_ = readLine()
		send(`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp"}}`)
		res := readLine()
		raw, _ := json.Marshal(res)
		if strings.Contains(string(raw), `"modes"`) || strings.Contains(string(raw), `"category":"mode"`) {
			t.Fatalf("expected no Mode menu in session/new, got: %s", raw)
		}
		sid, _ = res["result"].(map[string]any)["sessionId"].(string)
		if m := bridge.sess[sid].mode; m != "default" {
			t.Fatalf("expected new session in default mode, got %q", m)
		}

		send(fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"session/set_mode","params":{"sessionId":%q,"modeId":"plan"}}`, sid))
		_ = readLine() // config_option_update
		_ = readLine() // reply

		send(`{"jsonrpc":"2.0","id":4,"method":"session/load","params":{"sessionId":"legacy","cwd":"/tmp"}}`)
		raw4, _ := json.Marshal(readLine())
		if strings.Contains(string(raw4), `"modes"`) {
			t.Fatalf("expected no Mode menu in session/load, got: %s", raw4)
		}
	})

	if legacy := bridge.sess["legacy"]; legacy == nil || legacy.mode != "default" {
		t.Fatalf("expected legacy auto session reset to default on load, got %+v", legacy)
	}
	// Persist the host-set mode, then reload it in a fresh bridge.
	bridge.saveSession(bridge.sess[sid])
	reloaded, ok := New(gw).loadOrGetSession(sid)
	if !ok || reloaded.mode != "plan" || !reloaded.hostMode {
		t.Fatalf("expected host-set mode plan kept across reload, got %+v", reloaded)
	}
}
