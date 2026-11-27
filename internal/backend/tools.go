package backend

import "encoding/json"

// Tool calls are passed between the client and API-key accounts. apx never
// runs them. ACP and CLI agents use their own tools and ignore these.

// Tool is a function the client offers the model.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage // JSON Schema of the arguments
}

// Tool choice modes. The empty mode leaves the choice to the provider.
const (
	ToolChoiceAuto     = "auto"
	ToolChoiceNone     = "none"
	ToolChoiceRequired = "required"
	ToolChoiceNamed    = "named" // the tool in ToolChoice.Name
)

type ToolChoice struct {
	Mode string
	Name string
}

// ToolCall is a call the model made.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // a JSON object
}

// ToolCallDelta is one streamed piece of a ToolCall. The first piece of a call
// carries its ID and name.
type ToolCallDelta struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

// MergeToolCall adds a streamed piece to the calls collected so far.
func MergeToolCall(calls []ToolCall, d ToolCallDelta) []ToolCall {
	if d.Index < 0 || d.Index > len(calls) {
		d.Index = len(calls)
	}
	if d.Index == len(calls) {
		calls = append(calls, ToolCall{})
	}
	c := &calls[d.Index]
	if d.ID != "" {
		c.ID = d.ID
	}
	if d.Name != "" {
		c.Name = d.Name
	}
	c.Arguments += d.Arguments
	return calls
}

// ArgumentsObject returns the arguments as JSON, or an empty object when the
// model sent something that isn't.
func (c ToolCall) ArgumentsObject() json.RawMessage {
	if json.Valid([]byte(c.Arguments)) {
		return json.RawMessage(c.Arguments)
	}
	return json.RawMessage("{}")
}
