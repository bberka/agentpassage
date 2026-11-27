package quota

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bberka/agentpassage/internal/config"
)

func TestParsedBalances(t *testing.T) {
	ds, err := ParseDeepSeekBalance([]byte(`{"is_available":true,"balance_infos":[
		{"currency":"CNY","total_balance":"0.00"},
		{"currency":"USD","total_balance":"14.25"},
		{"currency":"EUR","total_balance":""}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []Balance{{"CNY", 0}, {"USD", 14.25}}; !slices.Equal(ds.Balances, want) {
		t.Fatalf("deepseek balances = %+v, want %+v", ds.Balances, want)
	}

	or, err := ParseOpenRouterKey([]byte(`{"data":{"limit":10,"limit_remaining":2.5}}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := []Balance{{"USD", 2.5}}; !slices.Equal(or.Balances, want) {
		t.Fatalf("openrouter balances = %+v, want %+v", or.Balances, want)
	}
	for _, raw := range []string{`{"data":{"limit":null,"limit_remaining":null,"usage":3}}`, `{"data":{"limit":10,"usage":3}}`} {
		q, err := ParseOpenRouterKey([]byte(raw))
		if err != nil || len(q.Balances) != 0 {
			t.Fatalf("%s: want no balance, got %+v (%v)", raw, q.Balances, err)
		}
	}
}

// The report keeps the balances of a cached check.
func TestReportKeepsBalances(t *testing.T) {
	f := New(t.TempDir(), func() []config.AccountConfig {
		return []config.AccountConfig{{ID: "ds", Backend: "deepseek", Name: "main"}}
	}, nil, nil)
	f.cache["ds"] = AccountQuota{AccountID: "ds", Balances: []Balance{{"USD", 1}}}
	if got := f.Report().Accounts[0].Balances; len(got) != 1 {
		t.Fatalf("balances lost in report: %+v", got)
	}
}

// An agy run killed at its deadline (cold start) is retried once.
func TestAgyQuotaRetriesAfterTimeout(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	script := `#!/bin/sh
if [ ! -e "` + marker + `" ]; then
  : > "` + marker + `"
  exec sleep 10
fi
echo '{"quota":{"gemini-5h":{"remaining_fraction":0.5}}}'
`
	if err := os.WriteFile(filepath.Join(dir, "agy"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	old := agyUsageTimeout
	agyUsageTimeout = time.Second
	defer func() { agyUsageTimeout = old }()

	q, err := fetchAgyQuota(context.Background(), config.AccountConfig{Backend: "agy", UseHostProfile: true}, dir)
	if err != nil {
		t.Fatalf("want success on retry, got %v", err)
	}
	if q.Primary == nil && len(q.Groups) == 0 {
		t.Fatalf("no quota parsed: %+v", q)
	}
}
