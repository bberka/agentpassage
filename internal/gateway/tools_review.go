package gateway

import (
	"os"

	"github.com/bberka/agentpassage/internal/alerts"
	"github.com/bberka/agentpassage/internal/sharing"
	"github.com/bberka/agentpassage/internal/skills"
)

// SharedTools lists the MCP servers and skills of the configured agents with
// the user's choice for each. cwd adds a project's own.
func (s *Server) SharedTools(cwd string) (sharing.Choices, []sharing.Item, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return sharing.Choices{}, nil, err
	}
	accounts := s.Config().Accounts
	choices, err := sharing.Open(s.DataDir(), home, accounts)
	if err != nil {
		return choices, nil, err
	}
	items, err := sharing.List(choices, home, cwd, sharing.Agents(accounts))
	return choices, items, err
}

// AnnounceNewTools notifies once about each MCP server that waits for approval.
func (s *Server) AnnounceNewTools(cwd string) {
	choices, items, err := s.SharedTools(cwd)
	if err != nil && s.logs != nil {
		s.logs.Warn("tools", err.Error())
	}
	if n := sharing.Unannounced(s.DataDir(), choices, items); n > 0 && s.alerts != nil {
		s.alerts.Notify("tools_review", alerts.Event{
			Kind:     "tools_review",
			Severity: "info",
			Title:    sharing.PendingText(n),
			Message:  "Review them in apx",
		})
	}
}

// UserSkills are the skills apx lists to a host for a session in cwd.
func (s *Server) UserSkills(cwd string) []skills.Skill {
	home, _ := os.UserHomeDir()
	choices, _ := sharing.Load(s.DataDir())
	return choices.Skills(skills.Discover(home, cwd), cwd)
}
