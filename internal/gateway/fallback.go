package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/pool"
	"github.com/bberka/agentpassage/internal/router"
)

// projectFromRequest resolves the effective project identifier for routing & usage attribution.
// If the authenticated API key is pinned to a project, that project is authoritative;
// otherwise the client may pass X-Apx-Project (or legacy X-Acpx-Project).
func projectFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if p := principalFromContext(r.Context()); p != nil && p.Client != nil {
		if proj := strings.TrimSpace(p.Client.Project); proj != "" {
			return proj
		}
	}
	return headerFirst(r, "X-Apx-Project", "X-Acpx-Project")
}

// shouldFallbackModel checks whether err is an eligible, configured failure kind for
// retrying with an alternate model in plan.Rule.FallbackModels.
func shouldFallbackModel(err error, rule config.RoutingRule) bool {
	if err == nil || len(rule.FallbackModels) == 0 {
		return false
	}
	kind := classifyFallbackKind(err)
	for _, allowed := range rule.EffectiveFallbackOn() {
		a := strings.ToLower(strings.TrimSpace(allowed))
		if a == "all" || a == kind {
			return true
		}
	}
	return false
}

func classifyFallbackKind(err error) string {
	if errors.Is(err, pool.ErrNoAvailableAccount) {
		return "unavailable"
	}
	if errors.Is(err, pool.ErrStrategyOptInNeeded) {
		return "policy"
	}
	if be, ok := backend.AsBackendError(err); ok {
		return strings.ToLower(string(be.Kind))
	}
	return "transient"
}

// buildModelChain returns the ordered list of resolved routing plans starting with primary,
// followed by any configured FallbackModels on primary.Rule (deduplicated and filtered by key allowlist).
func (s *Server) buildModelChain(primary *router.Plan, project string, principal *authPrincipal) []*router.Plan {
	chain := []*router.Plan{primary}
	if primary == nil || len(primary.Rule.FallbackModels) == 0 {
		return chain
	}
	visited := map[string]bool{
		primary.RequestedModel: true,
		primary.CanonicalModel: true,
	}
	for _, fbModel := range primary.Rule.FallbackModels {
		fbModel = strings.TrimSpace(fbModel)
		if fbModel == "" || visited[fbModel] {
			continue
		}
		if principal != nil && principal.Client != nil && !principal.Client.AllowsModel(fbModel) {
			continue
		}
		fbPlan, err := s.router.ResolveForProject(fbModel, project)
		if err != nil || fbPlan == nil {
			continue
		}
		if visited[fbPlan.CanonicalModel] || s.agentAccessError(principal, fbPlan) != nil {
			continue
		}
		visited[fbModel] = true
		visited[fbPlan.CanonicalModel] = true
		chain = append(chain, fbPlan)
	}
	return chain
}

type nonStreamResult struct {
	Resp         *backend.ChatResponse
	Account      *config.AccountConfig
	UsedModel    string
	FallbackFrom string
	Status       int
	Err          error
}

// executeNonStreamWithFallback executes a non-streaming request on primaryPlan (with account failover),
// and if that fails with a configured fallback-eligible error, retries each configured fallback model.
func (s *Server) executeNonStreamWithFallback(ctx context.Context, primary *router.Plan, project string, principal *authPrincipal, req backend.ChatRequest) nonStreamResult {
	chain := s.buildModelChain(primary, project, principal)
	var last nonStreamResult
	for i, pl := range chain {
		tryReq := req
		tryReq.Model = pl.RequestedModel
		tryReq.UpstreamModel = pl.UpstreamModel
		resp, acct, status, err := s.executeNonStream(ctx, pl, tryReq)
		if err == nil {
			fbFrom := ""
			if i > 0 {
				fbFrom = primary.RequestedModel
				if s.logs != nil {
					s.logs.Warn("fallback", fmt.Sprintf("model fallback %s -> %s succeeded", primary.RequestedModel, pl.RequestedModel))
				}
			}
			return nonStreamResult{
				Resp:         resp,
				Account:      acct,
				UsedModel:    pl.RequestedModel,
				FallbackFrom: fbFrom,
				Status:       status,
			}
		}
		last = nonStreamResult{
			Account:   acct,
			UsedModel: pl.RequestedModel,
			Status:    status,
			Err:       err,
		}
		if ctx.Err() != nil || !shouldFallbackModel(err, primary.Rule) {
			return last
		}
		if i+1 < len(chain) && s.logs != nil {
			s.logs.Warn("fallback", fmt.Sprintf("model %s failed (%s); falling back to %s", pl.RequestedModel, classifyFallbackKind(err), chain[i+1].RequestedModel))
		}
	}
	return last
}
