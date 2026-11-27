package acpbackend

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
)

// TestHelperACPProcess acts as a realistic ACP JSON-RPC stdio server subprocess during tests.
func TestHelperACPProcess(t *testing.T) {
	if os.Getenv("APX_TEST_ACP_HELPER") != "1" {
		return
	}
	profile := os.Getenv("APX_TEST_PROFILE")
	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	writeMsg := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = writer.Write(append(b, '\n'))
		_ = writer.Flush()
	}

	sessionCounter := 0
	modelsBySession := map[string]string{}
	effortsBySession := map[string]string{}

	for scanner.Scan() {
		var msg rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			res, _ := json.Marshal(map[string]any{
				"protocolVersion": 1,
				"agentInfo":       map[string]string{"name": "mock-acp", "version": "1.0"},
			})
			writeMsg(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Result: res})

		case "session/new":
			sessionCounter++
			sid := fmt.Sprintf("sess-%d", sessionCounter)
			modelsBySession[sid] = "default"
			effortsBySession[sid] = "default"
			res, _ := json.Marshal(map[string]any{
				"sessionId": sid,
				"configOptions": []map[string]any{
					{
						"id":           "model",
						"category":     "model",
						"currentValue": "default",
						"options": []map[string]any{
							{"value": "default", "name": "Default"},
							{"group": "gemini", "name": "Gemini", "options": []map[string]string{
								{"value": "gemini-3.1-pro", "name": "Gemini 3.1 Pro"},
							}},
						},
					},
					{
						"id":           "effort",
						"category":     "thought_level",
						"currentValue": "default",
						"options": []map[string]string{
							{"value": "default", "name": "Default"},
							{"value": "low", "name": "Low"},
							{"value": "high", "name": "High"},
							{"value": "max", "name": "Max"},
						},
					},
				},
			})
			writeMsg(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Result: res})

		case "session/set_config_option":
			var p setConfigOptionParams
			_ = json.Unmarshal(msg.Params, &p)
			if p.ConfigID == "effort" || p.ConfigID == "thought_level" {
				effortsBySession[p.SessionID] = p.Value
			} else {
				modelsBySession[p.SessionID] = p.Value
			}
			writeMsg(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Result: json.RawMessage(`{}`)})

		case "session/prompt":
			var p sessionPromptParams
			_ = json.Unmarshal(msg.Params, &p)
			text := ""
			if len(p.Prompt) > 0 {
				text = p.Prompt[0].Text
			}

			// Optional permission roundtrip test
			if strings.Contains(text, "NEED_PERM") {
				permReq, _ := json.Marshal(map[string]any{
					"sessionId": p.SessionID,
					"toolCall":  map[string]any{"title": "Run tests", "name": "shell"},
					"options": []map[string]string{
						{"optionId": "allow_once", "name": "Allow", "kind": "allow_once"},
						{"optionId": "reject_once", "name": "Reject", "kind": "reject_once"},
					},
				})
				writeMsg(rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`999`), Method: "session/request_permission", Params: permReq})
				if scanner.Scan() {
					var permResp rpcMessage
					_ = json.Unmarshal(scanner.Bytes(), &permResp)
					var pr permissionResult
					_ = json.Unmarshal(permResp.Result, &pr)
					text = "perm=" + pr.Outcome.OptionID
				}
			}

			if strings.Contains(text, "NEED_QUOTA") && profile == os.Getenv("APX_TEST_QUOTA_PROFILE") {
				upd, _ := json.Marshal(map[string]any{
					"sessionId": p.SessionID,
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": "\n⚠ Individual quota reached. Please upgrade your subscription to increase your limits. Resets in 38m2s.\n"},
					},
				})
				writeMsg(rpcMessage{JSONRPC: "2.0", Method: "session/update", Params: upd})
				quotaDone, _ := json.Marshal(sessionPromptResult{StopReason: "end_turn"})
				writeMsg(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Result: quotaDone})
				continue
			}

			// Emit tool activity + two streaming text chunks
			toolUpd, _ := json.Marshal(map[string]any{
				"sessionId": p.SessionID,
				"update":    map[string]any{"sessionUpdate": "tool_call", "title": "read_file"},
			})
			writeMsg(rpcMessage{JSONRPC: "2.0", Method: "session/update", Params: toolUpd})

			reply := fmt.Sprintf("[%s|%s|%s] %s", profile, p.SessionID, modelsBySession[p.SessionID], text)
			if eff := effortsBySession[p.SessionID]; eff != "" && eff != "default" {
				reply = fmt.Sprintf("[%s|%s|%s|effort=%s] %s", profile, p.SessionID, modelsBySession[p.SessionID], eff, text)
			}
			mid := len(reply) / 2
			for _, part := range []string{reply[:mid], reply[mid:]} {
				upd, _ := json.Marshal(map[string]any{
					"sessionId": p.SessionID,
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": part},
					},
				})
				writeMsg(rpcMessage{JSONRPC: "2.0", Method: "session/update", Params: upd})
			}

			done, _ := json.Marshal(sessionPromptResult{StopReason: "end_turn"})
			writeMsg(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Result: done})
		}
	}
	os.Exit(0)
}

func TestACPStreamingSessionReuseAndPermissions(t *testing.T) {
	dataDir := t.TempDir()
	reg, _ := descriptors.LoadRegistry(dataDir)
	mgr := NewManager(dataDir, reg, nil, config.ToolActivityDrop)
	defer mgr.Close()

	acct := config.AccountConfig{
		ID:             "custom-acp-work",
		Name:           "work",
		Kind:           config.KindACP,
		Backend:        "custom-acp",
		Enabled:        true,
		PermissionMode: config.PermDeny,
		CustomCommand:  []string{os.Args[0], "-test.run=TestHelperACPProcess"},
		CustomEnv: map[string]string{
			"APX_TEST_ACP_HELPER": "1",
			"APX_TEST_PROFILE":    "{profile}",
		},
	}

	ctx := context.Background()
	var chunks []string
	err := mgr.Stream(ctx, acct, backend.ChatRequest{
		Model:         "custom-acp/work/gemini-3.1-pro",
		UpstreamModel: "gemini-3.1-pro",
		Messages:      []backend.Message{{Role: "user", Content: "hello"}},
		SessionKey:    "conv-1",
		HasTools:      true,
	}, func(ch backend.StreamChunk) error {
		if ch.Delta != "" {
			chunks = append(chunks, ch.Delta)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ACP Stream failed: %v", err)
	}
	full := strings.Join(chunks, "")
	if !strings.Contains(full, "sess-1|gemini-3.1-pro] hello") {
		t.Fatalf("unexpected ACP reply: %q", full)
	}
	if strings.Contains(full, "[tool:") {
		t.Fatalf("expected tool activity dropped when mode=drop, got %q", full)
	}

	// Second turn with same SessionKey reuses sess-1 and enforces PermDeny
	resp, err := mgr.Complete(ctx, acct, backend.ChatRequest{
		Model:         "custom-acp/work/gemini-3.1-pro",
		UpstreamModel: "gemini-3.1-pro",
		Messages:      []backend.Message{{Role: "user", Content: "NEED_PERM"}},
		SessionKey:    "conv-1",
		HasTools:      true,
	})
	if err != nil {
		t.Fatalf("ACP Complete failed: %v", err)
	}
	if !resp.ToolsIgnored {
		t.Fatalf("expected ToolsIgnored=true")
	}
	if !strings.Contains(resp.Content, "sess-1|gemini-3.1-pro] perm=reject_once") {
		t.Fatalf("expected session reuse + deny permission, got %q", resp.Content)
	}
}

func TestSanitizeChildEnvStripsLoopbackGatewayEnv(t *testing.T) {
	// Local apx loopback URLs and keys must not reach pi-acp/opencode, or they loop.
	env := backend.SanitizeChildEnv(
		[]string{
			"PATH=/usr/bin",
			"OPENAI_BASE_URL=http://127.0.0.1:8787/v1",
			"OPENAI_API_KEY=apx_local_secret",
			"ANTHROPIC_BASE_URL=http://localhost:8787",
			"ANTHROPIC_AUTH_TOKEN=apx_local_secret",
			"SAFE_VAR=keep",
		},
		[]string{"PI_CODING_AGENT_DIR=/tmp/profile"},
		config.AccountConfig{ID: "pi-default", Backend: "pi"},
	)
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"127.0.0.1:8787", "localhost:8787", "apx_local_secret"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("sanitizeChildEnv leaked loopback gateway env %q: %s", forbidden, joined)
		}
	}
	if !strings.Contains(joined, "APX_IN_CHILD=1") || !strings.Contains(joined, "PI_CODING_AGENT_DIR=/tmp/profile") || !strings.Contains(joined, "SAFE_VAR=keep") {
		t.Fatalf("sanitizeChildEnv missing expected entries: %s", joined)
	}
}

func TestACPThoughtLevelDiscoveryAndForwarding(t *testing.T) {
	dir := t.TempDir()
	reg, _ := descriptors.LoadRegistry(dir)
	mgr := NewManager(dir, reg, nil, config.ToolActivityDrop)
	defer mgr.Close()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	acct := config.AccountConfig{
		ID:            "claude-test",
		Name:          "test",
		Kind:          config.KindACP,
		Backend:       "custom-acp",
		Enabled:       true,
		CustomCommand: []string{exe, "-test.run=TestHelperACPProcess"},
		CustomEnv: map[string]string{
			"APX_TEST_ACP_HELPER": "1",
			"APX_TEST_PROFILE":    "{profile}",
		},
	}

	models, labels, efforts, err := mgr.TestAccountDiscovery(context.Background(), acct)
	if err != nil {
		t.Fatalf("TestAccountDiscovery failed: %v", err)
	}
	// The helper lists gemini-3.1-pro inside an option group.
	if !slices.Contains(models, "gemini-3.1-pro") || labels["gemini-3.1-pro"] != "Gemini 3.1 Pro" {
		t.Fatalf("unexpected models=%v labels=%v", models, labels)
	}
	wantEfforts := []string{"low", "high", "max"}
	gotEfforts := efforts["*"]
	if len(gotEfforts) != len(wantEfforts) || gotEfforts[0] != "low" || gotEfforts[1] != "high" || gotEfforts[2] != "max" {
		t.Fatalf("expected discovered thought_level efforts %v, got %v", wantEfforts, gotEfforts)
	}

	resp, err := mgr.Complete(context.Background(), acct, backend.ChatRequest{
		Model:           "claude/gemini-3.1-pro",
		UpstreamModel:   "gemini-3.1-pro",
		ReasoningEffort: "high",
		Messages:        []backend.Message{{Role: "user", Content: "hello"}},
		SessionKey:      "effort-sess",
	})
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if !strings.Contains(resp.Content, "effort=high") {
		t.Fatalf("expected thought_level=high forwarded to ACP worker, got %q", resp.Content)
	}
}

func TestAgyQuotaReplyBecomesRateLimitError(t *testing.T) {
	dataDir := t.TempDir()
	reg, _ := descriptors.LoadRegistry(dataDir)
	mgr := NewManager(dataDir, reg, nil, config.ToolActivityDrop)
	defer mgr.Close()

	acct := config.AccountConfig{
		ID:            "agy-work",
		Name:          "work",
		Kind:          config.KindACP,
		Backend:       "agy",
		Enabled:       true,
		CustomCommand: []string{os.Args[0], "-test.run=TestHelperACPProcess"},
		CustomEnv: map[string]string{
			"APX_TEST_ACP_HELPER":    "1",
			"APX_TEST_PROFILE":       "p",
			"APX_TEST_QUOTA_PROFILE": "p",
		},
	}

	var got strings.Builder
	send := func(text string) error {
		return mgr.Stream(context.Background(), acct, backend.ChatRequest{
			Messages:   []backend.Message{{Role: "user", Content: text}},
			SessionKey: "conv-1",
		}, func(ch backend.StreamChunk) error {
			got.WriteString(ch.Delta)
			return nil
		})
	}

	err := send("NEED_QUOTA")
	be, ok := backend.AsBackendError(err)
	if !ok || be.Kind != backend.ErrKindRateLimit || be.RetryAfter != 38*60+2 {
		t.Fatalf("want rate limit error with 2282s retry, got %#v", err)
	}
	if got.String() != "" {
		t.Fatalf("quota notice reached the client: %q", got.String())
	}

	// The failed session is dropped, so the next turn starts a fresh one.
	if err := send("hello"); err != nil {
		t.Fatalf("follow-up failed: %v", err)
	}
	if !strings.Contains(got.String(), "sess-2|") {
		t.Fatalf("expected a new upstream session, got %q", got.String())
	}
}

func TestIsAgyQuotaReply(t *testing.T) {
	for text, want := range map[string]bool{
		"\n⚠ Individual quota reached. Resets in 38m2s.\n":    true,
		"The log says: Individual quota reached.":             false,
		"⚠ Individual quota reached.\nHere is how to fix it.": false,
		"⚠ This command deletes files.":                       false,
	} {
		if got := isAgyQuotaReply(text); got != want {
			t.Errorf("isAgyQuotaReply(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestSessionReplaysHistoryAfterAnotherAccountAnswers(t *testing.T) {
	dataDir := t.TempDir()
	reg, _ := descriptors.LoadRegistry(dataDir)
	mgr := NewManager(dataDir, reg, nil, config.ToolActivityDrop)
	defer mgr.Close()

	account := func(name string) config.AccountConfig {
		return config.AccountConfig{
			ID:            "switch-" + name,
			Name:          name,
			Kind:          config.KindACP,
			Backend:       "agy",
			Enabled:       true,
			CustomCommand: []string{os.Args[0], "-test.run=TestHelperACPProcess"},
			CustomEnv: map[string]string{
				"APX_TEST_ACP_HELPER":    "1",
				"APX_TEST_PROFILE":       name,
				"APX_TEST_QUOTA_PROFILE": "b",
			},
		}
	}
	a, b := account("a"), account("b")

	var history []backend.Message
	send := func(acct config.AccountConfig, key, text string) (string, error) {
		history = append(history, backend.Message{Role: "user", Content: text})
		resp, err := mgr.Complete(context.Background(), acct, backend.ChatRequest{Messages: history, SessionKey: key})
		if err != nil {
			history = history[:len(history)-1]
			return "", err
		}
		history = append(history, backend.Message{Role: "assistant", Content: "ok"})
		return resp.Content, nil
	}
	expect := func(acct config.AccountConfig, text, session string, replayed ...string) {
		t.Helper()
		got, err := send(acct, "conv-switch", text)
		if err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		if !strings.Contains(got, "|"+session+"|") {
			t.Fatalf("%s: want %s, got %q", text, session, got)
		}
		for _, want := range replayed {
			if !strings.Contains(got, want) {
				t.Fatalf("%s: history lacks %q: %q", text, want, got)
			}
		}
		if len(replayed) == 0 && strings.Contains(got, "[User]") {
			t.Fatalf("%s: reused session was sent the history: %q", text, got)
		}
	}

	expect(a, "one", "sess-1")
	expect(a, "two", "sess-1")
	expect(b, "three", "sess-1", "one", "two")
	expect(a, "four", "sess-2", "one", "three")
	expect(a, "five", "sess-2")

	// A turn another account failed does not take the conversation from A.
	if _, err := send(b, "conv-switch", "NEED_QUOTA"); err == nil {
		t.Fatal("expected quota error")
	}
	expect(a, "six", "sess-2")

	// A turn answered by a different backend type also invalidates A's session.
	backend.RecordAnswer("conv-switch", "some-http-account")
	expect(a, "seven", "sess-3", "six")

	// A failed turn drops the session, even without an account switch.
	failed := errors.New("client gone")
	err := mgr.Stream(context.Background(), a, backend.ChatRequest{Messages: history, SessionKey: "conv-switch"},
		func(backend.StreamChunk) error { return failed })
	if !errors.Is(err, failed) {
		t.Fatalf("want callback error, got %v", err)
	}
	expect(a, "eight", "sess-4", "seven")
}
