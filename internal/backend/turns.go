package backend

import "sync"

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
