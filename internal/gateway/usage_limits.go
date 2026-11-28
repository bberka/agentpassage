package gateway

import (
	"fmt"
	"time"

	"github.com/bberka/agentpassage/internal/alerts"
	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/limits"
	"github.com/bberka/agentpassage/internal/pool"
	"github.com/bberka/agentpassage/internal/quota"
	"github.com/bberka/agentpassage/internal/router"
)

// observeLimits counts new quota readings against the usage limits and notifies
// once about each limit that became reached.
func (s *Server) observeLimits(rep quota.Report) {
	cfg := s.Config()
	now := time.Now()
	if err := s.limits.Observe(cfg, rep, now); err != nil {
		s.warnLimits(err)
		return
	}
	reached, err := s.limits.Announce(cfg, now)
	if err != nil {
		s.warnLimits(err)
	}
	for _, st := range reached {
		acct := cfg.FindAccount(st.AccountID)
		if s.alerts == nil || acct == nil {
			continue
		}
		ends := now.Add(st.EndsIn)
		s.alerts.Notify("limit:"+st.AccountID+":"+st.Name(), alerts.Event{
			Kind:     "usage_limit",
			Severity: "warning",
			Account:  st.AccountID,
			Title:    fmt.Sprintf("%s/%s: %s %s limit reached", acct.Backend, acct.Name, st.Period(), st.Name()),
			Message:  quota.FormatResetIn(&ends, now) + " left",
		})
	}
}

func (s *Server) warnLimits(err error) {
	if s.logs != nil {
		s.logs.Warn("limits", err.Error())
	}
}

// limitBlocks returns the plan's accounts that a usage limit keeps unused.
func (s *Server) limitBlocks(cfg *config.Config, plan *router.Plan, now time.Time) map[string]pool.LimitBlock {
	s.observeLimits(s.routingQuotaReport())
	list, err := s.limits.Statuses(cfg, now)
	if err != nil {
		s.warnLimits(err)
		return nil
	}
	var out map[string]pool.LimitBlock
	for _, id := range plan.CandidateIDs {
		acct := cfg.FindAccount(id)
		if acct == nil {
			continue
		}
		if st, ok := limits.Blocking(list, id, acct.Backend, plan.UpstreamModel); ok {
			if out == nil {
				out = make(map[string]pool.LimitBlock)
			}
			out[id] = pool.LimitBlock{Reason: st.Period() + " " + st.Name() + " limit reached", Until: now.Add(st.EndsIn)}
		}
	}
	return out
}

// UsageLimits lists the usage limits that apply, with the use counted so far.
func (s *Server) UsageLimits() ([]limits.Status, error) {
	s.observeLimits(s.routingQuotaReport())
	return s.limits.Statuses(s.Config(), time.Now())
}

// AllowMore raises the account's reached limits for the current period.
func (s *Server) AllowMore(accountID string) (int, error) {
	return s.limits.Allow(s.Config(), accountID, time.Now())
}
