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

// A streamed 429 for one model family must not block the account's others.
func TestStreamedRateLimitCoolsDownOnlyTheModelGroup(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if model, _ := req["model"].(string); strings.Contains(model, "fable") {
			w.Header().Set("Retry-After", "3600")
			http.Error(w, `{"error":{"message":"rate limit"}}`, http.StatusTooManyRequests)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "sonnet-ok"}, "finish_reason": "stop"}},
		})
	}))
	defer upstream.Close()

	for _, path := range []string{"/v1/chat/completions", "/v1/messages"} {
		dir := t.TempDir()
		cfg := config.Default()
		cfg.Server.LocalKey = "apx_test_local_key_1234"
		cfg.Accounts = []config.AccountConfig{{
			ID: "claude-api", Name: "api", Kind: config.KindAPIKey, Backend: "claude", Enabled: true,
			BaseURL: upstream.URL + "/v1", AuthStyle: config.AuthBearer,
			Models: []string{"claude-fable-5", "claude-sonnet-5"}, MaxConcurrency: 4,
		}}
		sec := secrets.NewFileOnly(dir)
		if err := sec.Set("claude-api", "sk-test"); err != nil {
			t.Fatal(err)
		}
		reg, _ := descriptors.LoadRegistry(dir)
		logs := logbuf.New(100, dir, sec.KnownSecrets)
		defer logs.Close()
		ts := httptest.NewServer(gateway.New(dir, cfg, sec, reg, logs).Handler())
		defer ts.Close()

		post := func(model string, stream bool) (int, string) {
			body, _ := json.Marshal(map[string]any{
				"model": model, "stream": stream, "max_tokens": 16,
				"messages": []map[string]string{{"role": "user", "content": "hi"}},
			})
			req, _ := http.NewRequest(http.MethodPost, ts.URL+path, bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			out, _ := io.ReadAll(res.Body)
			return res.StatusCode, string(out)
		}
		if status, out := post("claude/claude-fable-5", true); status == http.StatusOK {
			t.Fatalf("%s: fable should be rate limited, got %s", path, out)
		}
		if status, out := post("claude/claude-sonnet-5", false); status != http.StatusOK || !strings.Contains(out, "sonnet-ok") {
			t.Fatalf("%s: sonnet blocked by a fable limit: %d %s", path, status, out)
		}
	}
}
