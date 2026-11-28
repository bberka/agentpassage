package acpbridge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/gateway"
	"github.com/bberka/agentpassage/internal/secrets"
)

// TestHelperCommandAgent is an ACP agent that offers a command as soon as a
// session opens and replies with the session and the prompt it received.
func TestHelperCommandAgent(t *testing.T) {
	if os.Getenv("APX_TEST_COMMAND_AGENT") != "1" {
		return
	}
	out := func(v map[string]any) {
		v["jsonrpc"] = "2.0"
		b, _ := json.Marshal(v)
		fmt.Println(string(b))
	}
	update := func(sid any, u map[string]any) {
		out(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": sid, "update": u}})
	}
	sessions := 0
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var m struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
				Prompt    []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			} `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch m.Method {
		case "session/new":
			sessions++
			sid := fmt.Sprintf("up-%d", sessions)
			// Before the reply, the earliest an agent can send it.
			update(sid, map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []any{
				map[string]any{"name": "compact", "description": "Compact", "input": map[string]any{"hint": "focus"}},
			}})
			out(map[string]any{"id": m.ID, "result": map[string]any{"sessionId": sid}})
		case "session/prompt":
			update(m.Params.SessionID, map[string]any{"sessionUpdate": "agent_message_chunk",
				"content": map[string]any{"type": "text", "text": m.Params.SessionID + ":" + m.Params.Prompt[0].Text}})
			out(map[string]any{"id": m.ID, "result": map[string]any{"stopReason": "end_turn"}})
		default:
			out(map[string]any{"id": m.ID, "result": map[string]any{}})
		}
	}
	os.Exit(0)
}

// The host gets the agent's own commands before the first prompt, a typed
// command reaches the same session unchanged, and another provider's model
// takes the commands off the list.
func TestAgentCommandsPassThrough(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Accounts = []config.AccountConfig{
		{ID: "agent", Name: "agent", Kind: config.KindACP, Backend: "custom-acp", Enabled: true, MaxConcurrency: 1,
			ExposeMode: config.ExposePrefix, ModelPrefix: "agent-", Models: []string{"m1"},
			CustomCommand: []string{os.Args[0], "-test.run=TestHelperCommandAgent"},
			CustomEnv:     map[string]string{"APX_TEST_COMMAND_AGENT": "1"}},
		{ID: "api", Name: "api", Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true, MaxConcurrency: 1,
			ExposeMode: config.ExposePrefix, ModelPrefix: "api-", Models: []string{"m2"},
			BaseURL: "http://127.0.0.1:1/v1", AuthStyle: config.AuthNone},
	}
	reg, _ := descriptors.LoadRegistry(dir)
	gw := gateway.New(dir, cfg, secrets.NewFileOnly(dir), reg, nil)
	defer gw.ACP().Close()

	runBridgeInteractive(t, gw, func(send func(string), readLine func() map[string]any) {
		// read returns the next update of a kind, or the reply to a request.
		read := func(want string) map[string]any {
			for {
				msg := readLine()
				if msg["error"] != nil {
					t.Fatalf("error: %v", msg)
				}
				if want == "reply" && msg["result"] != nil {
					return msg["result"].(map[string]any)
				}
				params, _ := msg["params"].(map[string]any)
				if u, _ := params["update"].(map[string]any); u["sessionUpdate"] == want {
					return u
				}
			}
		}
		send(`{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"` + t.TempDir() + `"}}`)
		sid := read("reply")["sessionId"].(string)
		send(`{"jsonrpc":"2.0","id":2,"method":"session/set_config_option","params":{"sessionId":"` + sid + `","configId":"model","value":"agent-m1"}}`)

		commands := read("available_commands_update")["availableCommands"].([]any)
		for len(commands) == 0 {
			commands = read("available_commands_update")["availableCommands"].([]any)
		}
		got, _ := json.Marshal(commands)
		if string(got) != `[{"description":"Compact","input":{"hint":"focus"},"name":"compact"}]` {
			t.Fatalf("commands = %s", got)
		}

		send(`{"jsonrpc":"2.0","id":3,"method":"session/prompt","params":{"sessionId":"` + sid + `","prompt":[{"type":"text","text":"/compact keep tests"}]}}`)
		content := read("agent_message_chunk")["content"].(map[string]any)
		if content["text"] != "up-1:/compact keep tests" {
			t.Fatalf("agent received %q", content["text"])
		}
		read("reply")

		send(`{"jsonrpc":"2.0","id":4,"method":"session/set_model","params":{"sessionId":"` + sid + `","modelId":"api-m2"}}`)
		if left := read("available_commands_update")["availableCommands"].([]any); len(left) != 0 {
			t.Fatalf("API model still offers %v", left)
		}
	})
}
