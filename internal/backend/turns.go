package backend

import (
	"crypto/sha256"
	"fmt"
	"sync"
)

// lastAnswered maps a session key to the account that last answered it. It is
// shared by all backends because one conversation can be routed across them.
var lastAnswered sync.Map

// RecordAnswer notes that accountID answered a turn of the conversation.
func RecordAnswer(sessionKey, accountID string) {
	if sessionKey != "" {
		lastAnswered.Store(sessionKey, accountID)
	}
}

// AnsweredElsewhere reports whether another account answered the conversation
// last, so an upstream session cached for accountID has missed turns.
func AnsweredElsewhere(sessionKey, accountID string) bool {
	last, ok := lastAnswered.Load(sessionKey)
	return ok && last != accountID
}

// ContinueSession reports whether an upstream session that has been sent the
// turns summed up by stored may answer req with only its newest user message,
// and returns the sum to store once it has.
//
// The earlier user messages in req must be the ones the session saw. A request
// carrying the newest message alone also continues a conversation its caller
// named, but not one named only after the user (SharedSession).
func ContinueSession(stored string, req ChatRequest) (next string, ok bool) {
	var users []string
	bare := true
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			users = append(users, m.Content)
		case "assistant":
			bare = false
		}
	}
	if len(users) == 0 {
		return stored, !req.SharedSession
	}
	earlier := ""
	for _, u := range users[:len(users)-1] {
		earlier = addTurn(earlier, u)
	}
	newest := users[len(users)-1]
	if bare && len(users) == 1 && !req.SharedSession {
		return addTurn(stored, newest), true
	}
	return addTurn(earlier, newest), stored == earlier
}

func addTurn(sum, message string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(sum+"\x00"+message)))
}
