package pool

import "github.com/bberka/agentpassage/internal/config"

// CandidateExplanation describes how one candidate account was judged.
type CandidateExplanation struct {
	AccountID    string   `json:"account_id"`
	Account      string   `json:"account"` // backend/name
	Status       string   `json:"status"`  // eligible, quota used up, skipped
	Reason       string   `json:"reason,omitempty"`
	QuotaUsedPct *float64 `json:"quota_used_pct,omitempty"`
}

// Explanation is a dry run of Acquire: which account a request would get, and why.
type Explanation struct {
	Strategy   string                 `json:"strategy"`
	QuotaGroup string                 `json:"quota_group,omitempty"`
	PinnedTo   string                 `json:"pinned_to,omitempty"` // account the conversation is on
	Chosen     string                 `json:"chosen,omitempty"`    // backend/name
	ChosenID   string                 `json:"chosen_id,omitempty"`
	Why        string                 `json:"why,omitempty"`
	Error      string                 `json:"error,omitempty"`
	Candidates []CandidateExplanation `json:"candidates"`
}

// Explain runs the same selection as Acquire without reserving a slot or
// changing round-robin, sticky, or session state.
func (m *Manager) Explain(req SelectRequest) Explanation {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	strategy := normalizeStrategy(req.Strategy)
	ex := Explanation{Strategy: strategy, QuotaGroup: req.QuotaGroup}

	label := func(id string) string {
		if a, ok := m.accounts[id]; ok {
			return a.Backend + "/" + a.Name
		}
		return id
	}
	idx := make(map[string]int, len(req.CandidateIDs))
	for _, id := range req.CandidateIDs {
		c := CandidateExplanation{AccountID: id, Account: label(id)}
		if u, ok := req.QuotaUsedPct[id]; ok {
			c.QuotaUsedPct = &u
		}
		idx[id] = len(ex.Candidates)
		ex.Candidates = append(ex.Candidates, c)
	}
	note := func(id, status, reason string) {
		if i, ok := idx[id]; ok {
			ex.Candidates[i].Status = status
			ex.Candidates[i].Reason = reason
		}
	}

	if err := m.optInCheckLocked(strategy, req); err != nil {
		ex.Error = err.Error()
		return ex
	}

	pinnedID := m.pinnedLocked(req.SessionKey, req.CandidateIDs, now)
	if pinnedID != "" {
		ex.PinnedTo = label(pinnedID)
		if req.WaitForResetWhenExhausted {
			if blocked, until := m.quotaBlockLocked(pinnedID, req, now); blocked {
				ex.Error = sessionWaitError(label(pinnedID), until, now).Error()
				_, _ = m.eligibleLocked(req, pinnedID, now, note)
				return ex
			}
		}
	}

	eligible, err := m.eligibleLocked(req, pinnedID, now, note)
	if err != nil {
		ex.Error = err.Error()
		return ex
	}
	allExhausted := true
	for _, a := range eligible {
		if _, out := req.QuotaExhausted[a.ID]; !out {
			allExhausted = false
			break
		}
	}

	for _, a := range eligible {
		if a.ID == pinnedID && strategy != config.StrategyManual {
			ex.Chosen, ex.ChosenID = label(a.ID), a.ID
			ex.Why = "this conversation already runs on it"
			return ex
		}
	}
	chosen := m.pickLocked(strategy, req, eligible, false)
	ex.Chosen, ex.ChosenID = label(chosen.ID), chosen.ID
	switch {
	case allExhausted:
		ex.Why = "every account looks out of quota, so apx tries them anyway (quota data can be a few minutes old)"
	case len(eligible) == 1:
		ex.Why = "only eligible account"
	default:
		ex.Why = "picked by the " + strategy + " strategy"
	}
	return ex
}
