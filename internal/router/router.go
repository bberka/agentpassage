package router

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/bberka/agentpassage/internal/config"
)

type ModelEntry struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name,omitempty"`
	Alias       string   `json:"alias,omitempty"`
	Backend     string   `json:"backend"`
	AccountID   string   `json:"account_id,omitempty"` // non-empty for manual or prefixed entries
	AccountName string   `json:"account_name,omitempty"`
	Upstream    string   `json:"upstream"`
	Kind        string   `json:"kind"` // "acp" or "api_key"
	Hidden      bool     `json:"hidden,omitempty"`
	Prefixed    bool     `json:"prefixed,omitempty"`
	AccountIDs  []string `json:"account_ids,omitempty"`
	Efforts     []string `json:"efforts,omitempty"` // supported thinking/effort levels (e.g. low, medium, high, xhigh)
}

type Plan struct {
	RequestedModel  string
	CanonicalModel  string
	UpstreamModel   string
	ReasoningEffort string
	Backend         string
	Rule            config.RoutingRule
	CandidateIDs    []string
	IsManual        bool
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
// - If account.ExposeMode is "pool" or "both":
//     - <backend>/<model> (pooled across accounts for that backend)
//     - bare <model> (when not "default" and unambiguous across backends, so clients see e.g. "gemini-4-neon")
//     - <backend>/<account>/<model> (explicit manual targeting)
// - If account.ExposeMode is "prefix" or "both":
//     - <prefix><model> (e.g. "a-gemini-4-neon", "b-gemini-4-neon") targeting that account directly
// - Plus aliases configured in config.Models
func (r *Router) ListModels(includeHidden bool) []ModelEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	overrides := make(map[string]config.ModelOverride, len(r.cfg.Models))
	for _, ov := range r.cfg.Models {
		overrides[ov.ID] = ov
	}

	pooled := make(map[string]*ModelEntry)
	bare := make(map[string]*ModelEntry)
	bareBackends := make(map[string]map[string]bool)
	var direct []ModelEntry
	seenDirect := make(map[string]bool)

	for _, acct := range r.cfg.Accounts {
		if !acct.Enabled {
			continue
		}
		models := acct.Models
		if len(models) == 0 && acct.Kind == config.KindACP {
			models = []string{"default"}
		}
		var effortsByBase map[string][]string
		if !r.cfg.Catalog.ShowEffortVariants {
			models, effortsByBase = GroupEffortVariants(models)
		}
		mode := acct.ExposeMode
		if mode == "" {
			mode = config.ExposePool
		}

		for _, m := range models {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			efforts := effortsByBase[m]

			if mode == config.ExposePool || mode == config.ExposeBoth {
				poolID := acct.Backend + "/" + m
				if entry, ok := pooled[poolID]; ok {
					entry.AccountIDs = append(entry.AccountIDs, acct.ID)
					entry.Efforts = mergeEfforts(entry.Efforts, efforts)
				} else {
					pooled[poolID] = &ModelEntry{
						ID:         poolID,
						Backend:    acct.Backend,
						Upstream:   m,
						Kind:       acct.Kind,
						AccountIDs: []string{acct.ID},
						Efforts:    append([]string(nil), efforts...),
					}
				}

				// Also expose bare model name (e.g. "gemini-4-neon") for clean pooled rotation.
				if m != "default" && !strings.Contains(m, "/") {
					if bareBackends[m] == nil {
						bareBackends[m] = make(map[string]bool)
					}
					bareBackends[m][acct.Backend] = true
					if entry, ok := bare[m]; ok {
						entry.AccountIDs = append(entry.AccountIDs, acct.ID)
						entry.Efforts = mergeEfforts(entry.Efforts, efforts)
					} else {
						bare[m] = &ModelEntry{
							ID:         m,
							Backend:    acct.Backend,
							Upstream:   m,
							Kind:       acct.Kind,
							AccountIDs: []string{acct.ID},
							Efforts:    append([]string(nil), efforts...),
						}
					}
				}

				if r.cfg.Catalog.ExposeDirectModels {
					manualID := acct.Backend + "/" + acct.Name + "/" + m
					if !seenDirect[manualID] {
						seenDirect[manualID] = true
						direct = append(direct, ModelEntry{
							ID:          manualID,
							Backend:     acct.Backend,
							AccountID:   acct.ID,
							AccountName: acct.Name,
							Upstream:    m,
							Kind:        acct.Kind,
							AccountIDs:  []string{acct.ID},
							Efforts:     append([]string(nil), efforts...),
						})
					}
				}
			}

			if mode == config.ExposePrefix || mode == config.ExposeBoth {
				prefID := acct.EffectivePrefix() + m
				if !seenDirect[prefID] {
					seenDirect[prefID] = true
					direct = append(direct, ModelEntry{
						ID:          prefID,
						Backend:     acct.Backend,
						AccountID:   acct.ID,
						AccountName: acct.Name,
						Upstream:    m,
						Kind:        acct.Kind,
						Prefixed:    true,
						AccountIDs:  []string{acct.ID},
						Efforts:     append([]string(nil), efforts...),
					})
				}
			}
		}
	}

	var all []ModelEntry
	bareExposed := make(map[string]bool)
	for m, e := range bare {
		if len(bareBackends[m]) == 1 && !seenDirect[m] {
			bareExposed[m] = true
			all = append(all, *e)
		}
	}
	for _, e := range pooled {
		if r.cfg.Catalog.HideBackendQualified && bareExposed[e.Upstream] {
			continue
		}
		all = append(all, *e)
	}
	all = append(all, direct...)

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

	// 2. Check prefixed account model form: <prefix><model> (e.g. "a-gemini-4-neon")
	for i := range r.cfg.Accounts {
		a := &r.cfg.Accounts[i]
		if a.ExposeMode != config.ExposePrefix && a.ExposeMode != config.ExposeBoth {
			continue
		}
		pref := a.EffectivePrefix()
		if pref != "" && strings.HasPrefix(canonical, pref) {
			upstream := strings.TrimPrefix(canonical, pref)
			if upstream != "" {
				return &Plan{
					RequestedModel: requested,
					CanonicalModel: canonical,
					UpstreamModel:  upstream,
					Backend:        a.Backend,
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

	// 3. Check explicit manual form: <backend>/<account>/<model...>
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

	// 4. Check <backend>/<model...> form.
	if len(parts) >= 2 {
		backendID := parts[0]
		upstream := strings.Join(parts[1:], "/")
		matchedAccounts := r.accountsForBackendLocked(backendID, upstream)
		if len(matchedAccounts) > 0 {
			rule := r.matchRuleLocked(canonical, backendID, matchedAccounts)
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

	// 5. Fallback: match bare model name (e.g. "gemini-4-neon") across accounts exposing it in pool/both mode.
	var matched []*config.AccountConfig
	for i := range r.cfg.Accounts {
		a := &r.cfg.Accounts[i]
		if a.ExposeMode == config.ExposePrefix {
			continue
		}
		found := false
		for _, m := range a.Models {
			if m == canonical {
				found = true
				break
			}
			// Also match collapsed base model when only effort variants were listed.
			if base, eff, _ := SplitModelEffort(m); eff != "" && base == canonical {
				found = true
				break
			}
		}
		if found {
			matched = append(matched, a)
		}
	}
	if len(matched) > 0 {
		rule := r.matchRuleLocked(canonical, matched[0].Backend, matched)
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
	return anyInBackend
}

func (r *Router) matchRuleLocked(model, backendID string, accounts []*config.AccountConfig) config.RoutingRule {
	// Match against both requested model (e.g. "gemini-4-neon") and qualified "<backend>/<model>" (e.g. "agy/gemini-4-neon").
	targets := []string{model}
	if backendID != "" && !strings.HasPrefix(model, backendID+"/") {
		targets = append(targets, backendID+"/"+model)
	}

	var best *config.RoutingRule
	bestScore := -1
	for i := range r.cfg.Routing {
		rule := &r.cfg.Routing[i]
		for _, t := range targets {
			if rule.Pattern == t {
				return *rule
			}
			matched, err := path.Match(rule.Pattern, t)
			if !matched || err != nil {
				if strings.HasSuffix(rule.Pattern, "*") {
					prefix := strings.TrimSuffix(rule.Pattern, "*")
					matched = strings.HasPrefix(t, prefix)
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
	}
	if best != nil {
		return *best
	}
	ids := make([]string, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.ID)
	}
	return config.RoutingRule{
		Pattern:                     model,
		Accounts:                    ids,
		Strategy:                    config.StrategyFailover,
		AllowCrossAccountRetryOn429: true,
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
