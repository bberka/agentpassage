package gateway

import (
	"fmt"
	"slices"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/router"
)

// allowedCandidates drops the agent (ACP and CLI) accounts from ids when the
// caller's key may not run agents. A nil principal is a local caller.
func (s *Server) allowedCandidates(p *authPrincipal, ids []string) []string {
	if p == nil || p.Client == nil || p.Client.AllowsAgents() {
		return ids
	}
	cfg := s.Config()
	return slices.DeleteFunc(slices.Clone(ids), func(id string) bool {
		a := cfg.FindAccount(id)
		return a == nil || a.Kind != config.KindAPIKey
	})
}

// agentAccessError explains why the caller's key can't use any account of plan.
func (s *Server) agentAccessError(p *authPrincipal, plan *router.Plan) error {
	if len(plan.CandidateIDs) == 0 || len(s.allowedCandidates(p, plan.CandidateIDs)) > 0 {
		return nil
	}
	return fmt.Errorf("API key %q may not use model %q: it runs an agent on the apx host. Allow it with: apx keys agents %s on",
		p.KeyName, plan.RequestedModel, p.Client.ID)
}

// sessionScope keeps the conversations of different API keys apart, so one key
// can't continue another's session by naming it.
func sessionScope(p *authPrincipal, session string) string {
	if session == "" || p == nil || p.Client == nil {
		return session
	}
	return "key:" + p.Client.ID + ":" + session
}
