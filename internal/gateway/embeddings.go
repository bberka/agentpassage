package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/pool"
)

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request) {
	s.handleHTTPProxyEndpoint(w, r, "embeddings")
}

func (s *Server) handleRerank(w http.ResponseWriter, r *http.Request) {
	s.handleHTTPProxyEndpoint(w, r, "rerank")
}

func (s *Server) handleHTTPProxyEndpoint(w http.ResponseWriter, r *http.Request, op string) {
	start := time.Now()
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "failed to read request body")
		return
	}
	var head struct {
		Model string `json:"model"`
		User  string `json:"user"`
	}
	if err := json.Unmarshal(rawBody, &head); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON: "+err.Error())
		return
	}
	head.Model = strings.TrimSpace(head.Model)
	if head.Model == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}

	principal := principalFromContext(r.Context())
	if principal != nil && principal.Client != nil && !principal.Client.AllowsModel(head.Model) {
		writeOpenAIError(w, http.StatusForbidden, "model_not_allowed", fmt.Sprintf("API key %q is not allowed to access model %q.", principal.KeyName, head.Model))
		return
	}
	project := projectFromRequest(r)
	primaryPlan, err := s.router.ResolveForProject(head.Model, project)
	if err != nil {
		s.logRequest(r, head.Model, "", http.StatusNotFound, start, err)
		writeOpenAIError(w, http.StatusNotFound, "model_not_found", err.Error())
		return
	}
	if loopErr := checkLoopGuard(r, primaryPlan); loopErr != nil {
		s.logRequest(r, head.Model, primaryPlan.Backend, http.StatusLoopDetected, start, loopErr)
		writeOpenAIError(w, http.StatusLoopDetected, "loop_detected", loopErr.Error())
		return
	}

	if accessErr := s.agentAccessError(principal, primaryPlan); accessErr != nil {
		s.logRequest(r, head.Model, "", http.StatusForbidden, start, accessErr)
		writeOpenAIError(w, http.StatusForbidden, "model_not_allowed", accessErr.Error())
		return
	}

	sessionKey := headerFirst(r, "X-Apx-Session", "X-Acpx-Session")
	if sessionKey == "" {
		sessionKey = strings.TrimSpace(head.User)
	}

	chain := s.buildModelChain(primaryPlan, project, principal)
	var lastErr error
	var lastAcct string
	var lastBackend string

	for planIdx, plan := range chain {
		if planIdx > 0 {
			if loopErr := checkLoopGuard(r, plan); loopErr != nil {
				continue
			}
		}
		tried := make(map[string]bool)
		var planErr error

		for attempt := 0; attempt < max(len(plan.CandidateIDs), 1); attempt++ {
			lease, err := s.pool.Acquire(pool.SelectRequest{
				RuleKey:                  plan.Rule.Pattern,
				Strategy:                 plan.Rule.Strategy,
				CandidateIDs:             s.allowedCandidates(principal, plan.CandidateIDs),
				SessionKey:               sessionScope(principal, sessionKey),
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
			lastBackend = acct.Backend
			tried[acct.ID] = true

			if acct.Kind != config.KindAPIKey {
				lease.Release()
				planErr = &backend.BackendError{
					Kind:       backend.ErrKindClient,
					StatusCode: http.StatusBadRequest,
					Message:    fmt.Sprintf("account %s (%s) does not support /v1/%s; route to an HTTP API-key provider", acctName, acct.Kind, op),
				}
				lastErr = planErr
				continue
			}

			apiKey, _ := s.secrets.Get(acct.ID)
			var respBody []byte
			var u backend.Usage
			var callErr error
			if op == "embeddings" {
				respBody, u, callErr = s.httpBE.Embed(r.Context(), acct, apiKey, plan.UpstreamModel, rawBody)
			} else {
				respBody, u, callErr = s.httpBE.Rerank(r.Context(), acct, apiKey, plan.UpstreamModel, rawBody)
			}
			lease.Release()

			if callErr == nil {
				s.pool.RecordSuccess(acct.ID)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Apx-Account", acctName)
				w.Header().Set("X-Apx-Model", plan.RequestedModel)
				if planIdx > 0 {
					w.Header().Set("X-Apx-Fallback-From", primaryPlan.RequestedModel)
				}
				if project != "" {
					w.Header().Set("X-Apx-Project", project)
				}
				s.recordUsageForRequest(r, plan.RequestedModel, acctName, acct.Backend, http.StatusOK, start, u, false)
				s.logRequest(r, plan.RequestedModel, acctName, http.StatusOK, start, nil)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(respBody)
				return
			}

			planErr = callErr
			lastErr = callErr
			if !s.recordFailure(r.Context(), acct.ID, plan, callErr) {
				break
			}
		}

		if planErr != nil {
			lastErr = planErr
		}
		if r.Context().Err() != nil || !shouldFallbackModel(lastErr, primaryPlan.Rule) {
			break
		}
	}

	status, lastErr := finalFailure(lastErr)
	s.recordUsageForRequest(r, head.Model, lastAcct, lastBackend, status, start, backend.Usage{}, false)
	s.logRequest(r, head.Model, lastAcct, status, start, lastErr)
	writeOpenAIError(w, status, "backend_error", lastErr.Error())
}
