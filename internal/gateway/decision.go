package gateway

import (
	"strings"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
)

// ResolveDecisionModel checks whether requestedModel matches the configured virtual
// decision/auto-router model (default "apx/auto" or "auto") and selects the best target
// model based on prompt intent (vision, coding, deep reasoning, fast/short, or default).
// Returns (resolvedModel, decisionReason, matched).
func (s *Server) ResolveDecisionModel(requestedModel string, req backend.ChatRequest) (string, string, bool) {
	s.mu.RLock()
	dc := s.cfg.Decision
	s.mu.RUnlock()

	if !dc.Enabled {
		return requestedModel, "", false
	}
	virtualID := strings.TrimSpace(dc.ModelID)
	if virtualID == "" {
		virtualID = "apx/auto"
	}
	reqLower := strings.ToLower(strings.TrimSpace(requestedModel))
	if reqLower != strings.ToLower(virtualID) && reqLower != "apx/auto" && reqLower != "decision" {
		return requestedModel, "", false
	}

	pick := func(cands ...string) string {
		for _, c := range cands {
			if c = strings.TrimSpace(c); c != "" {
				return c
			}
		}
		// Fallback to first enabled model in catalog.
		if models := s.router.ListModels(false); len(models) > 0 {
			return models[0].ID
		}
		return ""
	}

	// 1. Vision / image prompt
	if len(req.Images) > 0 && strings.TrimSpace(dc.VisionModel) != "" {
		return pick(dc.VisionModel, dc.DefaultModel), "vision", true
	}

	// Analyze latest user message
	lastUser := ""
	totalLen := 0
	for _, m := range req.Messages {
		totalLen += len(m.Content)
		if m.Role == "user" {
			lastUser = m.Content
		}
	}
	lower := strings.ToLower(lastUser)

	// 2. Deep reasoning / planning / architecture / math
	for _, kw := range []string{"prove ", "theorem", "architect", "design a ", "trade-off", "tradeoff", "step by step", "analyze", "debug why", "root cause", "plan "} {
		if strings.Contains(lower, kw) {
			return pick(dc.ReasoningModel, dc.CodingModel, dc.DefaultModel), "reasoning", true
		}
	}

	// 3. Coding / diff / stack trace
	if strings.Contains(lastUser, "```") || strings.Contains(lower, "func ") || strings.Contains(lower, "class ") ||
		strings.Contains(lower, "refactor") || strings.Contains(lower, "implement") || strings.Contains(lower, "fix bug") ||
		strings.Contains(lower, "typescript") || strings.Contains(lower, "golang") || strings.Contains(lower, "python") {
		return pick(dc.CodingModel, dc.DefaultModel, dc.ReasoningModel), "coding", true
	}

	// 4. Short / fast query
	if len(lastUser) < 160 && totalLen < 400 && strings.TrimSpace(dc.FastModel) != "" {
		return pick(dc.FastModel, dc.DefaultModel), "fast", true
	}

	return pick(dc.DefaultModel, dc.CodingModel, dc.FastModel), "default", true
}

// EffectiveDecisionModelID returns the virtual model ID when decision routing is enabled.
func EffectiveDecisionModelID(dc config.DecisionConfig) string {
	if !dc.Enabled {
		return ""
	}
	if strings.TrimSpace(dc.ModelID) != "" {
		return strings.TrimSpace(dc.ModelID)
	}
	return "apx/auto"
}
