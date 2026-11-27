package backend

import "testing"

func TestContinueSession(t *testing.T) {
	u := func(s string) Message { return Message{Role: "user", Content: s} }
	a := Message{Role: "assistant", Content: "ok"}
	first, _ := ContinueSession("", ChatRequest{Messages: []Message{u("one")}})
	for name, tc := range map[string]struct {
		req  ChatRequest
		want bool
	}{
		"next turn with history":    {ChatRequest{Messages: []Message{u("one"), a, u("two")}}, true},
		"newest message only":       {ChatRequest{Messages: []Message{u("two")}}, true},
		"another conversation":      {ChatRequest{Messages: []Message{u("other"), a, u("two")}}, false},
		"edited history":            {ChatRequest{Messages: []Message{u("one"), u("added"), a, u("two")}}, false},
		"new conversation per user": {ChatRequest{Messages: []Message{u("two")}, SharedSession: true}, false},
		"same user, next turn":      {ChatRequest{Messages: []Message{u("one"), a, u("two")}, SharedSession: true}, true},
	} {
		if _, got := ContinueSession(first, tc.req); got != tc.want {
			t.Errorf("%s: continues=%v, want %v", name, got, tc.want)
		}
	}

	// Turns sent one at a time add up to the same history as sending it whole.
	second, _ := ContinueSession(first, ChatRequest{Messages: []Message{u("two")}})
	if _, ok := ContinueSession(second, ChatRequest{Messages: []Message{u("one"), a, u("two"), a, u("three")}}); !ok {
		t.Error("full history should continue a session fed turn by turn")
	}
}
