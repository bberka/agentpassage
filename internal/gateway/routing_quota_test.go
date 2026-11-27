package gateway

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/quota"
)

func TestDaemonQuota(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/admin/quota" || r.Header.Get("Authorization") != "Bearer k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, quota.Report{Accounts: []quota.AccountQuota{{AccountID: "agy-bb"}}})
	}))
	defer daemon.Close()
	host, portText, _ := net.SplitHostPort(daemon.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)

	s := &Server{cfg: &config.Config{}}
	s.cfg.Server.Bind, s.cfg.Server.Port, s.cfg.Server.LocalKey = host, port, "k"
	rep, ok := s.daemonQuota(context.Background())
	if !ok || len(rep.Accounts) != 1 {
		t.Fatalf("daemonQuota = %+v, %v", rep, ok)
	}
	s.remoteQuota = &rep
	if got := s.routingQuotaReport(); len(got.Accounts) != 1 || got.Accounts[0].AccountID != "agy-bb" {
		t.Fatalf("routing did not use the daemon report: %+v", got)
	}

	s.cfg.Server.LocalKey = "wrong"
	if _, ok := s.daemonQuota(context.Background()); ok {
		t.Fatal("a rejected request must not count as a report")
	}
}
