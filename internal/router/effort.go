package router

import (
	"sort"
	"strings"

	"github.com/bberka/acpx/internal/config"
)

// Canonical effort/thinking levels ordered from lowest to highest.
var effortOrder = []string{"minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

var effortRank = map[string]int{
	"minimal": 1,
	"low":     2,
	"medium":  3,
	"high":    4,
	"xhigh":   5,
	"max":     6,
	"ultra":   7,
}

// NormalizeEffort normalizes user/API effort strings into canonical levels.
func NormalizeEffort(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "", "default", "auto", "none", "off":
		return ""
	case "med", "mid":
		return "medium"
	case "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
		return s
	default:
		return s
	}
}

// BudgetToEffort converts an Anthropic thinking budget_tokens value into an effort level.
func BudgetToEffort(budgetTokens int) string {
	switch {
	case budgetTokens <= 0:
		return ""
	case budgetTokens <= 2048:
		return "low"
	case budgetTokens <= 8192:
		return "medium"
	case budgetTokens <= 24576:
		return "high"
	default:
		return "max"
	}
}

// SplitModelEffort parses a model ID that encodes an effort suffix in either
// bracket style (`gpt-6-luna[high]`) or dash style (`gemini-4-neon-xhigh`).
func SplitModelEffort(modelID string) (base, effort, style string) {
	// 1. Bracket style: model[effort]
	if strings.HasSuffix(modelID, "]") {
		if idx := strings.LastIndex(modelID, "["); idx > 0 {
			cand := strings.ToLower(modelID[idx+1 : len(modelID)-1])
			if _, ok := effortRank[cand]; ok {
				return modelID[:idx], cand, "bracket"
			}
		}
	}
	// 2. Dash style: model-effort
	if idx := strings.LastIndex(modelID, "-"); idx > 0 && idx < len(modelID)-1 {
		cand := strings.ToLower(modelID[idx+1:])
		if _, ok := effortRank[cand]; ok {
			return modelID[:idx], cand, "dash"
		}
	}
	return modelID, "", ""
}

// GroupEffortVariants collapses `-low`/`-medium`/`-high`/`[xhigh]` effort variants
// into their base model when safe to do so (either the base model exists, at least
// two sibling effort levels share the base, or bracket syntax is used).
func GroupEffortVariants(models []string) (grouped []string, effortsByBase map[string][]string) {
	present := make(map[string]bool, len(models))
	for _, m := range models {
		present[strings.TrimSpace(m)] = true
	}

	// Count distinct effort levels per candidate base model.
	siblingEfforts := make(map[string]map[string]bool)
	hasBracket := make(map[string]bool)
	for _, raw := range models {
		m := strings.TrimSpace(raw)
		if m == "" {
			continue
		}
		base, eff, style := SplitModelEffort(m)
		if eff == "" {
			continue
		}
		if siblingEfforts[base] == nil {
			siblingEfforts[base] = make(map[string]bool)
		}
		siblingEfforts[base][eff] = true
		if style == "bracket" {
			hasBracket[base] = true
		}
	}

	shouldCollapse := func(base string) bool {
		return present[base] || hasBracket[base] || len(siblingEfforts[base]) >= 2
	}

	seenBase := make(map[string]bool, len(models))
	effortsByBase = make(map[string][]string)

	for _, raw := range models {
		m := strings.TrimSpace(raw)
		if m == "" {
			continue
		}
		base, eff, _ := SplitModelEffort(m)
		if eff != "" && shouldCollapse(base) {
			if !seenBase[base] {
				seenBase[base] = true
				grouped = append(grouped, base)
			}
			continue
		}
		if !seenBase[m] {
			seenBase[m] = true
			grouped = append(grouped, m)
		}
	}

	for base, set := range siblingEfforts {
		if !shouldCollapse(base) {
			continue
		}
		list := make([]string, 0, len(set))
		for eff := range set {
			list = append(list, eff)
		}
		sortEfforts(list)
		effortsByBase[base] = list
	}
	return grouped, effortsByBase
}

func sortEfforts(list []string) {
	sort.Slice(list, func(i, j int) bool {
		ri, okI := effortRank[list[i]]
		rj, okJ := effortRank[list[j]]
		if okI && okJ && ri != rj {
			return ri < rj
		}
		return list[i] < list[j]
	})
}

func mergeEfforts(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, x := range a {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	for _, x := range b {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sortEfforts(out)
	return out
}

// ResolveAccountEffortModel maps (baseUpstream, effort) to an account's concrete
// upstream effort-variant model ID (e.g. `gemini-4-neon-high` or `gpt-6-luna[high]`)
// if the account exposes that variant. Otherwise returns baseUpstream unchanged.
func ResolveAccountEffortModel(acct config.AccountConfig, baseUpstream, effort string) string {
	eff := NormalizeEffort(effort)
	if eff == "" || baseUpstream == "" {
		return baseUpstream
	}
	// If baseUpstream already encodes an effort suffix, leave it alone.
	if _, existing, _ := SplitModelEffort(baseUpstream); existing != "" {
		return baseUpstream
	}
	dashCand := baseUpstream + "-" + eff
	bracketCand := baseUpstream + "[" + eff + "]"
	checkList := func(list []string) string {
		for _, m := range list {
			if m == bracketCand || m == dashCand {
				return m
			}
		}
		return ""
	}
	if hit := checkList(acct.Models); hit != "" {
		return hit
	}
	if hit := checkList(acct.AvailableModels); hit != "" {
		return hit
	}
	return baseUpstream
}
