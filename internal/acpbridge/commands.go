package acpbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/skills"
)

// sendCommands lists to the host the agent's own slash commands as the agent
// sent them, followed by the user's and project's skills. Hosts such as T3 Code
// read a command whose name starts with "$" as a skill.
func sendCommands(notify func(string, any), sessionID string, native []map[string]any, found []skills.Skill) {
	commands := make([]map[string]any, 0, len(native)+len(found))
	taken := make(map[any]bool, len(native))
	for _, c := range native {
		commands = append(commands, c)
		taken[c["name"]] = true
	}
	for _, s := range found {
		if name := "$" + s.Name; !taken[name] {
			commands = append(commands, map[string]any{"name": name, "description": s.Description})
		}
	}
	notify("session/update", map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate":     "available_commands_update",
			"availableCommands": commands,
		},
	})
}

// commandReceiver passes the agent's command lists for bs to the host until the
// session switches model.
func (s *Server) commandReceiver(bs *bridgeSession, notify func(string, any)) func(json.RawMessage) {
	bs.cmdMu.Lock()
	defer bs.cmdMu.Unlock()
	model := bs.cmdModel
	return func(raw json.RawMessage) {
		var list []map[string]any
		if json.Unmarshal(raw, &list) != nil {
			return
		}
		s.sessMu.Lock()
		cwd := bs.cwd
		s.sessMu.Unlock()
		bs.cmdMu.Lock()
		defer bs.cmdMu.Unlock()
		if model != bs.cmdModel {
			return
		}
		bs.commands = list
		sendCommands(notify, bs.id, list, s.gw.UserSkills(cwd))
	}
}

// prepareSession opens the upstream session for the current model in the
// background, so the host can offer the agent's commands before the first
// prompt. A prompt waits for it.
func (s *Server) prepareSession(ctx context.Context, wg *sync.WaitGroup, bs *bridgeSession, notify func(string, any)) {
	done := make(chan struct{})
	s.sessMu.Lock()
	previous := bs.prepared
	bs.prepared = done
	req := backend.ChatRequest{
		Model:           bs.model,
		ReasoningEffort: bs.effort,
		MCPServers:      bs.mcpServers,
		HostTools:       s.hostTools(),
		Stream:          true,
		SessionKey:      bs.id,
		Cwd:             bs.cwd,
	}
	s.sessMu.Unlock()

	// Take the left model's commands off the host's list.
	bs.cmdMu.Lock()
	bs.cmdModel++
	if len(bs.commands) > 0 {
		bs.commands = nil
		sendCommands(notify, bs.id, nil, s.gw.UserSkills(req.Cwd))
	}
	bs.cmdMu.Unlock()
	req.OnCommands = s.commandReceiver(bs, notify)

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(done)
		if previous != nil {
			<-previous
		}
		if req.Model == "" || ctx.Err() != nil {
			return
		}
		prepareCtx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if err := s.gw.PrepareAgentSession(prepareCtx, req); err != nil && ctx.Err() == nil {
			if logs := s.gw.Logs(); logs != nil {
				logs.Warn("acp", fmt.Sprintf("open session for %s ahead of the first prompt: %v", req.Model, err))
			}
		}
	}()
}
