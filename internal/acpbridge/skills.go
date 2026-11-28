package acpbridge

import "github.com/bberka/agentpassage/internal/skills"

// sendSkills lists the user's and project's skills to the host before the
// agent's own commands are known.
func sendSkills(notify func(string, any), sessionID string, found []skills.Skill) {
	if len(found) > 0 {
		sendCommands(notify, sessionID, nil, found)
	}
}
