package acpbackend

import (
	"encoding/json"

	"github.com/bberka/agentpassage/internal/hostmcp"
)

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type initializeParams struct {
	ProtocolVersion    int            `json:"protocolVersion"`
	ClientInfo         clientInfo     `json:"clientInfo"`
	ClientCapabilities map[string]any `json:"clientCapabilities"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type initializeResult struct {
	ProtocolVersion int `json:"protocolVersion"`
	AgentInfo       struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"agentInfo"`
	AgentCapabilities struct {
		PromptCapabilities promptCapabilities   `json:"promptCapabilities"`
		MCPCapabilities    hostmcp.Capabilities `json:"mcpCapabilities"`
	} `json:"agentCapabilities"`
}

// promptCapabilities lists the prompt content an agent accepts beyond text
// and resource links.
type promptCapabilities struct {
	Image           bool `json:"image"`
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
}

type sessionNewParams struct {
	Cwd        string `json:"cwd"`
	MCPServers []any  `json:"mcpServers"`
}

type configOptionValue struct {
	Value   string              `json:"value"`
	ID      string              `json:"id"`
	Name    string              `json:"name"`
	Group   string              `json:"group"`
	Options []configOptionValue `json:"options,omitempty"`
}

type configOption struct {
	ID           string              `json:"id"`
	Category     string              `json:"category"`
	CurrentValue string              `json:"currentValue"`
	Options      []configOptionValue `json:"options"`
}

type sessionNewResult struct {
	SessionID     string         `json:"sessionId"`
	ConfigOptions []configOption `json:"configOptions,omitempty"`
	Models        *struct {
		CurrentModelID  string `json:"currentModelId"`
		AvailableModels []struct {
			ModelID string `json:"modelId"`
			ID      string `json:"id"`
			Name    string `json:"name"`
		} `json:"availableModels"`
	} `json:"models,omitempty"`
}

type setConfigOptionParams struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Value     string `json:"value"`
}

type setModelParams struct {
	SessionID string `json:"sessionId"`
	ModelID   string `json:"modelId"`
}

type promptBlock struct {
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	Data     string            `json:"data,omitempty"`
	MimeType string            `json:"mimeType,omitempty"`
	URI      string            `json:"uri,omitempty"`
	Name     string            `json:"name,omitempty"`
	Resource *embeddedResource `json:"resource,omitempty"`
}

type embeddedResource struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text,omitempty"`
	Blob     string `json:"blob,omitempty"`
}

type sessionPromptParams struct {
	SessionID string        `json:"sessionId"`
	Prompt    []promptBlock `json:"prompt"`
}

type sessionPromptResult struct {
	StopReason string `json:"stopReason"`
	// Usage is reported by agents such as claude-agent-acp; inputTokens
	// excludes the cached input.
	Usage *struct {
		InputTokens       int `json:"inputTokens"`
		OutputTokens      int `json:"outputTokens"`
		CachedReadTokens  int `json:"cachedReadTokens"`
		CachedWriteTokens int `json:"cachedWriteTokens"`
	} `json:"usage,omitempty"`
}

type sessionCancelParams struct {
	SessionID string `json:"sessionId"`
}

type sessionUpdateParams struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

type permissionParams struct {
	SessionID string          `json:"sessionId"`
	ToolCall  json.RawMessage `json:"toolCall"`
	Options   []struct {
		OptionID string `json:"optionId"`
		ID       string `json:"id"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
	} `json:"options"`
}

type permissionToolCallMeta struct {
	ToolCallID string          `json:"toolCallId"`
	Title      string          `json:"title"`
	Name       string          `json:"name"`
	Kind       string          `json:"kind"`
	Arguments  json.RawMessage `json:"arguments"`
	RawInput   json.RawMessage `json:"rawInput"`
}

type permissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

type permissionResult struct {
	Outcome permissionOutcome `json:"outcome"`
}
