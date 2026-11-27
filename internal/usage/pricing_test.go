package usage

import (
	"math"
	"testing"
	"time"
)

func TestEstimateCostUSD(t *testing.T) {
	noon := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) // Friday, DeepSeek off-peak
	million := Tokens{Prompt: 100_000, Completion: 100_000}
	for _, c := range []struct {
		model string
		tok   Tokens
		at    time.Time
		want  float64
	}{
		{"claude/opus", million, noon, 2.4},
		{"agy/work/claude-sonnet-5-5-high", million, noon, 1.2},
		{"claude-haiku-4-5-20251001", million, noon, 0.6},
		// 90K of the 100K prompt came from cache.
		{"claude-opus-5-5", Tokens{Prompt: 100_000, CacheRead: 90_000}, noon, 0.058},
		{"codex/gpt-6-luna", Tokens{Prompt: 200_000}, noon, 0.02},
		{"codex/gpt-6-luna", Tokens{Prompt: 300_000}, noon, 0.06},
		{"agy/gemini-4-carbon", million, noon, 1.2},
		{"agy/gemini-4-carbon", million, noon.AddDate(0, 0, 5), 2.4},
		{"deepseek/deepseek-flash", million, noon, 0.075},
		{"deepseek/deepseek-flash", million, time.Date(2026, 11, 27, 7, 0, 0, 0, time.UTC), 0.15},
		{"grok/grok-4.7", Tokens{Prompt: 200_001}, noon, 0.800004},
	} {
		got, ok := EstimateCostUSD(c.model, c.tok, c.at)
		if !ok || math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s %+v: got %v (%v), want %v", c.model, c.tok, got, ok, c.want)
		}
	}
	// A similar name must not borrow another model's price.
	for _, m := range []string{"grok/grok-4.7-build-fast", "agy/gpt-oss-120b-medium", "deepseek-chat", "claude-opus-9", "default"} {
		if usd, ok := EstimateCostUSD(m, million, noon); ok {
			t.Errorf("%s must be unpriced, got %v", m, usd)
		}
	}
}

func TestTrackerSeesOtherProcessesRecords(t *testing.T) {
	dir := t.TempDir()
	daemon, acp := New(dir), New(dir)
	defer daemon.Close()
	defer acp.Close()
	acp.Record(Record{Model: "claude/sonnet", PromptTokens: 1_000_000, Status: 200})
	daemon.Record(Record{Model: "nobody/unknown-model", PromptTokens: 10, Status: 200})
	sum := daemon.Summary()
	if sum.TotalRequests != 2 || sum.UnpricedRequests != 1 || math.Abs(sum.EstimatedCostUSD-2) > 1e-9 {
		t.Fatalf("got %d requests, %d unpriced, $%v", sum.TotalRequests, sum.UnpricedRequests, sum.EstimatedCostUSD)
	}
}
