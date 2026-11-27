package gateway

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/bberka/agentpassage/internal/quota"
)

const (
	sharedQuotaPoll    = time.Minute
	localQuotaInterval = 5 * time.Minute
)

// FollowQuota keeps routing supplied with quota data in a process that does not
// run the gateway itself (`apx acp`). It reuses the running daemon's report and
// queries providers directly only when no daemon answers. It stops with ctx.
func (s *Server) FollowQuota(ctx context.Context) {
	var lastLocal time.Time
	refresh := func() {
		rep, ok := s.daemonQuota(ctx)
		s.mu.Lock()
		if ok {
			s.remoteQuota = &rep
		} else {
			s.remoteQuota = nil
		}
		s.mu.Unlock()
		if !ok && time.Since(lastLocal) >= localQuotaInterval {
			lastLocal = time.Now()
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			s.quota.RefreshAll(rctx)
		}
	}
	go func() {
		ticker := time.NewTicker(sharedQuotaPoll)
		defer ticker.Stop()
		for {
			refresh()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// daemonQuota fetches the quota report from an apx daemon on the configured address.
func (s *Server) daemonQuota(ctx context.Context) (quota.Report, bool) {
	s.mu.RLock()
	addr := net.JoinHostPort(s.cfg.Server.Bind, strconv.Itoa(s.cfg.Server.Port))
	key := s.cfg.Server.LocalKey
	s.mu.RUnlock()

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/admin/quota", nil)
	if err != nil {
		return quota.Report{}, false
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return quota.Report{}, false
	}
	defer resp.Body.Close()
	var rep quota.Report
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&rep) != nil {
		return quota.Report{}, false
	}
	return rep, true
}

// routingQuotaReport is the daemon's report when one was fetched, else local data.
func (s *Server) routingQuotaReport() quota.Report {
	s.mu.RLock()
	shared := s.remoteQuota
	s.mu.RUnlock()
	if shared != nil {
		return *shared
	}
	return s.quota.Report()
}
