package gateway

import (
	"context"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/router"
)

// PrepareAgentSession opens the session an `apx acp` conversation's next turn
// would use, when an ACP account would serve it. It sends no prompt and counts
// no request. The auto router's model is skipped: it is chosen from the prompt.
func (s *Server) PrepareAgentSession(ctx context.Context, req backend.ChatRequest) error {
	if _, _, auto := s.ResolveDecisionModel(req.Model, req); !auto {
		plan, err := s.router.Resolve(req.Model)
		if err != nil {
			return err
		}
		acct, ok := s.pool.Preview(s.buildPoolSelectRequest(ctx, plan, req.SessionKey, nil))
		if ok && acct.Kind == config.KindACP {
			req = applyRouteDefaults(req, plan.Rule)
			req.Messages = nil
			req.Model = plan.RequestedModel
			req.UpstreamModel = router.ResolveAccountEffortModel(acct, plan.UpstreamModel, req.ReasoningEffort)
			return s.acpBE.Prepare(ctx, acct, req)
		}
	}
	s.acpBE.DropPrepared(req.SessionKey, "")
	return nil
}
