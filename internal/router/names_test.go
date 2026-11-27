package router

import (
	"testing"

	"github.com/bberka/agentpassage/internal/config"
)

func TestLabel(t *testing.T) {
	for want, m := range map[string]ModelEntry{
		"AGY Claude Sonnet 5.5":    {Backend: "agy", DisplayName: "Claude Sonnet 5.5"},
		"Claude Sonnet 5.5":        {Backend: "claude", DisplayName: "Sonnet 5.5"},
		"Codex GPT-6 Luna":         {Backend: "codex", Upstream: "gpt-6-luna", DisplayName: "6 Luna"},
		"Codex GPT-5.6 Terra (bx)": {Backend: "codex", Upstream: "gpt-5.6-terra", DisplayName: "5.6 Terra", AccountName: "bx"},
		"xAI Grok 4.7":             {Backend: "grok", Upstream: "grok-4.7", DisplayName: "Grok 4.7"},
		"DeepSeek Flash":           {Backend: "deepseek", Upstream: "deepseek-flash"},
		"Z.AI GLM 4.6":             {Backend: "zai", Upstream: "glm-4.6"},
		"OpenCode Big Pickle":      {Backend: "opencode", Upstream: "opencode/big-pickle", DisplayName: "opencode/Big Pickle"},
		"OpenCode Ollama Qwen3 8B": {Backend: "opencode", Upstream: "ollama/qwen3:8b", DisplayName: "ollama/qwen3:8b"},
		"OpenRouter Z.AI GLM 4.6":  {Backend: "openrouter", Upstream: "z-ai/glm-4.6"},
		"OpenRouter DeepSeek V3":   {Backend: "openrouter", Upstream: "deepseek/deepseek-v3"},
		"My Backend o3 Mini":       {Backend: "my-backend", Upstream: "o3-mini"},
		"AGY Gemini 4 Neon (zdk)":  {Backend: "agy", Upstream: "gemini-4-neon", AccountName: "zdk"},
	} {
		if got := Label(m); got != want {
			t.Errorf("Label(%+v) = %q, want %q", m, got, want)
		}
	}
}

func TestUserModelNameAndAlias(t *testing.T) {
	r := New(&config.Config{
		Accounts: []config.AccountConfig{{
			ID: "claude-main", Name: "main", Backend: "claude", Kind: config.KindACP, Enabled: true,
			Models: []string{"opus", "sonnet"}, ModelLabels: map[string]string{"opus": "Opus 5.5", "sonnet": "Sonnet 5.5"},
		}},
		Models: []config.ModelOverride{
			{ID: "claude/opus", DisplayName: "Big Brain"},
			{ID: "claude/sonnet", Alias: "fast"},
		},
	})
	got := map[string]string{}
	for _, m := range r.ListModels(false) {
		got[m.ID] = Label(m)
	}
	for id, want := range map[string]string{"claude/opus": "Big Brain", "claude/sonnet": "Claude Sonnet 5.5", "fast": "Claude Sonnet 5.5"} {
		if got[id] != want {
			t.Errorf("name of %s = %q, want %q", id, got[id], want)
		}
	}
}
