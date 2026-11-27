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

// Every client format must carry tools and earlier tool calls to every
// provider format, and bring the model's new call back.
func TestToolCallsBetweenFormats(t *testing.T) {
	const email = "alice@example.com"
	var mu sync.Mutex
	var sent map[string]any
	var sentRaw string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		sentRaw, sent = string(body), nil
		_ = json.Unmarshal(body, &sent)
		stream, _ := sent["stream"].(bool)
		mu.Unlock()
		// The model calls the tool with the placeholder it was given, split in two.
		args := `{\"to\":\"{{APX_EMAIL_1}}\"}`
		first, second := args[:12], args[12:]
		events := func(lines ...string) {
			for _, l := range lines {
				_, _ = io.WriteString(w, l+"\n\n")
			}
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages") && stream:
			events(`event: content_block_start`+"\n"+`data: {"index":0,"content_block":{"type":"tool_use","id":"new","name":"send"}}`,
				`event: content_block_delta`+"\n"+`data: {"delta":{"type":"input_json_delta","partial_json":"`+first+`"}}`,
				`event: content_block_delta`+"\n"+`data: {"delta":{"type":"input_json_delta","partial_json":"`+second+`"}}`,
				`event: content_block_stop`+"\n"+`data: {}`,
				`event: message_delta`+"\n"+`data: {"delta":{"stop_reason":"tool_use"}}`,
				`event: message_stop`+"\n"+`data: {}`)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			_, _ = io.WriteString(w, `{"content":[{"type":"tool_use","id":"new","name":"send","input":{"to":"{{APX_EMAIL_1}}"}}],"stop_reason":"tool_use"}`)
		case stream:
			events(`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"new","type":"function","function":{"name":"send","arguments":"`+first+`"}}]}}]}`,
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"`+second+`"}}]}}]}`,
				`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
				`data: [DONE]`)
		default:
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":null,"tool_calls":[{"id":"new","type":"function","function":{"name":"send","arguments":"`+args+`"}}]},"finish_reason":"tool_calls"}]}`)
		}
	}))
	defer upstream.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Server.LocalKey = "admin-key"
	cfg.Privacy = config.PrivacyConfig{Enabled: true, RedactEmails: true}
	account := func(backendID, auth string) config.AccountConfig {
		return config.AccountConfig{ID: backendID, Name: "main", Kind: config.KindAPIKey, Backend: backendID, Enabled: true,
			BaseURL: upstream.URL + "/v1", AuthStyle: auth, Models: []string{"m"}, MaxConcurrency: 4}
	}
	cfg.Accounts = []config.AccountConfig{account("openai", config.AuthNone), account("anthropic", config.AuthAnthropic)}
	reg, _ := descriptors.LoadRegistry(dir)
	ts := httptest.NewServer(gateway.New(dir, cfg, secrets.NewFileOnly(dir), reg, nil).Handler())
	defer ts.Close()

	requests := map[string]string{
		"/v1/chat/completions": `"tool_choice":"required",
			"tools":[{"type":"function","function":{"name":"send","description":"d","parameters":{"type":"object"}}}],
			"messages":[{"role":"user","content":"mail ` + email + `"},
				{"role":"assistant","content":null,"tool_calls":[{"id":"old","type":"function","function":{"name":"send","arguments":"{\"to\":\"` + email + `\"}"}}]},
				{"role":"tool","tool_call_id":"old","content":"sent-ok"}]}`,
		"/v1/messages": `"tool_choice":{"type":"any"},
			"tools":[{"name":"send","description":"d","input_schema":{"type":"object"}}],
			"messages":[{"role":"user","content":"mail ` + email + `"},
				{"role":"assistant","content":[{"type":"tool_use","id":"old","name":"send","input":{"to":"` + email + `"}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"old","content":"sent-ok"}]}]}`,
	}
	// What the provider must receive, by provider format.
	wants := map[string][]string{
		"openai":    {`"tool_calls"`, `"tool_call_id":"old"`, `"sent-ok"`, `"tool_choice":"required"`, `"parameters"`},
		"anthropic": {`"type":"tool_use"`, `"tool_use_id":"old"`, `"sent-ok"`, `"type":"any"`, `"input_schema"`},
	}
	for path, body := range requests {
		for provider, want := range wants {
			for _, stream := range []string{"false", "true"} {
				name := path + " -> " + provider + " stream=" + stream
				status, out := call(t, "POST", ts.URL+path, "admin-key", `{"model":"`+provider+`/m","max_tokens":50,"stream":`+stream+`,`+body)
				mu.Lock()
				got := sentRaw
				mu.Unlock()
				if status != http.StatusOK {
					t.Errorf("%s: %d %s", name, status, out)
					continue
				}
				for _, w := range want {
					if !strings.Contains(got, w) {
						t.Errorf("%s: provider got no %s: %s", name, w, got)
					}
				}
				if strings.Contains(got, email) {
					t.Errorf("%s: provider got the email: %s", name, got)
				}
				name2, args, stop := toolReply(t, out)
				wantStop := "tool_calls"
				if path == "/v1/messages" {
					wantStop = "tool_use"
				}
				var parsed struct {
					To string `json:"to"`
				}
				if err := json.Unmarshal([]byte(args), &parsed); err != nil || parsed.To != email || name2 != "send" || stop != wantStop {
					t.Errorf("%s: call %q %q stop %q in %s", name, name2, args, stop, out)
				}
			}
		}
	}
}

// toolReply reads the tool call out of any reply format apx sends.
func toolReply(t *testing.T, out string) (name, args, stop string) {
	t.Helper()
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, item := range x {
				walk(item)
			}
		case map[string]any:
			if s, ok := x["name"].(string); ok {
				name += s
			}
			if s, ok := x["arguments"].(string); ok {
				args += s
			}
			if s, ok := x["partial_json"].(string); ok {
				args += s
			}
			// A complete Anthropic block; streamed ones start with an empty input.
			if in, ok := x["input"].(map[string]any); ok && len(in) > 0 {
				raw, _ := json.Marshal(in)
				args += string(raw)
			}
			for _, key := range []string{"finish_reason", "stop_reason"} {
				if s, ok := x[key].(string); ok {
					stop = s
				}
			}
			for _, item := range x {
				walk(item)
			}
		}
	}
	chunks := []string{out}
	if strings.Contains(out, "data: ") {
		chunks = nil
		for _, line := range strings.Split(out, "\n") {
			if data, ok := strings.CutPrefix(line, "data: "); ok && data != "[DONE]" {
				chunks = append(chunks, data)
			}
		}
	}
	for _, c := range chunks {
		var v any
		if err := json.Unmarshal([]byte(c), &v); err != nil {
			t.Fatalf("bad JSON %q", c)
		}
		walk(v)
	}
	return name, args, stop
}
