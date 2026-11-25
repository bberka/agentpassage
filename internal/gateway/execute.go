package gateway

import (
	"context"

	"github.com/bberka/acpx/internal/backend"
	"github.com/bberka/acpx/internal/config"
	"github.com/bberka/acpx/internal/pool"
)

// StreamDirect executes a chat completion directly through the router, pool, and backend
// without an HTTP hop (used by the `acpx acp` stdio bridge server).
func (s *Server) StreamDirect(ctx context.Context, req backend.ChatRequest, cb backend.StreamCallback) (string, error) {
	plan, err := s.router.Resolve(req.Model)
	if err != nil {
		return "", err
	}
	req.UpstreamModel = plan.UpstreamModel

	tried := make(map[string]bool)
	var lastErr error
	var lastAcct string

	for attempt := 0; attempt < max(len(plan.CandidateIDs), 1); attempt++ {
		lease, err := s.pool.Acquire(pool.SelectRequest{
			RuleKey:                  plan.Rule.Pattern,
			Strategy:                 plan.Rule.Strategy,
			CandidateIDs:             plan.CandidateIDs,
			SessionKey:               req.SessionKey,
			SubscriptionWarningAcked: plan.Rule.SubscriptionWarningAcked,
			ExcludeIDs:               tried,
		})
		if err != nil {
			if lastErr != nil {
				return lastAcct, lastErr
			}
			return "", err
		}

		acct := lease.Account
		acctName := acct.Backend + "/" + acct.Name
		lastAcct = acctName
		tried[acct.ID] = true

		emitted := false
		wrapped := func(ch backend.StreamChunk) error {
			if ch.Delta != "" {
				emitted = true
			}
			return cb(ch)
		}

		var streamErr error
		switch acct.Kind {
		case config.KindACP:
			streamErr = s.acpBE.Stream(ctx, acct, req, wrapped)
		case config.KindCLI:
			streamErr = s.cliBE.Stream(ctx, acct, req, wrapped)
		default:
			apiKey, _ := s.secrets.Get(acct.ID)
			streamErr = s.httpBE.Stream(ctx, acct, apiKey, req, wrapped)
		}
		lease.Release()

		if streamErr == nil {
			s.pool.RecordSuccess(acct.ID)
			return acctName, nil
		}

		lastErr = streamErr
		allowFailover := s.pool.RecordError(acct.ID, streamErr, plan.Rule)
		if emitted || !allowFailover {
			return acctName, streamErr
		}
	}

	if lastErr == nil {
		lastErr = pool.ErrNoAvailableAccount
	}
	return lastAcct, lastErr
}
