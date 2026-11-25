package usage

import (
	"testing"
	"time"
)

func TestTrackerPersistenceAndAggregations(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 10, 14, 30, 0, 0, time.UTC)

	trk := New(dir)
	trk.Record(Record{Time: now.Add(-30 * time.Minute), Account: "agy/a", Backend: "agy", Model: "gemini-4-neon", Endpoint: "/v1/chat/completions", Status: 200, DurationMs: 400, PromptTokens: 100, CompletionTokens: 50})
	trk.Record(Record{Time: now.Add(-10 * time.Minute), Account: "agy/b", Backend: "agy", Model: "gemini-4-neon", Endpoint: "/v1/chat/completions", Status: 200, DurationMs: 200, PromptTokens: 60, CompletionTokens: 40})
	trk.Record(Record{Time: now.Add(-2 * time.Minute), Account: "deepseek/default", Backend: "deepseek", Model: "deepseek-chat", Endpoint: "/v1/messages", Status: 502, DurationMs: 100})
	if err := trk.Close(); err != nil {
		t.Fatal(err)
	}

	// Re-open tracker from disk to verify JSONL persistence
	trk2 := New(dir)
	defer trk2.Close()
	sum := trk2.SummaryAt(now)
	if sum.TotalRequests != 3 || sum.SuccessRequests != 2 || sum.ErrorRequests != 1 {
		t.Fatalf("unexpected totals: %+v", sum)
	}
	if sum.TotalTokens != 250 || sum.PromptTokens != 160 || sum.CompletionTokens != 90 {
		t.Fatalf("unexpected token totals: %+v", sum)
	}
	if len(sum.ByAccount) != 3 || len(sum.ByModel) != 2 {
		t.Fatalf("unexpected breakdown counts: accts=%d models=%d", len(sum.ByAccount), len(sum.ByModel))
	}
	if sum.ByModel[0].Model != "gemini-4-neon" || sum.ByModel[0].Requests != 2 {
		t.Fatalf("expected gemini-4-neon top model, got %+v", sum.ByModel)
	}
	if len(sum.Hourly24h) != 24 || len(sum.Daily14d) != 14 {
		t.Fatalf("unexpected bucket lengths: hourly=%d daily=%d", len(sum.Hourly24h), len(sum.Daily14d))
	}
}
