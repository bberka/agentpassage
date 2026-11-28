package acpbridge

import "github.com/bberka/agentpassage/internal/skills"

// sendSkills lists the user's and project's skills to the host. Hosts such as
// T3 Code read a command whose name starts with "$" as a skill.
func sendSkills(notify func(string, any), sessionID string, found []skills.Skill) {
	if len(found) == 0 {
		return
	}
	commands := make([]map[string]any, 0, len(found))
	for _, s := range found {
		commands = append(commands, map[string]any{"name": "$" + s.Name, "description": s.Description})
	}
	notify("session/update", map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate":     "available_commands_update",
			"availableCommands": commands,
		},
	})
}
