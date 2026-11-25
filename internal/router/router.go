package router

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/bberka/acpx/internal/config"
)

type ModelEntry struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name,omitempty"`
	Alias       string   `json:"alias,omitempty"`
	Backend     string   `json:"backend"`
	AccountID   string   `json:"account_id,omitempty"` // non-empty for manual <backend>/<account>/<model> entries
	AccountName string   `json:"account_name,omitempty"`
	Upstream    string   `json:"upstream"`
	Kind        string   `json:"kind"` // "acp" or "api_key"
	Hidden      bool     `json:"hidden,omitempty"`
	AccountIDs  []string `json:"account_ids,omitempty"`
}

type Plan struct {
	RequestedModel string
	CanonicalModel string
	UpstreamModel  string
	Backend        string
	Rule           config.RoutingRule
	CandidateIDs   []string
	IsManual       bool
}

type Router struct {
	mu  sync.RWMutex
	cfg *config.Config
}

func New(cfg *config.Config) *Router {
	return &Router{cfg: cfg}
}

func (r *Router) UpdateConfig(cfg *config.Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cfg = cfg
}

// ListModels builds the merged model namespace:
// - <backend>/<model> (pooled across accounts for that backend)
// - <backend>/<account>/<model> (manual targeting for every account)
// - aliases configured in config.Models
func (r *Router) ListModels(includeHidden bool) []ModelEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	overrides := make(map[string]config.ModelOverride, len(r.cfg.Models))
	for _, ov := range r.cfg.Models {
		overrides[ov.ID] = ov
	}

	pooled := make(map[string]*ModelEntry)
	var manual []ModelEntry

	for _, acct := range r.cfg.Accounts {
		if !acct.Enabled {
			continue
		}
		models := acct.Models
		if len(models) == 0 && acct.Kind == config.KindACP {
			models = []string{"default"}
		}
		for _, m := range models {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			poolID := acct.Backend + "/" + m
			if entry, ok := pooled[poolID]; ok {
				entry.AccountIDs = append(entry.AccountIDs, acct.ID)
			} else {
				pooled[poolID] = &ModelEntry{
					ID:         poolID,
					Backend:    acct.Backend,
					Upstream:   m,
					Kind:       acct.Kind,
					AccountIDs: []string{acct.ID},
				}
			}

			manualID := acct.Backend + "/" + acct.Name + "/" + m
			manual = append(manual, ModelEntry{
				ID:          manualID,
				Backend:     acct.Backend,
				AccountID:   acct.ID,
				AccountName: acct.Name,
				Upstream:    m,
				Kind:        acct.Kind,
				AccountIDs:  []string{acct.ID},
			})
		}
	}

	var all []ModelEntry
	for _, e := range pooled {
		all = append(all, *e)
	}
	all = append(all, manual...)

	out := make([]ModelEntry, 0, len(all))
	for _, e := range all {
		if ov, ok := overrides[e.ID]; ok {
			e.DisplayName = ov.DisplayName
			e.Alias = ov.Alias
			e.Hidden = ov.Hidden
		}
		if e.Hidden && !includeHidden {
			continue
		}
		out = append(out, e)
		if e.Alias != "" && (!e.Hidden || includeHidden) {
			aliasEntry := e
			aliasEntry.ID = e.Alias
			aliasEntry.Alias = ""
			if aliasEntry.DisplayName == "" {
				aliasEntry.DisplayName = e.ID
			}
			out = append(out, aliasEntry)
		}
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

// Resolve maps a requested model name to a routing plan and ordered candidate account IDs.
func (r *Router) Resolve(requested string) (*Plan, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	requested = strings.TrimSpace(requested)
	if requested == "" {
		return nil, fmt.Errorf("model is required")
	}

	// 1. Expand alias or display name override.
	canonical := requested
	for _, ov := range r.cfg.Models {
		if ov.Alias != "" && ov.Alias == requested {
			canonical = ov.ID
			break
		}
		if ov.DisplayName != "" && ov.DisplayName == requested {
			canonical = ov.ID
			break
		}
	}

	// 2. Check explicit manual form: <backend>/<account>/<model...>
	parts := strings.SplitN(canonical, "/", 3)
	if len(parts) == 3 {
		backendID, acctName, upstream := parts[0], parts[1], parts[2]
		for _, a := range r.cfg.Accounts {
			if a.Backend == backendID && (a.Name == acctName || a.ID == acctName) {
				return &Plan{
					RequestedModel: requested,
					CanonicalModel: canonical,
					UpstreamModel:  upstream,
					Backend:        backendID,
					Rule: config.RoutingRule{
						Pattern:  canonical,
						Accounts: []string{a.ID},
						Strategy: config.StrategyManual,
					},
					CandidateIDs: []string{a.ID},
					IsManual:     true,
				}, nil
			}
		}
	}

	// 3. Check <backend>/<model...> form.
	if len(parts) >= 2 {
		backendID := parts[0]
		upstream := strings.Join(parts[1:], "/")
		matchedAccounts := r.accountsForBackendLocked(backendID, upstream)
		if len(matchedAccounts) > 0 {
			rule := r.matchRuleLocked(canonical, matchedAccounts)
			candidates := r.orderCandidatesLocked(rule, matchedAccounts)
			return &Plan{
				RequestedModel: requested,
				CanonicalModel: canonical,
				UpstreamModel:  upstream,
				Backend:        backendID,
				Rule:           rule,
				CandidateIDs:   candidates,
			}, nil
		}
	}

	// 4. Fallback: match raw model name against accounts that list that model.
	var matched []*config.AccountConfig
	for i := range r.cfg.Accounts {
		a := &r.cfg.Accounts[i]
		for _, m := range a.Models {
			if m == canonical {
				matched = append(matched, a)
				break
			}
		}
	}
	if len(matched) > 0 {
		rule := r.matchRuleLocked(canonical, matched)
		candidates := r.orderCandidatesLocked(rule, matched)
		return &Plan{
			RequestedModel: requested,
			CanonicalModel: canonical,
			UpstreamModel:  canonical,
			Backend:        matched[0].Backend,
			Rule:           rule,
			CandidateIDs:   candidates,
		}, nil
	}

	return nil, fmt.Errorf("unknown model %q (check /v1/models or add an account)", requested)
}

func (r *Router) accountsForBackendLocked(backendID, upstream string) []*config.AccountConfig {
	var exact []*config.AccountConfig
	var anyInBackend []*config.AccountConfig
	for i := range r.cfg.Accounts {
		a := &r.cfg.Accounts[i]
		if a.Backend != backendID {
			continue
		}
		anyInBackend = append(anyInBackend, a)
		for _, m := range a.Models {
			if m == upstream {
				exact = append(exact, a)
				break
			}
		}
	}
	if len(exact) > 0 {
		return exact
	}
	// Allow pass-through of arbitrary model IDs for a configured backend (e.g. openrouter/any-model or agy/gemini-3.1-pro).
	return anyInBackend
}

func (r *Router) matchRuleLocked(model string, accounts []*config.AccountConfig) config.RoutingRule {
	// Prefer exact match, then longest glob match, then wildcard "*".
	var best *config.RoutingRule
	bestScore := -1
	for i := range r.cfg.Routing {
		rule := &r.cfg.Routing[i]
		if rule.Pattern == model {
			return *rule
		}
		matched, err := path.Match(rule.Pattern, model)
		if !matched || err != nil {
			// Also support simple prefix* matching across slashes (e.g. "agy/*").
			if strings.HasSuffix(rule.Pattern, "*") {
				prefix := strings.TrimSuffix(rule.Pattern, "*")
				matched = strings.HasPrefix(model, prefix)
			}
		}
		if matched {
			score := len(strings.TrimSuffix(rule.Pattern, "*"))
			if score > bestScore {
				bestScore = score
				best = rule
			}
		}
	}
	if best != nil {
		return *best
	}
	ids := make([]string, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.ID)
	}
	return config.RoutingRule{
		Pattern:  model,
		Accounts: ids,
		Strategy: config.StrategySticky,
	}
}

func (r *Router) orderCandidatesLocked(rule config.RoutingRule, matched []*config.AccountConfig) []string {
	if len(rule.Accounts) == 0 {
		out := make([]string, 0, len(matched))
		for _, a := range matched {
			out = append(out, a.ID)
		}
		return out
	}
	allowed := make(map[string]bool, len(matched))
	for _, a := range matched {
		allowed[a.ID] = true
	}
	out := make([]string, 0, len(rule.Accounts))
	for _, id := range rule.Accounts {
		if allowed[id] {
			out = append(out, id)
		} else if acct := r.cfg.FindAccount(id); acct != nil && allowed[acct.ID] {
			out = append(out, acct.ID)
		}
	}
	if len(out) == 0 {
		for _, a := range matched {
			out = append(out, a.ID)
		}
	}
	return out
}
