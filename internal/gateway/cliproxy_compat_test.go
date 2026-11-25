package gateway_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/gateway"
	"github.com/bberka/agentpassage/internal/logbuf"
	"github.com/bberka/agentpassage/internal/secrets"
)

func TestCLIProxyCompatManagementEndpoints(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.LocalKey = "apx_mgmt_secret"
	cfg.Accounts = []config.AccountConfig{
		{ID: "agy-bb", Name: "bb", Kind: config.KindACP, Backend: "agy", Enabled: true, Models: []string{"gemini-4-neon"}},
		{ID: "claude-main", Name: "main", Kind: config.KindACP, Backend: "claude", Enabled: true, Models: []string{"fable"}},
		{ID: "codex-bb", Name: "bb", Kind: config.KindACP, Backend: "codex", Enabled: true, Models: []string{"gpt-6-luna"}},
		{ID: "deepseek-main", Name: "main", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, Models: []string{"deepseek-chat"}},
	}

	sec := secrets.NewFileOnly(dir)
	reg, _ := descriptors.LoadRegistry(dir)
	logs := logbuf.New(50, dir, sec.KnownSecrets)
	defer logs.Close()
	srv := gateway.New(dir, cfg, sec, reg, logs)
	h := srv.Handler()

	// 1. Auth via X-Management-Key header works on /v0/management/auth-files
	req := httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	req.Header.Set("X-Management-Key", "apx_mgmt_secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on /v0/management/auth-files, got %d: %s", rec.Code, rec.Body.String())
	}
	var af struct {
		Files []struct {
			AuthIndex string `json:"auth_index"`
			Provider  string `json:"provider"`
			Name      string `json:"name"`
			ProjectID string `json:"project_id"`
		} `json:"files"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &af); err != nil {
		t.Fatalf("decode auth-files: %v", err)
	}
	if af.Total != 4 || len(af.Files) != 4 {
		t.Fatalf("expected 4 auth files, got %+v", af)
	}
	if af.Files[0].Provider != "antigravity" || af.Files[0].AuthIndex != "agy-bb" || af.Files[0].ProjectID == "" {
		t.Fatalf("expected agy mapped to antigravity with project_id, got %+v", af.Files[0])
	}

	// 2. Unauthorized without management key
	reqNoAuth := httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	recNoAuth := httptest.NewRecorder()
	h.ServeHTTP(recNoAuth, reqNoAuth)
	if recNoAuth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key, got %d", recNoAuth.Code)
	}

	// 3. POST /v0/management/api-call synthesizes upstream quota responses for Codex, Claude, and Antigravity
	for _, tc := range []struct {
		authIndex string
		url       string
		wantKey   string
	}{
		{"codex-bb", "https://chatgpt.com/backend-api/wham/usage", "rate_limit"},
		{"claude-main", "https://api.anthropic.com/api/oauth/usage", "seven_day"},
		{"agy-bb", "https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels", "models"},
		{"deepseek-main", "https://api.deepseek.com/user/balance", "balance_infos"},
	} {
		payload, _ := json.Marshal(map[string]any{
			"auth_index": tc.authIndex,
			"method":     "GET",
			"url":        tc.url,
		})
		rCall := httptest.NewRequest(http.MethodPost, "/v0/management/api-call", bytes.NewReader(payload))
		rCall.Header.Set("Authorization", "Bearer apx_mgmt_secret")
		wCall := httptest.NewRecorder()
		h.ServeHTTP(wCall, rCall)
		if wCall.Code != http.StatusOK {
			t.Fatalf("api-call %s failed: %d %s", tc.authIndex, wCall.Code, wCall.Body.String())
		}
		var env struct {
			StatusCode int    `json:"status_code"`
			Body       string `json:"body"`
		}
		if err := json.Unmarshal(wCall.Body.Bytes(), &env); err != nil || env.StatusCode != 200 {
			t.Fatalf("bad api-call envelope for %s: %s", tc.authIndex, wCall.Body.String())
		}
		var inner map[string]any
		if err := json.Unmarshal([]byte(env.Body), &inner); err != nil {
			t.Fatalf("bad inner JSON body for %s: %v", tc.authIndex, err)
		}
		if _, ok := inner[tc.wantKey]; !ok {
			t.Fatalf("expected key %q in %s body, got %s", tc.wantKey, tc.authIndex, env.Body)
		}
	}

	// 4. GET /v0/management/usage, /v0/management/usage-queue, /v0/management/config, /v0/management/auth-files/models, /v0/management/auth-files/download, /v8/management/auth-files
	for _, path := range []string{
		"/v0/management/usage",
		"/v0/management/usage-queue",
		"/v0/management/config",
		"/v0/management/latest-version",
		"/v0/management/auth-files/models?name=antigravity-bb.json",
		"/v0/management/auth-files/download?name=antigravity-bb.json",
		"/v8/management/auth-files",
	} {
		r2 := httptest.NewRequest(http.MethodGet, path, nil)
		r2.Header.Set("X-Management-Key", "apx_mgmt_secret")
		w2 := httptest.NewRecorder()
		h.ServeHTTP(w2, r2)
		if w2.Code != http.StatusOK {
			t.Fatalf("expected 200 on %s, got %d: %s", path, w2.Code, w2.Body.String())
		}
	}
}
