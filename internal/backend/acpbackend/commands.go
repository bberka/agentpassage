package acpbackend

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
)

// Prepare opens the session the conversation's next turn will use, without
// sending a prompt, so the agent's commands reach req.OnCommands before then.
func (m *Manager) Prepare(ctx context.Context, acct config.AccountConfig, req backend.ChatRequest) error {
	m.DropPrepared(req.SessionKey, acct.ID)
	m.mu.Lock()
	running := m.workers[acct.ID]
	m.mu.Unlock()
	if running != nil {
		running.sessMu.Lock()
		current := running.sessions[req.SessionKey]
		busy := current != nil && current.busy
		running.sessMu.Unlock()
		if busy {
			running.listenCommands(current.id, req.OnCommands)
			return nil
		}
	}
	w, sess, _, err := m.openSession(ctx, acct, req)
	if err != nil {
		return err
	}
	w.sessMu.Lock()
	sess.busy = false
	w.sessMu.Unlock()
	return nil
}

// DropPrepared closes the conversation's sessions that never ran a turn, except
// on keepAccount. They are left over when the host switches model.
func (m *Manager) DropPrepared(sessionKey, keepAccount string) {
	m.mu.Lock()
	var others []*worker
	for id, w := range m.workers {
		if id != keepAccount {
			others = append(others, w)
		}
	}
	m.mu.Unlock()
	for _, w := range others {
		w.sessMu.Lock()
		sess := w.sessions[sessionKey]
		unused := sess != nil && sess.fresh && !sess.busy
		if unused {
			delete(w.sessions, sessionKey)
		}
		w.sessMu.Unlock()
		if unused {
			w.closeSession(sess)
		}
	}
}

// noteCommands keeps a session's command list from a session/update. Agents
// send it right after session/new, often before anything listens.
func (w *worker) noteCommands(sessionID string, update json.RawMessage) {
	if !bytes.Contains(update, []byte("available_commands_update")) {
		return
	}
	var u struct {
		SessionUpdate     string          `json:"sessionUpdate"`
		AvailableCommands json.RawMessage `json:"availableCommands"`
	}
	if json.Unmarshal(update, &u) != nil || u.SessionUpdate != "available_commands_update" {
		return
	}
	w.cmdMu.Lock()
	defer w.cmdMu.Unlock()
	w.commands[sessionID] = u.AvailableCommands
	if fn := w.listeners[sessionID]; fn != nil {
		fn(u.AvailableCommands)
	}
}

// listenCommands sends a session's command list to fn, now if it is known and
// again whenever the agent changes it.
func (w *worker) listenCommands(sessionID string, fn func(json.RawMessage)) {
	if fn == nil {
		return
	}
	w.cmdMu.Lock()
	defer w.cmdMu.Unlock()
	w.listeners[sessionID] = fn
	if list, ok := w.commands[sessionID]; ok {
		fn(list)
	}
}

func (w *worker) forgetCommands(sessionID string) {
	w.cmdMu.Lock()
	defer w.cmdMu.Unlock()
	delete(w.commands, sessionID)
	delete(w.listeners, sessionID)
}
