package gateway

import (
	"context"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/pool"
	"github.com/bberka/agentpassage/internal/router"
)

// StreamDirect executes a chat completion directly through the router, pool, and backend
// without an HTTP hop (used by the `apx acp` stdio bridge server).
func (s *Server) StreamDirect(ctx context.Context, req backend.ChatRequest, cb backend.StreamCallback) (string, error) {
	primaryPlan, err := s.router.Resolve(req.Model)
	if err != nil {
		return "", err
	}

	privSess := s.newPrivacySession()
	req = applyPrivacyToRequest(privSess, req)

	chain := s.buildModelChain(primaryPlan, "", nil)
	var lastErr error
	var lastAcct string

	for _, plan := range chain {
		tried := make(map[string]bool)
		var planErr error

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
				if planErr == nil {
					planErr = err
				}
				break
			}

			acct := lease.Account
			acctName := acct.Backend + "/" + acct.Name
			lastAcct = acctName
			tried[acct.ID] = true

			emitted := false
			deltaRestorer := privSess.NewStreamRestorer()
			reasonRestorer := privSess.NewStreamRestorer()
			wrapped := func(ch backend.StreamChunk) error {
				ch.Delta = deltaRestorer.Push(ch.Delta)
				ch.Reasoning = reasonRestorer.Push(ch.Reasoning)
				if ch.FinishReason != "" {
					ch.Delta += deltaRestorer.Flush()
					ch.Reasoning += reasonRestorer.Flush()
				}
				if ch.Delta != "" {
					emitted = true
				}
				if ch.Delta == "" && ch.Reasoning == "" && ch.FinishReason == "" && ch.Usage == nil {
					return nil
				}
				return cb(ch)
			}

			acctReq := req
			acctReq.Model = plan.RequestedModel
			acctReq.UpstreamModel = router.ResolveAccountEffortModel(acct, plan.UpstreamModel, req.ReasoningEffort)

			var streamErr error
			switch acct.Kind {
			case config.KindACP:
				streamErr = s.acpBE.Stream(ctx, acct, acctReq, wrapped)
			case config.KindCLI:
				streamErr = s.cliBE.Stream(ctx, acct, acctReq, wrapped)
			default:
				apiKey, _ := s.secrets.Get(acct.ID)
				streamErr = s.httpBE.Stream(ctx, acct, apiKey, acctReq, wrapped)
			}
			lease.Release()

			if streamErr == nil {
				if td, tr := deltaRestorer.Flush(), reasonRestorer.Flush(); td != "" || tr != "" {
					_ = cb(backend.StreamChunk{Delta: td, Reasoning: tr})
				}
				s.pool.RecordSuccess(acct.ID)
				s.recordPrivacyHeader(nil, privSess, plan.RequestedModel)
				return acctName, nil
			}

			planErr = streamErr
			lastErr = streamErr
			allowFailover := s.pool.RecordError(acct.ID, streamErr, plan.Rule)
			if emitted {
				return acctName, streamErr
			}
			if !allowFailover {
				break
			}
		}

		if planErr != nil {
			lastErr = planErr
		}
		if !shouldFallbackModel(lastErr, primaryPlan.Rule) {
			break
		}
	}

	if lastErr == nil {
		lastErr = pool.ErrNoAvailableAccount
	}
	return lastAcct, lastErr
}
