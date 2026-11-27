package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/bberka/agentpassage/internal/pool"
)

// RouteExplanation answers "which account would this model go to, and why"
// without sending a request.
type RouteExplanation struct {
	RequestedModel string           `json:"requested_model"`
	UpstreamModel  string           `json:"upstream_model,omitempty"`
	Backend        string           `json:"backend,omitempty"`
	Rule           string           `json:"rule,omitempty"`
	SessionKey     string           `json:"session_key,omitempty"`
	Pool           pool.Explanation `json:"pool"`
	Error          string           `json:"error,omitempty"`
}

// ExplainRoute runs routing for model as a dry run. sessionKey, when set,
// shows the choice for an existing conversation instead of a new one.
func (s *Server) ExplainRoute(ctx context.Context, model, sessionKey string) (RouteExplanation, error) {
	s.mu.RLock()
	attached := s.attached
	addr := s.listenOn
	key := s.cfg.Server.LocalKey
	s.mu.RUnlock()

	if attached && addr != "" {
		q := url.Values{"model": {model}}
		if sessionKey != "" {
			q.Set("session", sessionKey)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/admin/route/explain?"+q.Encode(), nil)
		if err != nil {
			return RouteExplanation{}, err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return RouteExplanation{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return RouteExplanation{}, fmt.Errorf("apx service returned HTTP %d for route explain (restart apx.service if it predates this command)", resp.StatusCode)
		}
		var ex RouteExplanation
		err = json.NewDecoder(resp.Body).Decode(&ex)
		return ex, err
	}
	return s.explainRouteLocal(model, sessionKey), nil
}

func (s *Server) explainRouteLocal(model, sessionKey string) RouteExplanation {
	ex := RouteExplanation{RequestedModel: model, SessionKey: sessionKey}
	plan, err := s.router.Resolve(model)
	if err != nil {
		ex.Error = err.Error()
		return ex
	}
	ex.UpstreamModel = plan.UpstreamModel
	ex.Backend = plan.Backend
	ex.Rule = plan.Rule.Pattern
	ex.Pool = s.pool.Explain(s.buildPoolSelectRequest(plan, sessionKey, nil))
	return ex
}

func (s *Server) handleAdminRouteExplain(w http.ResponseWriter, r *http.Request) {
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if model == "" {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	writeJSON(w, http.StatusOK, s.explainRouteLocal(model, strings.TrimSpace(r.URL.Query().Get("session"))))
}
