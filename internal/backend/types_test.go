package backend

import "testing"

func TestParseResetDurationSeconds(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want int
	}{
		{
			name: "compact compound with preceding window mention",
			msg:  "You have hit your 5h usage limit. Resets in 48m24s.",
			want: 48*60 + 24,
		},
		{
			name: "compact hms compound",
			msg:  "Rate limit resets in 2h15m30s",
			want: 2*3600 + 15*60 + 30,
		},
		{
			name: "hours and minutes spaced",
			msg:  "Rate limit exceeded. Please try again in 2h 15m.",
			want: 2*3600 + 15*60,
		},
		{
			name: "retry after seconds",
			msg:  "HTTP 429 Too Many Requests: retry after 90s",
			want: 90,
		},
		{
			name: "retry-after bare integer seconds",
			msg:  "rate limit hit (retry-after: 120)",
			want: 120,
		},
		{
			name: "english words with and",
			msg:  "Quota exhausted, try again in 1 hour and 30 minutes",
			want: 5400,
		},
		{
			name: "capped at 6 hours",
			msg:  "Monthly quota reached. Resets in 12d 4h",
			want: 6 * 3600,
		},
		{
			name: "window mention without reset cue returns zero",
			msg:  "Rate limit exceeded on 5h window",
			want: 0,
		},
		{
			name: "empty",
			msg:  "",
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseResetDurationSeconds(tc.msg); got != tc.want {
				t.Fatalf("ParseResetDurationSeconds(%q) = %d, want %d", tc.msg, got, tc.want)
			}
		})
	}
}
