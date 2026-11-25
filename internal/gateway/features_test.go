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

func TestPrivacyFilterModelFallbackAndProjectRouting(t *testing.T) {
	var primarySaw string
	primarySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		primarySaw = string(raw)
		// Simulate rate-limit 429 on primary model to exercise model fallback
		if strings.Contains(primarySaw, `"model":"primary-model"`) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded"}}`))
			return
		}
		// Echo back whatever message content we received (should contain placeholders, not secrets!)
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &body)
		reply := ""
		if len(body.Messages) > 0 {
			reply = "echo:" + body.Messages[0].Content
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "cmpl-primary",
			"choices": []map[string]any{
				{"message": map[string]any{"content": reply}, "finish_reason": "stop"},
			},
			"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15},
		})
	}))
	defer primarySrv.Close()

	var fallbackSaw string
	fallbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		fallbackSaw = string(raw)
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &body)
		reply := "fallback:"
		if len(body.Messages) > 0 {
			reply += body.Messages[0].Content
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "cmpl-fb",
			"choices": []map[string]any{
				{"message": map[string]any{"content": reply}, "finish_reason": "stop"},
			},
			"usage": map[string]any{"prompt_tokens": 12, "completion_tokens": 8, "total_tokens": 20},
		})
	}))
	defer fallbackSrv.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.LocalKey = "apx_test_admin_key_123456"
	cfg.Server.APIKeys = []config.ClientKey{
		{ID: "work-key", Name: "work-key", Key: "apx_work_key_99999999", Role: config.KeyRoleClient, Project: "work"},
	}
	cfg.Privacy = config.PrivacyConfig{
		Enabled:       true,
		RedactAPIKeys: true,
		RedactEmails:  true,
		RedactPhones:  true,
	}
	cfg.Accounts = []config.AccountConfig{
		{ID: "p1", Name: "primary", Kind: config.KindAPIKey, Backend: "prov1", Enabled: true, BaseURL: primarySrv.URL + "/v1", AuthStyle: config.AuthNone, Models: []string{"primary-model", "work-model"}, MaxConcurrency: 4},
		{ID: "p2", Name: "backup", Kind: config.KindAPIKey, Backend: "prov2", Enabled: true, BaseURL: fallbackSrv.URL + "/v1", AuthStyle: config.AuthNone, Models: []string{"backup-model", "work-model"}, MaxConcurrency: 4},
	}
	cfg.Routing = []config.RoutingRule{
		{Pattern: "prov1/primary-model", Strategy: config.StrategyFailover, AllowCrossAccountRetryOn429: true, FallbackModels: []string{"prov2/backup-model"}},
		{Pattern: "work-model", Project: "work", Accounts: []string{"p2"}, Strategy: config.StrategyFailover},
	}

	sec := secrets.NewFileOnly(dir)
	reg, _ := descriptors.LoadRegistry(dir)
	logs := logbuf.New(50, dir, sec.KnownSecrets)
	defer logs.Close()
	gw := gateway.New(dir, cfg, sec, reg, logs)
	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	// 1. Model fallback + Privacy filter end-to-end: primary returns 429 -> falls back to prov2/backup-model,
	//    upstream never sees real secret/email, and client gets restored text + X-Apx-Redacted + X-Apx-Fallback-From!
	payload := `{"model":"prov1/primary-model","messages":[{"role":"user","content":"Send sk-proj-secret1234567890abcdef to dev@acme.io"}]}`
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewBufferString(payload))
	req.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bodyBytes, _ := io.ReadAll(res.Body)
	res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after model fallback, got %d: %s", res.StatusCode, string(bodyBytes))
	}
	if res.Header.Get("X-Apx-Fallback-From") != "prov1/primary-model" || res.Header.Get("X-Apx-Model") != "prov2/backup-model" {
		t.Fatalf("expected fallback headers, got from=%q model=%q", res.Header.Get("X-Apx-Fallback-From"), res.Header.Get("X-Apx-Model"))
	}
	if !strings.Contains(res.Header.Get("X-Apx-Redacted"), "api_key=1") || !strings.Contains(res.Header.Get("X-Apx-Redacted"), "email=1") {
		t.Fatalf("expected X-Apx-Redacted header, got %q", res.Header.Get("X-Apx-Redacted"))
	}
	if strings.Contains(fallbackSaw, "sk-proj-secret1234567890abcdef") || strings.Contains(fallbackSaw, "dev@acme.io") {
		t.Fatalf("upstream leaked raw secret/email: %s", fallbackSaw)
	}
	if !strings.Contains(string(bodyBytes), "sk-proj-secret1234567890abcdef") || !strings.Contains(string(bodyBytes), "dev@acme.io") {
		t.Fatalf("expected client response to have restored values, got: %s", string(bodyBytes))
	}

	// 2. Project-based routing via project-scoped API key routes "work-model" to p2 (backup) instead of p1 (primary)!
	req2, _ := http.NewRequest("POST", ts.URL+"/v1/chat/completions", bytes.NewBufferString(`{"model":"work-model","messages":[{"role":"user","content":"hello"}]}`))
	req2.Header.Set("Authorization", "Bearer apx_work_key_99999999")
	res2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusOK || res2.Header.Get("X-Apx-Account") != "prov2/backup" || res2.Header.Get("X-Apx-Project") != "work" {
		t.Fatalf("expected project-routed prov2/backup for work key, got status=%d acct=%q proj=%q", res2.StatusCode, res2.Header.Get("X-Apx-Account"), res2.Header.Get("X-Apx-Project"))
	}

	// 3. Expanded usage reporting tracks API key, project, and period filter
	uSum := gw.LiveUsagePeriod("1h")
	if uSum.Period != "1h" || !uSum.CostEstimated || len(uSum.ByKey) < 2 || len(uSum.ByProject) < 2 {
		t.Fatalf("expected expanded usage with ByKey & ByProject, got %+v", uSum)
	}
}

func TestEmbeddingsAndRerankEndpoints(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/embeddings":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"data": []map[string]any{
					{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2, 0.3}},
				},
				"usage": map[string]any{"prompt_tokens": 6, "total_tokens": 6},
			})
		case "/v1/rerank":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"results": []map[string]any{
					{"index": 1, "relevance_score": 0.95},
					{"index": 0, "relevance_score": 0.12},
				},
				"usage": map[string]any{"total_tokens": 18},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer up.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.LocalKey = "apx_embed_test_key_12345"
	cfg.Accounts = []config.AccountConfig{
		{
			ID:             "embed-main",
			Name:           "main",
			Kind:           config.KindAPIKey,
			Backend:        "openai",
			Enabled:        true,
			BaseURL:        up.URL + "/v1",
			AuthStyle:      config.AuthNone,
			Models:         []string{"text-embedding-3-small", "rerank-v3"},
			MaxConcurrency: 4,
		},
	}

	sec := secrets.NewFileOnly(dir)
	reg, _ := descriptors.LoadRegistry(dir)
	gw := gateway.New(dir, cfg, sec, reg, nil)
	ts := httptest.NewServer(gw.Handler())
	defer ts.Close()

	// POST /v1/embeddings
	embReq, _ := http.NewRequest("POST", ts.URL+"/v1/embeddings", bytes.NewBufferString(`{"model":"text-embedding-3-small","input":"hello world"}`))
	embReq.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
	embReq.Header.Set("X-Apx-Project", "search")
	embRes, err := http.DefaultClient.Do(embReq)
	if err != nil {
		t.Fatal(err)
	}
	embBody, _ := io.ReadAll(embRes.Body)
	embRes.Body.Close()
	if embRes.StatusCode != http.StatusOK || !strings.Contains(string(embBody), `"embedding"`) {
		t.Fatalf("unexpected embeddings response %d: %s", embRes.StatusCode, string(embBody))
	}

	// POST /v1/rerank
	rrReq, _ := http.NewRequest("POST", ts.URL+"/v1/rerank", bytes.NewBufferString(`{"model":"rerank-v3","query":"go gateway","documents":["cats","agentpassage gateway"]}`))
	rrReq.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
	rrReq.Header.Set("X-Apx-Project", "search")
	rrRes, err := http.DefaultClient.Do(rrReq)
	if err != nil {
		t.Fatal(err)
	}
	rrBody, _ := io.ReadAll(rrRes.Body)
	rrRes.Body.Close()
	if rrRes.StatusCode != http.StatusOK || !strings.Contains(string(rrBody), `"relevance_score"`) {
		t.Fatalf("unexpected rerank response %d: %s", rrRes.StatusCode, string(rrBody))
	}

	// Verify usage recorded for project "search" and estimated cost > 0
	sum := gw.LiveUsagePeriod("24h")
	if sum.TotalRequests != 2 || sum.EstimatedCostUSD <= 0 {
		t.Fatalf("expected 2 requests with positive estimated cost, got %+v", sum)
	}
}
