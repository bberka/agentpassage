package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bberka/agentpassage/internal/config"
	"github.com/bberka/agentpassage/internal/descriptors"
	"github.com/bberka/agentpassage/internal/quota"
	"github.com/bberka/agentpassage/internal/secrets"
)

func TestRequestsMoveOffAnAccountAtItsUsageLimit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Alerts.Enabled = false
	cfg.Limits.Balance = 1
	for _, name := range []string{"a", "b"} {
		cfg.Accounts = append(cfg.Accounts, config.AccountConfig{
			ID: "deepseek-" + name, Name: name, Kind: config.KindAPIKey, Backend: "deepseek", Enabled: true,
			BaseURL: upstream.URL, AuthStyle: config.AuthNone, Models: []string{"deepseek-chat"}, MaxConcurrency: 4,
		})
	}
	reg, _ := descriptors.LoadRegistry(dir)
	s := New(dir, cfg, secrets.NewFileOnly(dir), reg, nil)

	balances := func(a, b float64) {
		rep := quota.Report{}
		for id, amount := range map[string]float64{"deepseek-a": a, "deepseek-b": b} {
			rep.Accounts = append(rep.Accounts, quota.AccountQuota{AccountID: id, Backend: "deepseek", Source: "deepseek-live",
				CheckedAt: time.Now(), Balances: []quota.Balance{{Currency: "USD", Amount: amount}}})
		}
		s.mu.Lock()
		s.remoteQuota = &rep
		s.mu.Unlock()
		s.observeLimits(rep)
	}
	ask := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
			strings.NewReader(`{"model":"deepseek/deepseek-chat","messages":[{"role":"user","content":"hi"}]}`))
		req.RemoteAddr = "127.0.0.1:1000"
		req.Header.Set("Authorization", "Bearer "+cfg.Server.LocalKey)
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, req)
		return rec
	}

	balances(10, 10)
	if rec := ask(); rec.Code != 200 || rec.Header().Get("X-Apx-Account") != "deepseek/a" {
		t.Fatalf("before the limit: %d %s %s", rec.Code, rec.Header().Get("X-Apx-Account"), rec.Body)
	}
	balances(8.5, 10)
	if rec := ask(); rec.Code != 200 || rec.Header().Get("X-Apx-Account") != "deepseek/b" {
		t.Fatalf("first account at its limit: %d %s %s", rec.Code, rec.Header().Get("X-Apx-Account"), rec.Body)
	}
	balances(8.5, 8)
	rec := ask()
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "daily balance limit reached, ") {
		t.Fatalf("every account at its limit: %d %s", rec.Code, rec.Body)
	}
	if n, err := s.AllowMore("deepseek-a"); n != 1 || err != nil {
		t.Fatalf("allow = %d, %v", n, err)
	}
	if rec := ask(); rec.Code != 200 {
		t.Fatalf("after allowing more: %d %s", rec.Code, rec.Body)
	}
}
