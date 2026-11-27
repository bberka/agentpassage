package gateway

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/pool"
	"github.com/bberka/agentpassage/internal/quota"
	"github.com/bberka/agentpassage/internal/router"
	"github.com/bberka/agentpassage/internal/usage"
)

// applyRouteDefaults applies per-route DefaultEffort (including "off"/"none" to disable thinking)
// and optional SystemPrompt prepending.
func applyRouteDefaults(req backend.ChatRequest, rule config.RoutingRule) backend.ChatRequest {
	if def := strings.TrimSpace(rule.DefaultEffort); def != "" {
		lower := strings.ToLower(def)
		if lower == "off" || lower == "none" {
			req.ReasoningEffort = ""
		} else if req.ReasoningEffort == "" {
			req.ReasoningEffort = router.NormalizeEffort(def)
		}
	}
	if sp := strings.TrimSpace(rule.SystemPrompt); sp != "" {
		hasSame := len(req.Messages) > 0 && req.Messages[0].Role == "system" && strings.Contains(req.Messages[0].Content, sp)
		if !hasSame {
			msgs := make([]backend.Message, 0, len(req.Messages)+1)
			msgs = append(msgs, backend.Message{Role: "system", Content: sp})
			msgs = append(msgs, req.Messages...)
			req.Messages = msgs
		}
	}
	return req
}

// buildPoolSelectRequest builds a pool.SelectRequest enriched with live quota percentages
// and USD spend budget checks for the candidate accounts.
func (s *Server) buildPoolSelectRequest(ctx context.Context, plan *router.Plan, sessionKey string, exclude map[string]bool) pool.SelectRequest {
	qGroup := quota.GroupForModel(plan.Backend, plan.UpstreamModel)
	req := pool.SelectRequest{
		RuleKey:                  plan.Rule.Pattern,
		Strategy:                 plan.Rule.Strategy,
		CandidateIDs:             s.allowedCandidates(principalFromContext(ctx), plan.CandidateIDs),
		SessionKey:               sessionKey,
		SubscriptionWarningAcked: plan.Rule.SubscriptionWarningAcked,
		ExcludeIDs:               exclude,
		QuotaGroup:               qGroup,
		QuotaThresholdPct:        plan.Rule.QuotaThresholdPct,
	}

	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	req.WaitForResetWhenExhausted = cfg.Sessions.WaitForResetWhenExhausted

	if s.quota != nil {
		candidate := make(map[string]bool, len(plan.CandidateIDs))
		for _, id := range plan.CandidateIDs {
			candidate[id] = true
		}
		now := time.Now()
		used := make(map[string]float64, len(plan.CandidateIDs))
		var out map[string]time.Time
		for _, aq := range s.routingQuotaReport().Accounts {
			if !candidate[aq.AccountID] {
				continue
			}
			u := aq.UsageForModel(plan.UpstreamModel, now)
			if u.HasData {
				used[aq.AccountID] = u.UsedPct
			}
			if u.Exhausted {
				if out == nil {
					out = make(map[string]time.Time)
				}
				out[aq.AccountID] = u.ResetsAt
			}
		}
		req.QuotaUsedPct = used
		req.QuotaExhausted = out
	}

	if s.usage != nil {
		needDay, needMonth := false, false
		for _, id := range plan.CandidateIDs {
			if acct := cfg.FindAccount(id); acct != nil {
				if acct.DailyBudgetUSD > 0 {
					needDay = true
				}
				if acct.MonthlyBudgetUSD > 0 {
					needMonth = true
				}
			}
		}
		if needDay || needMonth {
			daySpend := make(map[string]float64)
			monthSpend := make(map[string]float64)
			if needDay {
				for _, au := range s.usage.SummaryPeriod("24h").ByAccount {
					daySpend[au.Account] = au.EstimatedCostUSD
				}
			}
			if needMonth {
				for _, au := range s.usage.SummaryPeriod("30d").ByAccount {
					monthSpend[au.Account] = au.EstimatedCostUSD
				}
			}
			exceeded := make(map[string]bool)
			for _, id := range plan.CandidateIDs {
				acct := cfg.FindAccount(id)
				if acct == nil {
					continue
				}
				label := acct.Backend + "/" + acct.Name
				if acct.DailyBudgetUSD > 0 && daySpend[label] >= acct.DailyBudgetUSD {
					exceeded[id] = true
				}
				if acct.MonthlyBudgetUSD > 0 && monthSpend[label] >= acct.MonthlyBudgetUSD {
					exceeded[id] = true
				}
			}
			if len(exceeded) > 0 {
				req.BudgetExceededIDs = exceeded
			}
		}
	}

	return req
}

// StreamDirect executes a chat completion directly through the router, pool, and backend
// without an HTTP hop (used by the `apx acp` stdio bridge server).
func (s *Server) StreamDirect(ctx context.Context, req backend.ChatRequest, cb backend.StreamCallback) (string, error) {
	if resolved, _, ok := s.ResolveDecisionModel(req.Model, req); ok && resolved != "" {
		req.Model = resolved
	}
	primaryPlan, err := s.router.Resolve(req.Model)
	if err != nil {
		return "", err
	}
	start := time.Now()
	res, err := s.streamPlan(ctx, primaryPlan, "", req, cb)
	if res.account != "" && (err == nil || res.emitted) {
		status := http.StatusOK
		if err != nil {
			status = httpStatusFromError(err)
		}
		s.recordDirectUsage(res.model, res.account, res.backendID, status, start, req, res.usage, res.replyLen)
	}
	return res.account, err
}

// streamResult says which account and model served a turn.
type streamResult struct {
	account   string
	backendID string
	model     string
	usage     *backend.Usage // nil when the account reported none
	replyLen  int
	emitted   bool // reply text reached the caller
}

// streamPlan streams one turn, failing over between accounts and fallback
// models. The caller records usage. The key in ctx, if any, limits the
// accounts and fallback models.
func (s *Server) streamPlan(ctx context.Context, primaryPlan *router.Plan, project string, req backend.ChatRequest, cb backend.StreamCallback) (streamResult, error) {
	principal := principalFromContext(ctx)
	privSess := s.newPrivacySession()
	req = applyPrivacyToRequest(privSess, req)

	chain := s.buildModelChain(primaryPlan, project, principal)
	var lastErr error
	var last streamResult

	for _, plan := range chain {
		tried := make(map[string]bool)
		var planErr error

		for attempt := 0; attempt < max(len(plan.CandidateIDs), 1); attempt++ {
			lease, err := s.pool.Acquire(s.buildPoolSelectRequest(ctx, plan, req.SessionKey, tried))
			if err != nil {
				if planErr == nil {
					planErr = err
				}
				break
			}

			acct := lease.Account
			acctName := acct.Backend + "/" + acct.Name
			last = streamResult{account: acctName, backendID: acct.Backend, model: plan.RequestedModel}
			tried[acct.ID] = true

			emitted := false
			var reported *backend.Usage
			var replyLen int
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
					replyLen += len(ch.Delta)
				}
				if ch.Usage != nil {
					reported = ch.Usage
				}
				if ch.Delta == "" && ch.Reasoning == "" && len(ch.RawACPUpdate) == 0 && ch.FinishReason == "" && ch.Usage == nil {
					return nil
				}
				return cb(ch)
			}

			acctReq := applyRouteDefaults(req, plan.Rule)
			acctReq.Model = plan.RequestedModel
			acctReq.UpstreamModel = router.ResolveAccountEffortModel(acct, plan.UpstreamModel, acctReq.ReasoningEffort)

			callStart := time.Now()
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
				s.pool.PinSession(req.SessionKey, acct.ID)
				s.pool.RecordLatency(acct.ID, time.Since(callStart).Milliseconds())
				s.recordPrivacyHeader(nil, privSess, plan.RequestedModel)
				last.usage, last.replyLen, last.emitted = reported, replyLen, emitted
				return last, nil
			}

			planErr = streamErr
			lastErr = streamErr
			allowFailover := s.recordFailure(ctx, acct.ID, plan, streamErr)
			if emitted {
				last.usage, last.replyLen, last.emitted = reported, replyLen, true
				return last, streamErr
			}
			if !allowFailover {
				break
			}
		}

		if planErr != nil {
			lastErr = planErr
		}
		if ctx.Err() != nil || !shouldFallbackModel(lastErr, primaryPlan.Rule) {
			break
		}
	}

	if lastErr == nil {
		lastErr = pool.ErrNoAvailableAccount
	}
	return last, lastErr
}

// recordDirectUsage logs a turn that did not come over HTTP (`apx acp`). When
// the agent reports no token counts they are estimated from the newest user
// message and the reply, which leaves out the agent's own context.
func (s *Server) recordDirectUsage(model, acct, backendID string, status int, start time.Time, req backend.ChatRequest, reported *backend.Usage, replyLen int) {
	if s.usage == nil {
		return
	}
	var u backend.Usage
	estimated := reported == nil
	if estimated {
		u.PromptTokens = usage.EstimateTokens(backend.FormatPrompt(req.Messages, true))
		u.CompletionTokens = (replyLen + 3) / 4
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	} else {
		u = *reported
	}
	s.usage.Record(usage.Record{
		Time:             time.Now(),
		Account:          acct,
		Backend:          backendID,
		Model:            model,
		Endpoint:         "acp",
		Status:           status,
		DurationMs:       time.Since(start).Milliseconds(),
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CacheReadTokens:  u.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens,
		Estimated:        estimated,
	})
}
