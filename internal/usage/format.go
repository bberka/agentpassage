package usage

import (
	"fmt"
	"strconv"
)

// FormatCount abbreviates a token or request count (5800 -> "5.8k",
// 443940686 -> "443.9M"). The web usage page's fmtNum matches it.
func FormatCount(n int) string {
	// Each limit is where the smaller unit would round up to "1000.0".
	switch {
	case n >= 999_950_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 999_950:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return strconv.Itoa(n)
	}
}

// FormatLatency shortens a duration in milliseconds ("450ms", "3.2s",
// "5m 17s", "1h 4m"). The web usage page's fmtDur matches it.
func FormatLatency(ms int64) string {
	switch {
	case ms < 1_000:
		return fmt.Sprintf("%dms", ms)
	case ms < 59_950:
		return fmt.Sprintf("%.1fs", float64(ms)/1e3)
	}
	sec := (ms + 500) / 1_000
	if sec < 3600 {
		return fmt.Sprintf("%dm %ds", sec/60, sec%60)
	}
	return fmt.Sprintf("%dh %dm", sec/3600, sec%3600/60)
}
