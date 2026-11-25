package gateway_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/gateway"
	"github.com/bberka/agentpassage/internal/logbuf"
	"github.com/bberka/agentpassage/internal/secrets"
)

func TestGatewayOpenAIAndAnthropicEndpoints(t *testing.T) {
	// Mock upstream OpenAI-compatible server (e.g. DeepSeek)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-upstream-test" {
			http.Error(w, `{"error":{"message":"bad key"}}`, http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"id": "deepseek-chat"}},
			})
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if stream, _ := req["stream"].(bool); stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello \"}}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"world\"},\"finish_reason\":\"stop\"}]}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "up-1",
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "ok-from-deepseek"}, "finish_reason": "stop"},
			},
			"usage": map[string]int{"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8},
		})
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.LocalKey = "apx_test_local_key_1234"
	cfg.Accounts = []config.AccountConfig{
		{
			ID:             "deepseek-default",
			Name:           "default",
			Kind:           config.KindAPIKey,
			Backend:        "deepseek",
			Enabled:        true,
			BaseURL:        upstream.URL + "/v1",
			AuthStyle:      config.AuthBearer,
			Models:         []string{"deepseek-chat"},
			MaxConcurrency: 8,
		},
	}
	sec := secrets.NewFileOnly(dir)
	if err := sec.Set("deepseek-default", "sk-upstream-test"); err != nil {
		t.Fatal(err)
	}
	reg, _ := descriptors.LoadRegistry(dir)
	logs := logbuf.New(100, dir, sec.KnownSecrets)
	defer logs.Close()

	srv := gateway.New(dir, cfg, sec, reg, logs)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. /healthz works without auth
	res, err := http.Get(ts.URL + "/healthz")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("healthz failed: err=%v status=%v", err, res)
	}
	res.Body.Close()

	// 2. /v1/models rejects missing auth
	res, err = http.Get(ts.URL + "/v1/models")
	if err != nil || res.StatusCode != 401 {
		t.Fatalf("expected 401 without key, got %v %v", err, res.StatusCode)
	}
	res.Body.Close()

	// 3. /v1/models succeeds with local key
	req, _ := http.NewRequest("GET", ts.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("/v1/models failed: %v %d", err, res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "deepseek/deepseek-chat") {
		t.Fatalf("expected deepseek/deepseek-chat in /v1/models, got %s", body)
	}

	// 4. OpenAI non-streaming /v1/chat/completions
	chatPayload := `{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hi"}]}`
	req, _ = http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewBufferString(chatPayload))
	req.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("chat completions failed: %v %d", err, res.StatusCode)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "ok-from-deepseek") {
		t.Fatalf("unexpected completion body: %s", body)
	}

	// 5. OpenAI streaming /v1/chat/completions
	streamPayload := `{"model":"deepseek/deepseek-chat","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req, _ = http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewBufferString(streamPayload))
	req.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("streaming failed: %v %d", err, res.StatusCode)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "Hello ") || !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("unexpected SSE stream body: %s", body)
	}

	// 6. Anthropic-compatible /v1/messages (non-stream + stream) with x-api-key
	anthPayload := `{"model":"deepseek/deepseek-chat","max_tokens":128,"messages":[{"role":"user","content":"hi"}]}`
	req, _ = http.NewRequest("POST", ts.URL+"/v1/messages", bytes.NewBufferString(anthPayload))
	req.Header.Set("x-api-key", cfg.Server.LocalKey)
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("/v1/messages failed: %v %d", err, res.StatusCode)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), `"type":"message"`) || !strings.Contains(string(body), "ok-from-deepseek") {
		t.Fatalf("unexpected /v1/messages body: %s", body)
	}

	anthStreamPayload := `{"model":"deepseek/deepseek-chat","max_tokens":128,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req, _ = http.NewRequest("POST", ts.URL+"/v1/messages", bytes.NewBufferString(anthStreamPayload))
	req.Header.Set("x-api-key", cfg.Server.LocalKey)
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("/v1/messages stream failed: %v %d", err, res.StatusCode)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "event: content_block_delta") || !strings.Contains(string(body), "event: message_stop") {
		t.Fatalf("unexpected /v1/messages SSE body: %s", body)
	}

	// 7. Usage Report Hub HTML (/usage) and JSON (/admin/usage)
	res, err = http.Get(ts.URL + "/usage")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("GET /usage failed: %v %d", err, res.StatusCode)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "Usage Report Hub") {
		t.Fatalf("expected Usage Report Hub HTML")
	}

	req, _ = http.NewRequest("GET", ts.URL+"/admin/usage", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("GET /admin/usage failed: %v %d", err, res.StatusCode)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), `"total_requests":4`) {
		t.Fatalf("expected 4 recorded requests in /admin/usage, got %s", body)
	}

	// 8. Active Health Checker API (/admin/health and POST /admin/health/check + enriched /healthz)
	req, _ = http.NewRequest("POST", ts.URL+"/admin/health/check", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("POST /admin/health/check failed: %v %d", err, res.StatusCode)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), `"overall":"healthy"`) || !strings.Contains(string(body), `"healthy_count":1`) {
		t.Fatalf("expected healthy report from /admin/health/check, got %s", body)
	}

	req, _ = http.NewRequest("GET", ts.URL+"/admin/health", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("GET /admin/health failed: %v %d", err, res.StatusCode)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), `"account_id":"deepseek-default"`) {
		t.Fatalf("expected deepseek-default in /admin/health, got %s", body)
	}

	res, err = http.Get(ts.URL + "/healthz")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("GET /healthz after probe failed: %v %d", err, res.StatusCode)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), `"overall_health":"healthy"`) || !strings.Contains(string(body), `"accounts_healthy":1`) {
		t.Fatalf("expected enriched /healthz with overall_health=healthy, got %s", body)
	}

	// 9. Subscription Quota API (/admin/quota and POST /admin/quota/refresh)
	req, _ = http.NewRequest("GET", ts.URL+"/admin/quota", nil)
	req.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("GET /admin/quota failed: %v %d", err, res.StatusCode)
	}
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), `"account_name":"deepseek/default"`) || !strings.Contains(string(body), `"local_5h_reqs":4`) {
		t.Fatalf("expected deepseek/default with local_5h_reqs=4 in /admin/quota, got %s", body)
	}

	// 10. Multiple API Keys: client role allowed on /v1/*, blocked on /admin/*, model allowlist enforced
	clientKey, err := cfg.AddAPIKey("laptop", config.KeyRoleClient, []string{"deepseek/*"})
	if err != nil {
		t.Fatalf("AddAPIKey failed: %v", err)
	}
	restrictedKey, err := cfg.AddAPIKey("phone", config.KeyRoleClient, []string{"other-model"})
	if err != nil {
		t.Fatalf("AddAPIKey failed: %v", err)
	}
	adminKey2, err := cfg.AddAPIKey("ops", config.KeyRoleAdmin, nil)
	if err != nil {
		t.Fatalf("AddAPIKey failed: %v", err)
	}

	// Client key works on /v1/models
	req, _ = http.NewRequest("GET", ts.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+clientKey.Key)
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("client key /v1/models failed: %v %d", err, res.StatusCode)
	}
	res.Body.Close()

	// Client key blocked (403) on /admin/state
	req, _ = http.NewRequest("GET", ts.URL+"/admin/state", nil)
	req.Header.Set("Authorization", "Bearer "+clientKey.Key)
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for client key on /admin/state, got %v %d", err, res.StatusCode)
	}
	res.Body.Close()

	// Restricted key blocked (403) on disallowed model
	req, _ = http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewBufferString(chatPayload))
	req.Header.Set("Authorization", "Bearer "+restrictedKey.Key)
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for restricted key on deepseek/deepseek-chat, got %v %d", err, res.StatusCode)
	}
	res.Body.Close()

	// Secondary admin key allowed on /admin/state
	req, _ = http.NewRequest("GET", ts.URL+"/admin/state", nil)
	req.Header.Set("Authorization", "Bearer "+adminKey2.Key)
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("expected 200 for secondary admin key on /admin/state, got %v %d", err, res.StatusCode)
	}
	res.Body.Close()

	// Disabled key rejected with 401
	cfg.FindAPIKey("laptop").Disabled = true
	req, _ = http.NewRequest("GET", ts.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+clientKey.Key)
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for disabled key, got %v %d", err, res.StatusCode)
	}
	res.Body.Close()
}

func TestGatewayFailover5xxVsNoFailover429(t *testing.T) {
	primaryStatus := 503
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"primary err"}}`, primaryStatus)
	}))
	defer primary.Close()

	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "sec-1",
			"choices": []map[string]any{
				{"message": map[string]string{"role": "assistant", "content": "from-secondary"}, "finish_reason": "stop"},
			},
		})
	}))
	defer secondary.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.LocalKey = "apx_failover_key_1234"
	cfg.Accounts = []config.AccountConfig{
		{ID: "deepseek-p1", Name: "p1", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, BaseURL: primary.URL + "/v1", AuthStyle: config.AuthNone, Models: []string{"deepseek-chat"}, MaxConcurrency: 8},
		{ID: "deepseek-p2", Name: "p2", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, BaseURL: secondary.URL + "/v1", AuthStyle: config.AuthNone, Models: []string{"deepseek-chat"}, MaxConcurrency: 8},
	}
	cfg.Routing = []config.RoutingRule{
		{Pattern: "deepseek/*", Accounts: []string{"deepseek-p1", "deepseek-p2"}, Strategy: config.StrategyFailover},
	}
	sec := secrets.NewFileOnly(dir)
	reg, _ := descriptors.LoadRegistry(dir)
	srv := gateway.New(dir, cfg, sec, reg, nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 503 on primary -> fails over to secondary
	payload := `{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hi"}]}`
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewBufferString(payload))
	req.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("expected failover 200 on 503, got err=%v status=%d", err, res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(body), "from-secondary") {
		t.Fatalf("expected secondary response, got %s", body)
	}

	// 429 on primary -> does NOT fail over (returns 429)
	primaryStatus = 429
	req, _ = http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewBufferString(payload))
	req.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != 429 {
		t.Fatalf("expected 429 without failover, got err=%v status=%d", err, res.StatusCode)
	}
	res.Body.Close()

	// Loop guard: X-Apx-Client matching target backend returns 508 Loop Detected
	req, _ = http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewBufferString(payload))
	req.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
	req.Header.Set("X-Apx-Client", "deepseek")
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusLoopDetected {
		t.Fatalf("expected 508 Loop Detected when X-Apx-Client matches backend, got err=%v status=%d", err, res.StatusCode)
	}
	res.Body.Close()

	// Loop guard: X-Apx-Hop >= 2 returns 508 Loop Detected
	req, _ = http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewBufferString(payload))
	req.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
	req.Header.Set("X-Apx-Hop", "2")
	res, err = http.DefaultClient.Do(req)
	if err != nil || res.StatusCode != http.StatusLoopDetected {
		t.Fatalf("expected 508 Loop Detected on X-Apx-Hop=2, got err=%v status=%d", err, res.StatusCode)
	}
	res.Body.Close()
}

func TestAttachToRunningDaemonAndHotReload(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.Bind = "127.0.0.1"
	cfg.Server.Port = 18789
	cfg.Server.LocalKey = "apx_attach_test_key_1234"
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}

	sec := secrets.NewFileOnly(dir)
	reg, _ := descriptors.LoadRegistry(dir)
	logs := logbuf.New(50, dir, sec.KnownSecrets)
	defer logs.Close()
	logs.Info("system", "daemon started")

	daemon := gateway.New(dir, cfg, sec, reg, logs)
	if err := daemon.Start(); err != nil {
		t.Fatalf("daemon Start failed: %v", err)
	}
	defer daemon.Stop(t.Context())

	// Extract bound port and attach a second Server instance (simulating opening `apx` TUI while service runs)
	var boundPort int
	_, portStr, okCut := strings.Cut(daemon.Addr(), ":")
	if !okCut {
		t.Fatalf("unexpected addr %q", daemon.Addr())
	}
	_ = json.Unmarshal([]byte(portStr), &boundPort)

	cfg2, _, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg2.Server.Port = boundPort

	client := gateway.New(dir, cfg2, sec, reg, nil)
	ok, err := client.AttachIfRunning()
	if err != nil || !ok || !client.Attached() {
		t.Fatalf("AttachIfRunning failed: ok=%v err=%v", ok, err)
	}
	if len(client.LiveLogEntries()) == 0 {
		t.Fatalf("expected attached client to fetch remote logs from /admin/logs")
	}

	// Add an account via the attached client -> saves config.toml AND triggers POST /admin/reload on the daemon!
	cfg2.Accounts = append(cfg2.Accounts, config.AccountConfig{
		ID:      "deepseek-hot",
		Name:    "hot",
		Kind:    config.KindAPIKey,
		Backend: "deepseek",
		Enabled: true,
		BaseURL: "https://api.deepseek.com/v1",
		Models:  []string{"deepseek-chat"},
	})
	if err := client.ReloadConfig(cfg2); err != nil {
		t.Fatalf("attached ReloadConfig failed: %v", err)
	}

	// Daemon must now know about the newly added account immediately without restart!
	if daemon.Config().FindAccount("deepseek-hot") == nil {
		t.Fatalf("expected daemon to hot-reload new account via /admin/reload")
	}
}
