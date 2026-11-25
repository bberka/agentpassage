package usage

import (
	"testing"
	"time"
)

func BenchmarkUsageSummaryCached(b *testing.B) {
	tr := New("")
	now := time.Now()
	for i := range 2000 {
		tr.Record(Record{
			Time:             now.Add(-time.Duration(i) * time.Minute),
			Account:          "deepseek/main",
			Backend:          "deepseek",
			Model:            "deepseek/deepseek-chat",
			Endpoint:         "/v1/chat/completions",
			APIKey:           "work-key",
			Project:          "work",
			Status:           200,
			DurationMs:       420,
			PromptTokens:     500,
			CompletionTokens: 200,
			TotalTokens:      700,
		})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = tr.SummaryPeriod("24h")
	}
}
