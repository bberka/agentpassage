package usage_test

import (
	"testing"

	"github.com/bberka/agentpassage/internal/usage"
)

func TestFormatCount(t *testing.T) {
	for n, want := range map[int]string{
		0: "0", 999: "999", 1000: "1.0k", 5800: "5.8k",
		999_949: "999.9k", 999_950: "1.0M", 443_940_686: "443.9M",
		999_950_000: "1.0B", 2_340_000_000: "2.3B",
	} {
		if got := usage.FormatCount(n); got != want {
			t.Errorf("FormatCount(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestFormatLatency(t *testing.T) {
	for ms, want := range map[int64]string{
		0: "0ms", 999: "999ms", 1000: "1.0s", 59_949: "59.9s",
		59_950: "1m 0s", 316_991: "5m 17s", 3_599_500: "1h 0m", 3_840_000: "1h 4m",
	} {
		if got := usage.FormatLatency(ms); got != want {
			t.Errorf("FormatLatency(%d) = %q, want %q", ms, got, want)
		}
	}
}
