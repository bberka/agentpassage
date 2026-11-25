package acpbackend

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/bberka/acpx/internal/backend"
	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/descriptors"
)

// TestHelperACPProcess acts as a realistic ACP JSON-RPC stdio server subprocess during tests.
func TestHelperACPProcess(t *testing.T) {
	if os.Getenv("ACPX_TEST_ACP_HELPER") != "1" {
		return
	}
	profile := os.Getenv("ACPX_TEST_PROFILE")
	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	writeMsg := func(v any) {
		b, _ := json.Marshal(v)
		_, _ = writer.Write(append(b, '\n'))
		_ = writer.Flush()
	}

	sessionCounter := 0
	modelsBySession := map[string]string{}

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
			res, _ := json.Marshal(map[string]any{
				"sessionId": sid,
				"configOptions": []map[string]any{
					{
						"id":           "model",
						"category":     "model",
						"currentValue": "default",
						"options": []map[string]string{
							{"value": "default", "name": "Default"},
							{"value": "gemini-3.1-pro", "name": "Gemini 3.1 Pro"},
						},
					},
				},
			})
			writeMsg(rpcMessage{JSONRPC: "2.0", ID: msg.ID, Result: res})

		case "session/set_config_option":
			var p setConfigOptionParams
			_ = json.Unmarshal(msg.Params, &p)
			modelsBySession[p.SessionID] = p.Value
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

			// Emit tool activity + two streaming text chunks
			toolUpd, _ := json.Marshal(map[string]any{
				"sessionId": p.SessionID,
				"update":    map[string]any{"sessionUpdate": "tool_call", "title": "read_file"},
			})
			writeMsg(rpcMessage{JSONRPC: "2.0", Method: "session/update", Params: toolUpd})

			reply := fmt.Sprintf("[%s|%s|%s] %s", profile, p.SessionID, modelsBySession[p.SessionID], text)
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
			"ACPX_TEST_ACP_HELPER": "1",
			"ACPX_TEST_PROFILE":    "{profile}",
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
