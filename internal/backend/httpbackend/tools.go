package httpbackend

import (
	"encoding/json"

	"github.com/bberka/agentpassage/internal/backend"
)

type openAIToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func openAIToolCalls(calls []backend.ToolCall) []openAIToolCall {
	out := make([]openAIToolCall, len(calls))
	for i, c := range calls {
		out[i] = openAIToolCall{ID: c.ID, Type: "function"}
		out[i].Function.Name, out[i].Function.Arguments = c.Name, c.Arguments
	}
	return out
}

func schemaOrEmpty(schema json.RawMessage) json.RawMessage {
	if len(schema) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return schema
}

func addOpenAITools(payload map[string]any, req backend.ChatRequest) {
	if len(req.Tools) == 0 {
		return
	}
	tools := make([]map[string]any, len(req.Tools))
	for i, t := range req.Tools {
		tools[i] = map[string]any{"type": "function", "function": map[string]any{
			"name": t.Name, "description": t.Description, "parameters": schemaOrEmpty(t.Schema),
		}}
	}
	payload["tools"] = tools
	switch req.ToolChoice.Mode {
	case "":
	case backend.ToolChoiceNamed:
		payload["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": req.ToolChoice.Name}}
	default:
		payload["tool_choice"] = req.ToolChoice.Mode
	}
}

func addAnthropicTools(payload map[string]any, req backend.ChatRequest) {
	if len(req.Tools) == 0 {
		return
	}
	tools := make([]map[string]any, len(req.Tools))
	for i, t := range req.Tools {
		tools[i] = map[string]any{"name": t.Name, "description": t.Description, "input_schema": schemaOrEmpty(t.Schema)}
	}
	payload["tools"] = tools
	switch req.ToolChoice.Mode {
	case backend.ToolChoiceAuto, backend.ToolChoiceNone:
		payload["tool_choice"] = map[string]any{"type": req.ToolChoice.Mode}
	case backend.ToolChoiceRequired:
		payload["tool_choice"] = map[string]any{"type": "any"}
	case backend.ToolChoiceNamed:
		payload["tool_choice"] = map[string]any{"type": "tool", "name": req.ToolChoice.Name}
	}
}

// anthropicMessages renders the conversation for the Messages API, where tool
// results are blocks of a user message. System messages are left out.
func anthropicMessages(msgs []backend.Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	var results []map[string]any // tool results waiting for their user message
	flush := func() {
		if len(results) > 0 {
			out = append(out, map[string]any{"role": "user", "content": results})
			results = nil
		}
	}
	for _, m := range msgs {
		switch m.Role {
		case "system":
		case "tool":
			results = append(results, map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": anthropicContent(m)})
		case "assistant":
			flush()
			content := anthropicContent(m)
			if len(m.ToolCalls) > 0 {
				var blocks []map[string]any
				if m.Content != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": m.Content})
				}
				for _, c := range m.ToolCalls {
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": c.ArgumentsObject()})
				}
				content = blocks
			}
			out = append(out, map[string]any{"role": "assistant", "content": content})
		default:
			flush()
			out = append(out, map[string]any{"role": "user", "content": anthropicContent(m)})
		}
	}
	flush()
	return out
}
