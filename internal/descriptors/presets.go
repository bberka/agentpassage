package descriptors

import "github.com/bberka/agentpassage/internal/config"

// ProviderPreset defines an API-key provider with base URL, auth header style, and default models.
type ProviderPreset struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	BaseURL       string   `json:"base_url"`
	AuthStyle     string   `json:"auth_style"` // bearer, anthropic, none
	DefaultModels []string `json:"default_models"`
	KeyOptional   bool     `json:"key_optional,omitempty"`
}

func BuiltInPresets() []ProviderPreset {
	return []ProviderPreset{
		{
			ID:        "deepseek",
			Name:      "DeepSeek",
			BaseURL:   "https://api.deepseek.com/v1",
			AuthStyle: config.AuthBearer,
			DefaultModels: []string{
				"deepseek-chat",
				"deepseek-reasoner",
			},
		},
		{
			ID:        "openrouter",
			Name:      "OpenRouter",
			BaseURL:   "https://openrouter.ai/api/v1",
			AuthStyle: config.AuthBearer,
			DefaultModels: []string{
				"openai/gpt-4.1",
				"anthropic/claude-sonnet-4",
				"google/gemini-2.5-pro",
			},
		},
		{
			ID:        "openai",
			Name:      "OpenAI",
			BaseURL:   "https://api.openai.com/v1",
			AuthStyle: config.AuthBearer,
			DefaultModels: []string{
				"gpt-4.1",
				"gpt-4.1-mini",
				"o4-mini",
			},
		},
		{
			ID:        "anthropic",
			Name:      "Anthropic",
			BaseURL:   "https://api.anthropic.com/v1",
			AuthStyle: config.AuthAnthropic,
			DefaultModels: []string{
				"claude-sonnet-4-20250514",
				"claude-opus-4-20250514",
				"claude-3-5-haiku-20241022",
			},
		},
		{
			ID:        "google",
			Name:      "Google AI Studio",
			BaseURL:   "https://generativelanguage.googleapis.com/v1beta/openai",
			AuthStyle: config.AuthBearer,
			DefaultModels: []string{
				"gemini-2.5-pro",
				"gemini-2.5-flash",
			},
		},
		{
			ID:        "groq",
			Name:      "Groq",
			BaseURL:   "https://api.groq.com/openai/v1",
			AuthStyle: config.AuthBearer,
			DefaultModels: []string{
				"llama-3.3-70b-versatile",
				"openai/gpt-oss-120b",
			},
		},
		{
			ID:        "together",
			Name:      "Together AI",
			BaseURL:   "https://api.together.xyz/v1",
			AuthStyle: config.AuthBearer,
			DefaultModels: []string{
				"meta-llama/Llama-3.3-70B-Instruct-Turbo",
				"deepseek-ai/DeepSeek-R1",
			},
		},
		{
			ID:        "mistral",
			Name:      "Mistral AI",
			BaseURL:   "https://api.mistral.ai/v1",
			AuthStyle: config.AuthBearer,
			DefaultModels: []string{
				"mistral-large-latest",
				"codestral-latest",
			},
		},
		{
			ID:        "xai",
			Name:      "xAI",
			BaseURL:   "https://api.x.ai/v1",
			AuthStyle: config.AuthBearer,
			DefaultModels: []string{
				"grok-3",
				"grok-3-mini",
			},
		},
		{
			ID:            "comfyui",
			Name:          "ComfyUI (local, needs an OpenAI images add-on)",
			BaseURL:       "http://127.0.0.1:8188/v1",
			AuthStyle:     config.AuthNone,
			KeyOptional:   true,
			DefaultModels: []string{},
		},
		{
			ID:          "ollama",
			Name:        "Ollama (local)",
			BaseURL:     "http://127.0.0.1:11434/v1",
			AuthStyle:   config.AuthNone,
			KeyOptional: true,
			DefaultModels: []string{
				"llama3.2",
				"qwen2.5-coder",
			},
		},
		{
			ID:            "lmstudio",
			Name:          "LM Studio (local)",
			BaseURL:       "http://127.0.0.1:1234/v1",
			AuthStyle:     config.AuthNone,
			KeyOptional:   true,
			DefaultModels: []string{},
		},
		{
			ID:            "llamacpp",
			Name:          "llama.cpp server (local)",
			BaseURL:       "http://127.0.0.1:8080/v1",
			AuthStyle:     config.AuthNone,
			KeyOptional:   true,
			DefaultModels: []string{},
		},
		{
			ID:            "vllm",
			Name:          "vLLM (local)",
			BaseURL:       "http://127.0.0.1:8000/v1",
			AuthStyle:     config.AuthNone,
			KeyOptional:   true,
			DefaultModels: []string{},
		},
		{
			ID:            "custom",
			Name:          "Custom OpenAI-compatible URL",
			BaseURL:       "http://127.0.0.1:8080/v1",
			AuthStyle:     config.AuthBearer,
			DefaultModels: []string{},
		},
	}
}
