package gateway

import (
	"encoding/json"
	"fmt"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/privacy"
)

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func parseOpenAIToolCalls(in []openAIToolCall) []backend.ToolCall {
	var out []backend.ToolCall
	for _, c := range in {
		out = append(out, backend.ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
	}
	return out
}

func parseOpenAITools(tools []json.RawMessage, choice json.RawMessage) ([]backend.Tool, backend.ToolChoice) {
	var out []backend.Tool
	for _, raw := range tools {
		var t struct {
			Function struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
			} `json:"function"`
		}
		if json.Unmarshal(raw, &t) == nil && t.Function.Name != "" {
			out = append(out, backend.Tool{Name: t.Function.Name, Description: t.Function.Description, Schema: t.Function.Parameters})
		}
	}
	var mode string
	if json.Unmarshal(choice, &mode) == nil {
		return out, backend.ToolChoice{Mode: mode}
	}
	var named struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(choice, &named) == nil && named.Function.Name != "" {
		return out, backend.ToolChoice{Mode: backend.ToolChoiceNamed, Name: named.Function.Name}
	}
	return out, backend.ToolChoice{}
}

// parseAnthropicTools skips Anthropic's own server tools, which have no schema
// another provider could use.
func parseAnthropicTools(tools []json.RawMessage, choice json.RawMessage) ([]backend.Tool, backend.ToolChoice) {
	var out []backend.Tool
	for _, raw := range tools {
		var t struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
		}
		if json.Unmarshal(raw, &t) == nil && t.Name != "" && len(t.InputSchema) > 0 {
			out = append(out, backend.Tool{Name: t.Name, Description: t.Description, Schema: t.InputSchema})
		}
	}
	var c struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(choice, &c)
	switch c.Type {
	case "auto", "none":
		return out, backend.ToolChoice{Mode: c.Type}
	case "any":
		return out, backend.ToolChoice{Mode: backend.ToolChoiceRequired}
	case "tool":
		return out, backend.ToolChoice{Mode: backend.ToolChoiceNamed, Name: c.Name}
	}
	return out, backend.ToolChoice{}
}

// parseAnthropicMessage splits a Messages API message into apx messages: each
// tool result becomes its own "tool" message, ahead of any other content.
func parseAnthropicMessage(role string, content json.RawMessage) []backend.Message {
	var blocks []json.RawMessage
	if json.Unmarshal(content, &blocks) != nil {
		text, atts := parseContent(content, "")
		return []backend.Message{{Role: role, Content: text, Attachments: atts}}
	}
	var out []backend.Message
	var calls []backend.ToolCall
	rest := make([]json.RawMessage, 0, len(blocks))
	for _, raw := range blocks {
		var b struct {
			Type      string          `json:"type"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
		}
		_ = json.Unmarshal(raw, &b)
		switch b.Type {
		case "tool_use":
			calls = append(calls, backend.ToolCall{ID: b.ID, Name: b.Name, Arguments: string(b.Input)})
		case "tool_result":
			text, atts := parseContent(b.Content, "\n")
			out = append(out, backend.Message{Role: "tool", ToolCallID: b.ToolUseID, Content: text, Attachments: atts})
		default:
			rest = append(rest, raw)
		}
	}
	if len(rest) == 0 && len(calls) == 0 && len(out) > 0 {
		return out
	}
	joined, _ := json.Marshal(rest)
	text, atts := parseContent(joined, "")
	return append(out, backend.Message{Role: role, Content: text, Attachments: atts, ToolCalls: calls})
}

// openAIToolCallsOut renders a reply's tool calls for a chat completion.
func openAIToolCallsOut(calls []backend.ToolCall, priv *privacy.Session) []openAIToolCall {
	out := make([]openAIToolCall, len(calls))
	for i, c := range calls {
		out[i] = openAIToolCall{ID: toolCallID(c.ID, i), Type: "function"}
		out[i].Function.Name, out[i].Function.Arguments = c.Name, priv.RestoreJSON(c.Arguments)
	}
	return out
}

// toolCallID makes up an ID for providers that send none, since the client
// answers a call by its ID.
func toolCallID(id string, index int) string {
	if id == "" {
		return fmt.Sprintf("call_apx_%d", index)
	}
	return id
}

// toolStream renders streamed tool calls as chat completion deltas, restoring
// redacted values that arrive split across pieces.
type toolStream struct {
	priv      *privacy.Session
	restorers map[int]*privacy.StreamRestorer
}

func (ts *toolStream) push(pieces []backend.ToolCallDelta) []map[string]any {
	var out []map[string]any
	for _, d := range pieces {
		if ts.restorers == nil {
			ts.restorers = make(map[int]*privacy.StreamRestorer)
		}
		sr, started := ts.restorers[d.Index]
		if !started {
			sr = ts.priv.NewJSONRestorer()
			ts.restorers[d.Index] = sr
		}
		fn := map[string]any{"arguments": sr.Push(d.Arguments)}
		call := map[string]any{"index": d.Index, "function": fn}
		if d.Name != "" {
			fn["name"] = d.Name
		}
		if !started {
			call["id"], call["type"] = toolCallID(d.ID, d.Index), "function"
		}
		out = append(out, call)
	}
	return out
}

// flush returns the argument text still held back.
func (ts *toolStream) flush() []map[string]any {
	var out []map[string]any
	for index := range len(ts.restorers) {
		if tail := ts.restorers[index].Flush(); tail != "" {
			out = append(out, map[string]any{"index": index, "function": map[string]any{"arguments": tail}})
		}
	}
	return out
}
