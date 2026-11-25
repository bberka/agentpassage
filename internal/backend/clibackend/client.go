package clibackend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/logbuf"
)

// Manager executes direct CLI proxy requests (claude -p, codex exec, agy --print, opencode run, etc.)
// without requiring an external ACP adapter.
type Manager struct {
	mu       sync.Mutex
	dataDir  string
	registry *descriptors.Registry
	logs     *logbuf.Buffer
	sessions map[string]string // "<accountID>:<sessionKey>" -> CLI conversation/session ID
}

func NewManager(dataDir string, reg *descriptors.Registry, logs *logbuf.Buffer) *Manager {
	return &Manager{
		dataDir:  dataDir,
		registry: reg,
		logs:     logs,
		sessions: make(map[string]string),
	}
}

func (m *Manager) LoginCommand(acct config.AccountConfig) (*exec.Cmd, string, error) {
	desc, ok := m.registry.GetCLI(acct.Backend)
	if !ok {
		return nil, "", fmt.Errorf("unknown CLI backend %q", acct.Backend)
	}
	if len(desc.LoginCmd) == 0 && len(acct.CustomLogin) == 0 {
		return nil, "", fmt.Errorf("backend %q has no login command configured", acct.Backend)
	}
	loginArgs := desc.LoginCmd
	if len(acct.CustomLogin) > 0 {
		loginArgs = acct.CustomLogin
	}
	profileDir := ""
	var extraEnv []string
	if !acct.UseHostProfile {
		var err error
		profileDir, err = config.EnsureProfileDir(m.dataDir, acct.Backend, acct.Name)
		if err != nil {
			return nil, "", err
		}
		extraEnv = desc.ExpandEnv(profileDir, acct.Name, acct.CustomEnv)
	}
	expanded := make([]string, len(loginArgs))
	for i, s := range loginArgs {
		s = strings.ReplaceAll(s, "{profile}", profileDir)
		s = strings.ReplaceAll(s, "{account}", acct.Name)
		expanded[i] = s
	}
	cmd := exec.Command(expanded[0], expanded[1:]...)
	cmd.Env = sanitizeChildEnv(os.Environ(), extraEnv, acct)
	return cmd, profileDir, nil
}

// TestAccount verifies the CLI binary exists and fetches live models when supported (e.g. `agy models`, `opencode models`).
func (m *Manager) TestAccount(ctx context.Context, acct config.AccountConfig) ([]string, error) {
	desc, ok := m.registry.GetCLI(acct.Backend)
	if !ok {
		return nil, fmt.Errorf("unknown CLI backend %q", acct.Backend)
	}

	bin := desc.Binary
	if len(acct.CustomCommand) > 0 {
		bin = acct.CustomCommand[0]
	}
	if bin == "" {
		return nil, fmt.Errorf("no CLI binary configured for %s", acct.Name)
	}
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("CLI binary %q not found on PATH", bin)
	}

	var extraEnv []string
	if !acct.UseHostProfile {
		profileDir, err := config.EnsureProfileDir(m.dataDir, acct.Backend, acct.Name)
		if err != nil {
			return nil, err
		}
		extraEnv = desc.ExpandEnv(profileDir, acct.Name, acct.CustomEnv)
	}

	// If descriptor supports a models command (e.g. `agy models`, `opencode models`), run it to discover real models.
	if len(desc.ModelsCmd) > 0 {
		tctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(tctx, desc.ModelsCmd[0], desc.ModelsCmd[1:]...)
		cmd.Env = sanitizeChildEnv(os.Environ(), extraEnv, acct)
		out, err := cmd.CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			return nil, classifyCLIError(errors.New(msg))
		}
		outStr := string(out)
		lowerOut := strings.ToLower(outStr)
		if strings.Contains(lowerOut, "not authenticated") || strings.Contains(lowerOut, "not logged in") || strings.Contains(lowerOut, "authentication required") {
			return nil, classifyCLIError(errors.New(strings.TrimSpace(outStr)))
		}
		parsed := parseModelsCLIOutput(outStr)
		if len(parsed) > 0 {
			return parsed, nil
		}
	}

	// Fallback: verify binary runs `--version`.
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(tctx, bin, "--version")
	cmd.Env = sanitizeChildEnv(os.Environ(), extraEnv, acct)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("%s --version failed: %s (%w)", bin, strings.TrimSpace(string(out)), err)
	}
	return acct.Models, nil
}

func (m *Manager) Complete(ctx context.Context, acct config.AccountConfig, req backend.ChatRequest) (*backend.ChatResponse, error) {
	var sb strings.Builder
	var sessionID string
	err := m.Stream(ctx, acct, req, func(ch backend.StreamChunk) error {
		if ch.Delta != "" {
			sb.WriteString(ch.Delta)
		}
		if ch.SessionID != "" {
			sessionID = ch.SessionID
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &backend.ChatResponse{
		ID:           fmt.Sprintf("cli_%d", time.Now().UnixNano()),
		Model:        req.Model,
		Content:      sb.String(),
		FinishReason: "stop",
		ToolsIgnored: req.HasTools,
		SessionID:    sessionID,
	}, nil
}

func (m *Manager) Stream(ctx context.Context, acct config.AccountConfig, req backend.ChatRequest, cb backend.StreamCallback) error {
	desc, ok := m.registry.GetCLI(acct.Backend)
	if !ok {
		return fmt.Errorf("unknown CLI backend %q", acct.Backend)
	}

	var profileDir string
	var extraEnv []string
	if acct.UseHostProfile {
		for k, v := range acct.CustomEnv {
			extraEnv = append(extraEnv, k+"="+v)
		}
	} else {
		var err error
		profileDir, err = config.EnsureProfileDir(m.dataDir, acct.Backend, acct.Name)
		if err != nil {
			return err
		}
		_ = os.MkdirAll(filepath.Join(profileDir, ".config"), 0o700)
		_ = os.MkdirAll(filepath.Join(profileDir, ".gemini"), 0o700)
		extraEnv = desc.ExpandEnv(profileDir, acct.Name, acct.CustomEnv)
	}

	cwd := strings.TrimSpace(req.Cwd)
	if cwd == "" {
		cwd = strings.TrimSpace(acct.DefaultCwd)
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	var existingSID string
	sessMapKey := ""
	if req.SessionKey != "" {
		sessMapKey = acct.ID + ":" + req.SessionKey
		m.mu.Lock()
		existingSID = m.sessions[sessMapKey]
		m.mu.Unlock()
	}

	promptText := formatPrompt(req.Messages, existingSID != "")
	argv, err := buildCLIArgs(desc, acct, req.UpstreamModel, req.ReasoningEffort, promptText, existingSID, cwd, profileDir)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = sanitizeChildEnv(os.Environ(), extraEnv, acct)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return &backend.BackendError{
			Kind:    backend.ErrKindTransient,
			Message: fmt.Sprintf("failed to launch %s: %v", argv[0], err),
			Cause:   err,
		}
	}

	var discoveredSID string
	emittedText := false
	var streamErr error

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		delta, sid, isFinalFallback := parseCLIOutputLine(desc.OutputFormat, line)
		if sid != "" {
			discoveredSID = sid
		}
		// Avoid duplicating text if a final `result` summary repeats already-streamed deltas.
		if isFinalFallback && emittedText {
			continue
		}
		if delta != "" {
			emittedText = true
			if err := cb(backend.StreamChunk{
				Delta:        delta,
				ToolsIgnored: req.HasTools,
				SessionID:    discoveredSID,
			}); err != nil {
				streamErr = err
				_ = cmd.Process.Kill()
				break
			}
		}
	}

	waitErr := cmd.Wait()
	if streamErr != nil {
		return streamErr
	}
	if discoveredSID != "" && sessMapKey != "" {
		m.mu.Lock()
		m.sessions[sessMapKey] = discoveredSID
		m.mu.Unlock()
	}

	if waitErr != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return ctx.Err()
		}
		errMsg := strings.TrimSpace(stderrBuf.String())
		if errMsg == "" {
			errMsg = waitErr.Error()
		}
		return classifyCLIError(fmt.Errorf("%s failed: %s", argv[0], errMsg))
	}

	return cb(backend.StreamChunk{
		FinishReason: "stop",
		ToolsIgnored: req.HasTools,
		SessionID:    discoveredSID,
	})
}

func buildCLIArgs(desc descriptors.CLIDescriptor, acct config.AccountConfig, model, effort, prompt, sessionID, cwd, profileDir string) ([]string, error) {
	if len(acct.CustomCommand) > 0 {
		out := make([]string, 0, len(acct.CustomCommand)+1)
		hasPromptPlaceholder := false
		for _, tok := range acct.CustomCommand {
			if strings.Contains(tok, "{prompt}") {
				hasPromptPlaceholder = true
			}
			tok = strings.ReplaceAll(tok, "{prompt}", prompt)
			tok = strings.ReplaceAll(tok, "{model}", model)
			tok = strings.ReplaceAll(tok, "{effort}", effort)
			tok = strings.ReplaceAll(tok, "{session}", sessionID)
			tok = strings.ReplaceAll(tok, "{profile}", profileDir)
			tok = strings.ReplaceAll(tok, "{cwd}", cwd)
			out = append(out, tok)
		}
		if !hasPromptPlaceholder {
			out = append(out, prompt)
		}
		return out, nil
	}

	if desc.Binary == "" {
		return nil, fmt.Errorf("CLI backend %q has no binary configured", desc.ID)
	}

	args := []string{desc.Binary}

	// Special subcommand resume form for Codex: `codex exec resume <sid> --json --skip-git-repo-check ...`
	if desc.ResumeSubcmd && sessionID != "" {
		args = append(args, "exec", "resume", sessionID, "--json", "--skip-git-repo-check")
	} else {
		args = append(args, desc.BaseArgs...)
		if desc.ResumeFlag != "" && sessionID != "" {
			args = append(args, desc.ResumeFlag, sessionID)
		}
	}

	if desc.CwdFlag != "" && cwd != "" && !desc.ResumeSubcmd {
		args = append(args, desc.CwdFlag, cwd)
	}
	if desc.ModelFlag != "" && model != "" && model != "default" {
		args = append(args, desc.ModelFlag, model)
	}
	if desc.EffortFlag != "" && effort != "" {
		args = append(args, desc.EffortFlag, effort)
	}
	if acct.PermissionMode == config.PermAutoApprove && len(desc.AutoApproveArgs) > 0 {
		args = append(args, desc.AutoApproveArgs...)
	}

	if desc.PromptFlag != "" {
		args = append(args, desc.PromptFlag, prompt)
	} else {
		args = append(args, prompt)
	}
	return args, nil
}

// parseCLIOutputLine extracts streaming text delta and session/conversation ID from CLI NDJSON or text output.
func parseCLIOutputLine(format, line string) (delta string, sessionID string, isFinalFallback bool) {
	if format == "text" {
		return line + "\n", "", false
	}

	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "{") {
		// Non-JSON status/plain text fallback
		return "", "", false
	}

	var obj map[string]any
	if err := json.Unmarshal([]byte(trimmed), &obj); err != nil {
		return "", "", false
	}

	// Extract session/conversation/thread ID across Claude, agy, Codex, OpenCode, Gemini
	for _, k := range []string{"session_id", "sessionId", "sessionID", "conversation_id", "conversationId", "thread_id", "threadId"} {
		if v, ok := obj[k].(string); ok && v != "" {
			sessionID = v
			break
		}
	}

	typ, _ := obj["type"].(string)

	switch format {
	case "codex-jsonl":
		// Codex exec --json events:
		// {"type":"thread.started","thread_id":"..."}
		// {"type":"item.completed","item":{"type":"agent_message","text":"..."}}
		if item, ok := obj["item"].(map[string]any); ok {
			if itemType, _ := item["type"].(string); itemType == "agent_message" || itemType == "message" {
				if t, ok := item["text"].(string); ok && t != "" {
					return t, sessionID, false
				}
			}
		}
		if d, ok := obj["delta"].(string); ok && d != "" {
			return d, sessionID, false
		}
		return "", sessionID, false

	case "opencode-json":
		// OpenCode run --format json: {"type":"text","sessionID":"...","part":{"type":"text","text":"..."}}
		if part, ok := obj["part"].(map[string]any); ok {
			if t, ok := part["text"].(string); ok && t != "" {
				return t, sessionID, false
			}
		}
		if t, ok := obj["text"].(string); ok && t != "" {
			return t, sessionID, false
		}
		return "", sessionID, false

	default: // "stream-json" (Claude Code, agy, Gemini CLI, Cursor)
		// 1. Anthropic/Claude stream_event -> content_block_delta -> delta.text
		if typ == "stream_event" {
			if ev, ok := obj["event"].(map[string]any); ok {
				if d, ok := ev["delta"].(map[string]any); ok {
					if t, ok := d["text"].(string); ok && t != "" {
						return t, sessionID, false
					}
				}
			}
		}
		// 2. Direct delta object: {"type":"content_block_delta","delta":{"text":"..."}} or {"delta":"..."}
		if d, ok := obj["delta"].(map[string]any); ok {
			if t, ok := d["text"].(string); ok && t != "" {
				return t, sessionID, false
			}
			if t, ok := d["content"].(string); ok && t != "" {
				return t, sessionID, false
			}
		}
		if d, ok := obj["delta"].(string); ok && d != "" {
			return d, sessionID, false
		}
		// 3. Assistant message event: {"type":"assistant","message":{"content":[{"type":"text","text":"..."}]}}
		if typ == "assistant" {
			if msg, ok := obj["message"].(map[string]any); ok {
				if blocks, ok := msg["content"].([]any); ok {
					var sb strings.Builder
					for _, rawB := range blocks {
						if b, ok := rawB.(map[string]any); ok {
							if bt, _ := b["type"].(string); bt == "text" {
								if t, _ := b["text"].(string); t != "" {
									sb.WriteString(t)
								}
							}
						}
					}
					if sb.Len() > 0 {
						return sb.String(), sessionID, true
					}
				}
			}
		}
		// 4. Final result summary: {"type":"result","result":"..."}
		if typ == "result" {
			if res, ok := obj["result"].(string); ok && res != "" {
				return res, sessionID, true
			}
		}
		// 5. Generic text/content field (Gemini CLI / agy text chunk)
		if typ == "message" || typ == "text" || typ == "chunk" {
			if t, ok := obj["content"].(string); ok && t != "" {
				return t, sessionID, false
			}
			if t, ok := obj["text"].(string); ok && t != "" {
				return t, sessionID, false
			}
		}
		return "", sessionID, false
	}
}

var modelTokenRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{1,80}$`)

func parseModelsCLIOutput(out string) []string {
	seen := make(map[string]bool)
	var models []string
	for _, rawLine := range strings.Split(out, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "Fetching") || strings.HasPrefix(line, "Available") ||
			strings.HasPrefix(line, "Error") || strings.HasPrefix(line, "Default ") ||
			strings.HasPrefix(line, "You ") || strings.HasPrefix(line, "Note") ||
			strings.HasPrefix(line, "Warning") || strings.HasPrefix(line, "─") {
			continue
		}
		line = strings.TrimLeft(line, "•*- ")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		tok := fields[0]
		if strings.HasSuffix(tok, ":") {
			continue
		}
		if modelTokenRe.MatchString(tok) && !seen[tok] {
			seen[tok] = true
			models = append(models, tok)
		}
	}
	return models
}

func formatPrompt(messages []backend.Message, reusedSession bool) string {
	if len(messages) == 0 {
		return ""
	}
	if reusedSession {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				return messages[i].Content
			}
		}
		return messages[len(messages)-1].Content
	}
	if len(messages) == 1 {
		return messages[0].Content
	}
	var sb strings.Builder
	for _, m := range messages {
		switch m.Role {
		case "system":
			sb.WriteString("[System]\n" + m.Content + "\n\n")
		case "assistant":
			sb.WriteString("[Assistant]\n" + m.Content + "\n\n")
		default:
			sb.WriteString("[User]\n" + m.Content + "\n\n")
		}
	}
	return strings.TrimSpace(sb.String())
}

func classifyCLIError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "restricted") || strings.Contains(lower, "suspended") || strings.Contains(lower, "banned"):
		return &backend.BackendError{Kind: backend.ErrKindRestricted, StatusCode: 403, Message: msg, Cause: err}
	case strings.Contains(lower, "429") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "rate_limit") || strings.Contains(lower, "too many requests") || strings.Contains(lower, "quota") || strings.Contains(lower, "usage limit") || strings.Contains(lower, "resource_exhausted"):
		return &backend.BackendError{
			Kind:       backend.ErrKindRateLimit,
			StatusCode: 429,
			Message:    msg,
			RetryAfter: backend.ParseResetDurationSeconds(msg),
			Cause:      err,
		}
	case strings.Contains(lower, "sign in") || strings.Contains(lower, "log in") || strings.Contains(lower, "not logged in") || strings.Contains(lower, "authentication") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "401"):
		return &backend.BackendError{Kind: backend.ErrKindAuth, StatusCode: 401, Message: msg, Cause: err}
	default:
		return &backend.BackendError{Kind: backend.ErrKindTransient, StatusCode: 502, Message: msg, Cause: err}
	}
}

func sanitizeChildEnv(baseEnv, extraEnv []string, acct config.AccountConfig) []string {
	overrides := make(map[string]string, len(extraEnv)+3)
	for _, kv := range extraEnv {
		if k, v, ok := strings.Cut(kv, "="); ok {
			overrides[k] = v
		}
	}
	overrides["APX_IN_CHILD"] = "1"
	overrides["APX_CHILD_BACKEND"] = acct.Backend
	overrides["APX_CHILD_ACCOUNT"] = acct.ID

	out := make([]string, 0, len(baseEnv)+len(overrides))
	for _, kv := range baseEnv {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if _, replaced := overrides[k]; replaced {
			continue
		}
		switch k {
		case "OPENAI_BASE_URL", "ANTHROPIC_BASE_URL":
			lower := strings.ToLower(v)
			if strings.Contains(lower, "127.0.0.1") || strings.Contains(lower, "localhost") || strings.Contains(lower, "[::1]") {
				continue
			}
		case "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "APX_API_KEY", "ACPX_API_KEY":
			trimmed := strings.TrimSpace(v)
			if strings.HasPrefix(trimmed, "apx_") || strings.HasPrefix(trimmed, "acpx_") {
				continue
			}
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}
