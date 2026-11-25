package clibackend

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/bberka/acpx/internal/backend"
	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/descriptors"
)

// TestHelperCLIProcess emulates a streaming NDJSON coding CLI (stream-json / codex-jsonl / opencode-json).
func TestHelperCLIProcess(t *testing.T) {
	mode := os.Getenv("ACPX_TEST_CLI_MODE")
	if mode == "" {
		return
	}
	args := os.Args
	prompt := args[len(args)-1]

	switch mode {
	case "claude":
		fmt.Println(`{"type":"system","session_id":"cl-sess-42"}`)
		fmt.Printf(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello from Claude CLI: "}}}` + "\n")
		fmt.Printf(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":%q}}}`+"\n", prompt)
		fmt.Printf(`{"type":"result","session_id":"cl-sess-42","result":"duplicate summary ignored"}` + "\n")
	case "codex":
		fmt.Println(`{"type":"thread.started","thread_id":"cx-thread-99"}`)
		fmt.Printf(`{"type":"item.completed","item":{"type":"agent_message","text":"Codex reply: %s"}}`+"\n", prompt)
	case "opencode":
		fmt.Printf(`{"type":"text","sessionID":"oc-sess-7","part":{"type":"text","text":"OpenCode reply: %s"}}`+"\n", prompt)
	}
	os.Exit(0)
}

func TestDirectCLIStreamingAndParsers(t *testing.T) {
	dir := t.TempDir()
	reg, _ := descriptors.LoadRegistry(dir)
	mgr := NewManager(dir, reg, nil)

	tests := []struct {
		name      string
		backendID string
		envMode   string
		wantText  string
		wantSID   string
	}{
		{"claude stream-json", "claude", "claude", "Hello from Claude CLI: ping", "cl-sess-42"},
		{"codex jsonl", "codex", "codex", "Codex reply: ping", "cx-thread-99"},
		{"opencode json", "opencode", "opencode", "OpenCode reply: ping", "oc-sess-7"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			acct := config.AccountConfig{
				ID:             tc.backendID + "-test",
				Name:           "test",
				Kind:           config.KindCLI,
				Backend:        tc.backendID,
				Enabled:        true,
				UseHostProfile: true,
				CustomCommand:  []string{os.Args[0], "-test.run=TestHelperCLIProcess", "--", "{prompt}"},
				CustomEnv:      map[string]string{"ACPX_TEST_CLI_MODE": tc.envMode},
			}
			var chunks []string
			var gotSID string
			err := mgr.Stream(context.Background(), acct, backend.ChatRequest{
				Model:         tc.backendID + "/default",
				UpstreamModel: "default",
				Messages:      []backend.Message{{Role: "user", Content: "ping"}},
				SessionKey:    "conv-1",
			}, func(ch backend.StreamChunk) error {
				if ch.Delta != "" {
					chunks = append(chunks, ch.Delta)
				}
				if ch.SessionID != "" {
					gotSID = ch.SessionID
				}
				return nil
			})
			if err != nil {
				t.Fatalf("Stream failed: %v", err)
			}
			got := strings.Join(chunks, "")
			if got != tc.wantText {
				t.Fatalf("got %q, want %q", got, tc.wantText)
			}
			if gotSID != tc.wantSID {
				t.Fatalf("got sessionID %q, want %q", gotSID, tc.wantSID)
			}
		})
	}
}

func TestBuildCLIArgsAndModelsParsing(t *testing.T) {
	reg, _ := descriptors.LoadRegistry(t.TempDir())
	codex, _ := reg.GetCLI("codex")
	acct := config.AccountConfig{Kind: config.KindCLI, Backend: "codex", PermissionMode: config.PermAutoApprove}

	args1, err := buildCLIArgs(codex, acct, "o4-mini", "", "hello", "", "/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	joined1 := strings.Join(args1, " ")
	if !strings.Contains(joined1, "codex exec --json --skip-git-repo-check --model o4-mini --full-auto hello") {
		t.Fatalf("unexpected codex args: %s", joined1)
	}

	args2, err := buildCLIArgs(codex, acct, "o4-mini", "", "followup", "cx-thread-99", "/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.Join(args2, " "), "codex exec resume cx-thread-99") {
		t.Fatalf("expected codex exec resume, got: %v", args2)
	}

	agy, _ := reg.GetCLI("agy")
	args3, err := buildCLIArgs(agy, config.AccountConfig{Kind: config.KindCLI, Backend: "agy"}, "gemini-4-neon", "high", "think", "", "/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(args3, " "), "--effort high") {
		t.Fatalf("expected --effort high in agy CLI args, got: %v", args3)
	}

	models := parseModelsCLIOutput("Fetching available models...\n  gemini-4-neon  (default)\n• gemini-3.1-pro\n")
	if len(models) != 2 || models[0] != "gemini-4-neon" || models[1] != "gemini-3.1-pro" {
		t.Fatalf("unexpected parsed models: %v", models)
	}
}
