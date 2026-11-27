package gateway_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/gateway"
	"github.com/bberka/agentpassage/internal/secrets"
)

// keyedGateway serves one API-key model and one ACP model to two client keys.
// It returns the message lists the upstream received.
func keyedGateway(t *testing.T) (*gateway.Server, string, func() [][]map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var seen [][]map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []map[string]any `json:"messages"`
			Stream   bool             `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		seen = append(seen, req.Messages)
		mu.Unlock()
		if !req.Stream {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}]}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(upstream.Close)

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.LocalKey = "admin-key"
	cfg.Server.APIKeys = []config.ClientKey{
		{ID: "plain", Name: "plain", Key: "plain-key"},
		{ID: "trusted", Name: "trusted", Key: "trusted-key", Agents: true},
	}
	cfg.Accounts = []config.AccountConfig{
		{ID: "api", Name: "main", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true,
			BaseURL: upstream.URL + "/v1", AuthStyle: config.AuthNone, Models: []string{"chat"}, MaxConcurrency: 8},
		{ID: "agent", Name: "work", Kind: config.KindACP, Backend: "claude", Enabled: true,
			CustomCommand: []string{"apx-test-no-such-agent"}, Models: []string{"opus"}, MaxConcurrency: 8},
	}
	cfg.Routing = []config.RoutingRule{{Pattern: "deepseek/*", Strategy: config.StrategyFailover}}
	reg, _ := descriptors.LoadRegistry(dir)
	srv := gateway.New(dir, cfg, secrets.NewFileOnly(dir), reg, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts.URL, func() [][]map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return append([][]map[string]any(nil), seen...)
	}
}

func call(t *testing.T, method, url, key, body string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(out)
}

func TestClientKeyNeedsPermissionForAgentModels(t *testing.T) {
	_, url, _ := keyedGateway(t)
	requests := map[string]string{
		"/v1/chat/completions": `{"model":"claude/opus","messages":[{"role":"user","content":"hi"}]}`,
		"/v1/messages":         `{"model":"claude/opus","messages":[{"role":"user","content":"hi"}]}`,
		"/v1/responses":        `{"model":"claude/opus","input":"hi"}`,
		"/v1/embeddings":       `{"model":"claude/opus","input":"hi"}`,
		"/v1/audio/speech":     `{"model":"claude/opus","input":"hi"}`,
	}
	for path, body := range requests {
		for _, stream := range []string{body, strings.Replace(body, `{`, `{"stream":true,`, 1)} {
			if status, out := call(t, "POST", url+path, "plain-key", stream); status != http.StatusForbidden {
				t.Errorf("%s with a plain client key: %d %s", path, status, out)
			}
		}
		for _, key := range []string{"trusted-key", "admin-key"} {
			if status, out := call(t, "POST", url+path, key, body); status == http.StatusForbidden {
				t.Errorf("%s with %s was refused: %s", path, key, out)
			}
		}
	}

	if _, out := call(t, "GET", url+"/v1/models", "plain-key", ""); strings.Contains(out, "claude/opus") || !strings.Contains(out, "deepseek/chat") {
		t.Errorf("plain client key model list: %s", out)
	}
	if _, out := call(t, "GET", url+"/v1/models", "trusted-key", ""); !strings.Contains(out, "claude/opus") {
		t.Errorf("trusted key model list: %s", out)
	}
	if status, out := call(t, "POST", url+"/v1/chat/completions", "plain-key", `{"model":"deepseek/chat","messages":[{"role":"user","content":"hi"}]}`); status != http.StatusOK {
		t.Errorf("API-key model with a plain client key: %d %s", status, out)
	}
}

// Codex shows nothing unless the stream opens and closes the reply item.
func TestResponsesStreamEvents(t *testing.T) {
	_, url, seen := keyedGateway(t)
	body := `{"model":"deepseek/chat","stream":true,"input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"rules"}]},{"role":"user","content":"hi"}]}`
	status, out := call(t, "POST", url+"/v1/responses", "admin-key", body)
	var events []string
	for _, line := range strings.Split(out, "\n") {
		if name, ok := strings.CutPrefix(line, "event: "); ok {
			events = append(events, name)
		}
	}
	want := "response.created response.output_item.added response.content_part.added response.output_text.delta " +
		"response.output_text.done response.content_part.done response.output_item.done response.completed"
	if got := strings.Join(events, " "); status != http.StatusOK || got != want {
		t.Errorf("status %d, events %q", status, got)
	}
	if role := seen()[0][0]["role"]; role != "system" {
		t.Errorf("developer message reached the provider as %v", role)
	}
}

func TestResponsesRecordsUsageOnceAndChainsPerKey(t *testing.T) {
	srv, url, seen := keyedGateway(t)
	first := `{"model":"deepseek/chat","instructions":"be brief","input":"one"}`
	status, out := call(t, "POST", url+"/v1/responses", "plain-key", first)
	if status != http.StatusOK {
		t.Fatalf("first response: %d %s", status, out)
	}
	var res struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.ID == "" {
		t.Fatalf("no response id in %s", out)
	}
	if got := srv.Usage().Summary().TotalRequests; got != 1 {
		t.Errorf("one request recorded %d usage entries", got)
	}

	next := `{"model":"deepseek/chat","previous_response_id":"` + res.ID + `","input":"two"}`
	if status, out := call(t, "POST", url+"/v1/responses", "trusted-key", next); status != http.StatusNotFound {
		t.Errorf("another key continued the response: %d %s", status, out)
	}
	if status, out := call(t, "POST", url+"/v1/responses", "plain-key", next); status != http.StatusOK {
		t.Fatalf("follow-up: %d %s", status, out)
	}
	msgs := seen()[len(seen())-1]
	var roles []string
	for _, m := range msgs {
		roles = append(roles, m["role"].(string))
	}
	if got := strings.Join(roles, ","); got != "user,assistant,user" {
		t.Errorf("follow-up sent roles %s: instructions must not carry over", got)
	}
}
