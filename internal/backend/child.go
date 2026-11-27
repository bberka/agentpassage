package backend

import (
	"strings"

	"github.com/bberka/agentpassage/internal/config"
)

// FormatPrompt renders a conversation as one text prompt for an agent.
func FormatPrompt(messages []Message, reusedSession bool) string {
	if len(messages) == 0 {
		return ""
	}
	if reusedSession {
		// The session already has the earlier turns; send the latest user message.
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

// SanitizeChildEnv builds a child agent's environment. It drops inherited
// variables that point back at the apx gateway (the same CLI can be both a
// connected client and an upstream backend, which would loop), applies extraEnv
// over baseEnv, and tags the process with APX_IN_CHILD.
func SanitizeChildEnv(baseEnv, extraEnv []string, acct config.AccountConfig) []string {
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
