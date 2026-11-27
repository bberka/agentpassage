package usage

import (
	"regexp"
	"strings"
	"time"
)

// Rate is a public list price in USD per million tokens.
type Rate struct {
	Input, CacheRead, CacheWrite, Output float64
	// Prompts longer than LongAbove tokens are billed entirely at Long.
	LongAbove int
	Long      *Rate
}

// Tokens is what one request used. Prompt counts all input, including the
// cached part that CacheRead and CacheWrite break out.
type Tokens struct {
	Prompt, CacheRead, CacheWrite, Completion int
}

// PricesChecked is when the table below was last compared with the providers'
// pricing pages:
//   - platform.claude.com (Anthropic; cache writes at the 5-minute rate)
//   - developers.openai.com/api/docs/pricing (standard tier)
//   - ai.google.dev/gemini-api/docs/pricing (paid tier, text)
//   - api-docs.deepseek.com/quick_start/pricing
//   - docs.x.ai/docs/models
const PricesChecked = "2026-11-27"

func flat(in, cacheRead, cacheWrite, out float64) Rate {
	return Rate{Input: in, CacheRead: cacheRead, CacheWrite: cacheWrite, Output: out}
}

func tiered(base Rate, above int, long Rate) Rate {
	base.LongAbove, base.Long = above, &long
	return base
}

// openAI56 is the pricing shape of GPT-5.6 and later: cache writes cost 1.25x
// input, and prompts over 272K cost 2x input and 1.5x output.
func openAI56(in, cacheRead, out float64) Rate {
	return tiered(flat(in, cacheRead, 1.25*in, out), 272_000, flat(2*in, 2*cacheRead, 2.5*in, 1.5*out))
}

func grok(in, cacheRead, out float64) Rate {
	return tiered(flat(in, cacheRead, in, out), 200_000, flat(2*in, 2*cacheRead, 2*in, 2*out))
}

// prices maps exact model IDs to list prices. A model that is not listed has no
// cost estimate: guessing from a similar name gives wrong totals.
var prices = map[string]Rate{
	// Anthropic
	"claude-fable-5-2":  flat(8, 0.20, 10, 40),
	"claude-mythos-5-2": flat(8, 0.20, 10, 40),
	"claude-fable-5-1":  flat(10, 0.25, 12.5, 50),
	"claude-mythos-5-1": flat(10, 0.25, 12.5, 50),
	"claude-fable-5":    flat(10, 1, 12.5, 50),
	"claude-opus-5-5":   flat(4, 0.20, 5, 20),
	"claude-opus-5":     flat(5, 0.50, 6.25, 25),
	"claude-opus-4-8":   flat(5, 0.50, 6.25, 25),
	"claude-opus-4-7":   flat(5, 0.50, 6.25, 25),
	"claude-opus-4-6":   flat(5, 0.50, 6.25, 25),
	"claude-sonnet-5-5": flat(2, 0.10, 2.5, 10),
	"claude-sonnet-5":   flat(2, 0.20, 2.5, 10),
	"claude-sonnet-4-6": flat(3, 0.30, 3.75, 15),
	"claude-haiku-5-5":  tiered(flat(0.10, 0.01, 0.125, 0.50), 100_000, flat(0.50, 0.05, 0.625, 2.50)),
	"claude-haiku-4-5":  flat(1, 0.10, 1.25, 5),

	// OpenAI
	"gpt-6.2-astra": openAI56(8, 0.40, 40),
	"gpt-6.2-sol":   openAI56(2, 0.10, 10),
	"gpt-6.1-sol":   openAI56(2, 0.10, 10),
	"gpt-6.1-terra": openAI56(1, 0.05, 5),
	"gpt-6-astra":   openAI56(10, 1, 50),
	"gpt-6-sol":     openAI56(2, 0.20, 10),
	"gpt-6-luna":    openAI56(0.10, 0.01, 0.50),
	"gpt-5.6-sol":   openAI56(4, 0.40, 20),
	"gpt-5.6-terra": openAI56(2, 0.20, 12),
	"gpt-5.6-luna":  openAI56(0.20, 0.02, 1.20),
	"gpt-5.5":       tiered(flat(5, 0.50, 5, 30), 272_000, flat(10, 1, 10, 45)),
	"gpt-5.5-pro":   tiered(flat(30, 30, 30, 180), 272_000, flat(60, 60, 60, 270)),
	"gpt-5.4":       tiered(flat(2.50, 0.25, 2.50, 15), 272_000, flat(5, 0.50, 5, 22.50)),
	"gpt-5.4-pro":   tiered(flat(30, 30, 30, 180), 272_000, flat(60, 60, 60, 270)),
	"gpt-5.4-mini":  flat(0.75, 0.075, 0.75, 4.50),
	"gpt-5.4-nano":  flat(0.20, 0.02, 0.20, 1.25),
	"gpt-5.2":       flat(1.75, 0.175, 1.75, 14),
	"gpt-5.2-pro":   flat(21, 21, 21, 168),
	"gpt-5.1":       flat(1.25, 0.125, 1.25, 10),
	"gpt-5":         flat(1.25, 0.125, 1.25, 10),
	"gpt-5-mini":    flat(0.25, 0.025, 0.25, 2),
	"gpt-5-nano":    flat(0.05, 0.005, 0.05, 0.40),
	"gpt-5-pro":     flat(15, 15, 15, 120),
	"o3":            flat(2, 0.50, 2, 8),
	"o3-pro":        flat(20, 20, 20, 80),
	"gpt-4.1":       flat(2, 0.50, 2, 8),
	"gpt-4.1-mini":  flat(0.40, 0.10, 0.40, 1.60),
	"gpt-4o":        flat(2.50, 1.25, 2.50, 10),
	"gpt-4o-mini":   flat(0.15, 0.075, 0.15, 0.60),

	"text-embedding-3-small": flat(0.02, 0.02, 0.02, 0),
	"text-embedding-3-large": flat(0.13, 0.13, 0.13, 0),
	"text-embedding-ada-002": flat(0.10, 0.10, 0.10, 0),

	// Google
	"gemini-4-carbon":        flat(2, 0.10, 2, 10),
	"gemini-4-argon":         flat(2, 0.10, 2, 10),
	"gemini-4-neon":          flat(3, 0.15, 3, 15),
	"gemini-4-hydrogen":      flat(1.50, 0.075, 1.50, 7.50),
	"gemini-3.8-flash":       flat(0.75, 0.075, 0.75, 3.75),
	"gemini-3.6-flash":       flat(0.75, 0.075, 0.75, 3.75),
	"gemini-3.5-flash-lite":  flat(0.30, 0.03, 0.30, 2.50),
	"gemini-3.1-pro-preview": tiered(flat(2, 0.20, 2, 12), 200_000, flat(4, 0.40, 4, 18)),
	"gemini-3.1-flash-lite":  flat(0.25, 0.025, 0.25, 1.50),
	"gemini-3-flash-preview": flat(0.50, 0.05, 0.50, 3),
	"gemini-2.5-pro":         tiered(flat(1.25, 0.125, 1.25, 10), 200_000, flat(2.50, 0.25, 2.50, 15)),
	"gemini-2.5-flash":       flat(0.30, 0.03, 0.30, 2.50),
	"gemini-2.5-flash-lite":  flat(0.10, 0.01, 0.10, 0.40),

	// DeepSeek, off-peak (see deepSeekPeak)
	"deepseek-flash":  flat(0.15, 0.003, 0.15, 0.60),
	"deepseek-v4-pro": flat(0.66, 0.022, 0.66, 1.98),

	// xAI
	"grok-4.7": grok(2, 0.50, 6),
	"grok-4.6": grok(2, 0.50, 6),
	"grok-4.5": grok(2, 0.30, 6),
	"grok-4.3": grok(1.25, 0.20, 2.50),
}

// scheduled holds announced price changes.
var scheduled = []struct {
	model string
	from  time.Time
	rate  Rate
}{
	{"gemini-4-carbon", date(2026, 12, 1), flat(4, 0.20, 4, 20)},
	{"gemini-4-argon", date(2026, 12, 1), flat(4, 0.20, 4, 20)},
	{"gemini-3.8-flash", date(2027, 1, 1), flat(1.50, 0.15, 1.50, 7.50)},
	{"gemini-3.6-flash", date(2027, 1, 1), flat(1.50, 0.15, 1.50, 7.50)},
}

func date(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// aliases are the short or retired names apx and the agents use.
var aliases = map[string]string{
	"fable":                              "claude-fable-5-2",
	"opus":                               "claude-opus-5-5",
	"sonnet":                             "claude-sonnet-5-5",
	"haiku":                              "claude-haiku-5-5",
	"deepseek-v4-flash":                  "deepseek-flash",
	"deepseek-v4-flash-vision-exp":       "deepseek-flash",
	"gemini-3.1-pro-preview-customtools": "gemini-3.1-pro-preview",
}

var snapshotSuffix = regexp.MustCompile(`-20\d{2}-?\d{2}-?\d{2}$`)

var effortSuffixes = []string{"-thinking", "-low", "-medium", "-high", "-xhigh", "-max"}

// canonicalModel turns an apx model ID such as "agy/work/claude-opus-5-5-high"
// into a price table key, or "" when the model is not listed.
func canonicalModel(model string) string {
	id := strings.ToLower(strings.TrimSpace(model))
	if i := strings.LastIndex(id, "/"); i >= 0 {
		id = id[i+1:]
	}
	id = snapshotSuffix.ReplaceAllString(id, "")
	for {
		if to, ok := aliases[id]; ok {
			id = to
		}
		if _, ok := prices[id]; ok {
			return id
		}
		trimmed := id
		for _, s := range effortSuffixes {
			trimmed = strings.TrimSuffix(trimmed, s)
		}
		if trimmed == id {
			return ""
		}
		id = trimmed
	}
}

// deepSeekPeak reports DeepSeek's double-price hours: 01:00-04:00 and
// 06:00-10:00 UTC on weekdays. Chinese public holidays are not tracked.
func deepSeekPeak(at time.Time) bool {
	u := at.UTC()
	if wd := u.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false
	}
	h := u.Hour()
	return (h >= 1 && h < 4) || (h >= 6 && h < 10)
}

// EstimateCostUSD prices one request made at the given time at public list
// rates. The second result is false when the model is not in the table.
func EstimateCostUSD(model string, t Tokens, at time.Time) (float64, bool) {
	id := canonicalModel(model)
	if id == "" {
		return 0, false
	}
	rate := prices[id]
	for _, c := range scheduled {
		if c.model == id && !at.Before(c.from) {
			rate = c.rate
		}
	}
	if rate.Long != nil && t.Prompt > rate.LongAbove {
		rate = *rate.Long
	}
	fresh := max(t.Prompt-t.CacheRead-t.CacheWrite, 0)
	usd := (float64(fresh)*rate.Input + float64(t.CacheRead)*rate.CacheRead +
		float64(t.CacheWrite)*rate.CacheWrite + float64(t.Completion)*rate.Output) / 1e6
	if strings.HasPrefix(id, "deepseek-") && deepSeekPeak(at) {
		usd *= 2
	}
	return usd, true
}
