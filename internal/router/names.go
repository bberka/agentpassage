package router

import (
	"fmt"
	"strings"
)

// brandNames spells backends, agents and model vendors the way their owners do,
// keyed by lowercase ID. Unknown IDs are title-cased.
var brandNames = map[string]string{
	// Agent backends (ACP registry and CLI presets).
	"agoragentic-acp": "Agoragentic", "agy": "AGY", "amp-acp": "Amp", "auggie": "Auggie",
	"autohand": "Autohand Code", "bob": "Bob", "claude": "Claude", "cline": "Cline",
	"codebuddy-code": "CodeBuddy", "codex": "Codex", "cortex-code": "Cortex Code",
	"corust-agent": "Corust", "crow-cli": "Crow", "cursor": "Cursor", "deepagents": "DeepAgents",
	"devin": "Devin", "dimcode": "DimCode", "dirac": "Dirac", "factory-droid": "Factory Droid",
	"fast-agent": "fast-agent", "gemini": "Gemini", "github-copilot-cli": "GitHub Copilot",
	"glm-acp-agent": "GLM Agent", "goose": "Goose", "grok": "xAI", "harn": "Harn", "junie": "Junie",
	"kilo": "Kilo", "kimchi": "Kimchi", "kimi": "Kimi", "kimi-code": "Kimi Code", "kiro": "Kiro",
	"minimax-code": "MiniMax Code", "minion-code": "Minion Code", "mistral-vibe": "Mistral Vibe",
	"muse-code": "Muse Code", "nova": "Nova", "opencode": "OpenCode", "pi": "Pi",
	"poolside": "Poolside", "qoder": "Qoder", "qwen-code": "Qwen Code", "sigit": "siGit Code",
	"stakpak": "Stakpak", "vtcode": "VT Code",

	// API and local providers, and the vendors that prefix their model IDs.
	"ai21": "AI21", "alibaba": "Alibaba", "amazon": "Amazon", "amazon-bedrock": "Amazon Bedrock",
	"anthropic": "Anthropic", "azure": "Azure", "baidu": "Baidu", "bytedance": "ByteDance",
	"cerebras": "Cerebras", "cohere": "Cohere", "deepinfra": "DeepInfra", "deepseek": "DeepSeek",
	"deepseek-ai": "DeepSeek", "fireworks": "Fireworks", "fireworks-ai": "Fireworks",
	"github-copilot": "GitHub Copilot", "github-models": "GitHub Models", "google": "Google",
	"google-vertex": "Google Vertex", "groq": "Groq", "huggingface": "Hugging Face",
	"llamacpp": "llama.cpp", "lmstudio": "LM Studio", "meta": "Meta", "meta-llama": "Meta",
	"microsoft": "Microsoft", "minimax": "MiniMax", "mistral": "Mistral", "mistralai": "Mistral",
	"moonshot": "Moonshot AI", "moonshotai": "Moonshot AI", "nvidia": "NVIDIA", "ollama": "Ollama",
	"ollama-cloud": "Ollama Cloud", "openai": "OpenAI", "openrouter": "OpenRouter",
	"perplexity": "Perplexity", "qwen": "Qwen", "tencent": "Tencent", "together": "Together AI",
	"togetherai": "Together AI", "venice": "Venice", "vercel": "Vercel", "vllm": "vLLM",
	"x-ai": "xAI", "xai": "xAI", "xiaomi": "Xiaomi", "z-ai": "Z.AI", "zai": "Z.AI",
	"zai-org": "Z.AI", "zhipuai": "Zhipu AI",
}

// modelWords spells model name parts that plain title-casing gets wrong.
var modelWords = map[string]string{
	"ai": "AI", "api": "API", "deepseek": "DeepSeek", "glm": "GLM", "llm": "LLM",
	"mimo": "MiMo", "minimax": "MiniMax", "o1": "o1", "o3": "o3", "o4": "o4",
	"oss": "OSS", "vl": "VL",
}

// Label names a model for client pickers as "Provider Model", so the same model
// served by two providers is told apart. Nested IDs such
// as OpenCode's "ollama/qwen3:8b" keep their vendor: "OpenCode Ollama Qwen3 8B".
// A name the user set is used as written.
func Label(m ModelEntry) string {
	if m.CustomName {
		return m.DisplayName
	}
	base := cleanModelDisplayName(m)
	if base == "" {
		return m.ID
	}
	// Codex reports its GPT models without the family name ("6 Luna").
	if strings.HasPrefix(m.Upstream, "gpt-") && base[0] >= '0' && base[0] <= '9' {
		base = "GPT-" + base
	}
	base = joinBrand(providerLabel(m.Backend), base)
	if m.AccountName != "" {
		return fmt.Sprintf("%s (%s)", base, m.AccountName)
	}
	return base
}

// joinBrand prefixes name with brand unless name already starts with it.
func joinBrand(brand, name string) string {
	if brand == "" {
		return name
	}
	lower, prefix := strings.ToLower(name), strings.ToLower(brand)
	if lower == prefix || strings.HasPrefix(lower, prefix+" ") || strings.HasPrefix(lower, prefix+"-") {
		return name
	}
	return brand + " " + name
}

func providerLabel(backendID string) string {
	if name, ok := brandNames[strings.ToLower(backendID)]; ok {
		return name
	}
	return prettifyModelID(backendID)
}

// cleanModelDisplayName is the model's own name, without the backend.
func cleanModelDisplayName(m ModelEntry) string {
	raw := strings.TrimSpace(m.Upstream)
	if raw == "" {
		raw = strings.TrimSpace(m.ID)
		if idx := strings.LastIndex(raw, "/"); idx >= 0 && idx+1 < len(raw) {
			raw = raw[idx+1:]
		}
	}
	name := strings.TrimSpace(m.DisplayName)
	if strings.Contains(raw, "/") {
		return nestedModelName(raw, name)
	}
	if name != "" {
		return name
	}
	return prettifyModelID(raw)
}

// nestedModelName names a "vendor/.../model" ID. label is the agent's own name
// for it, which may repeat the vendor path or give the model name alone.
func nestedModelName(id, label string) string {
	parts := strings.Split(id, "/")
	last := parts[len(parts)-1]
	model := label[strings.LastIndex(label, "/")+1:]
	if model == "" || model == last {
		model = prettifyModelID(last)
	}
	for i := len(parts) - 2; i >= 0; i-- {
		vendor, ok := brandNames[strings.ToLower(parts[i])]
		if !ok {
			vendor = parts[i]
		}
		model = joinBrand(vendor, model)
	}
	return model
}

func prettifyModelID(raw string) string {
	parts := strings.FieldsFunc(strings.TrimSpace(raw), func(r rune) bool {
		return r == '-' || r == '_' || r == ':'
	})
	isSingleDigit := func(s string) bool {
		return len(s) == 1 && s[0] >= '0' && s[0] <= '9'
	}
	var out []string
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		lower := strings.ToLower(p)
		// Join adjacent single-digit version numbers like "5", "5" -> "5.5".
		if isSingleDigit(p) && i+1 < len(parts) && isSingleDigit(parts[i+1]) {
			out = append(out, p+"."+parts[i+1])
			i++
			continue
		}
		switch {
		case lower == "gpt":
			// Combine "gpt", "6" -> "GPT-6".
			if i+1 < len(parts) && parts[i+1][0] >= '0' && parts[i+1][0] <= '9' {
				out = append(out, "GPT-"+parts[i+1])
				i++
			} else {
				out = append(out, "GPT")
			}
		case modelWords[lower] != "":
			out = append(out, modelWords[lower])
		case len(lower) >= 2 && lower[len(lower)-1] == 'b' && lower[0] >= '0' && lower[0] <= '9':
			out = append(out, strings.ToUpper(lower))
		case p[0] >= 'a' && p[0] <= 'z':
			out = append(out, strings.ToUpper(p[:1])+p[1:])
		default:
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}
