package usage

import "strings"

type modelRate struct {
	match        string
	inputPer1M   float64
	outputPer1M  float64
}

// Reference USD per 1M token list rates used strictly for cost *estimates* when provider
// billing data is incomplete or unavailable. Ordered most-specific first.
var referenceRates = []modelRate{
	{match: "claude-opus", inputPer1M: 15.0, outputPer1M: 75.0},
	{match: "claude-sonnet", inputPer1M: 3.0, outputPer1M: 15.0},
	{match: "claude-haiku", inputPer1M: 0.80, outputPer1M: 4.0},
	{match: "gpt-4o-mini", inputPer1M: 0.15, outputPer1M: 0.60},
	{match: "gpt-4o", inputPer1M: 2.50, outputPer1M: 10.0},
	{match: "gpt-5-mini", inputPer1M: 0.25, outputPer1M: 2.0},
	{match: "gpt-5", inputPer1M: 1.25, outputPer1M: 10.0},
	{match: "o3-mini", inputPer1M: 1.10, outputPer1M: 4.40},
	{match: "o4-mini", inputPer1M: 1.10, outputPer1M: 4.40},
	{match: "o3", inputPer1M: 2.0, outputPer1M: 8.0},
	{match: "gemini-2.5-flash", inputPer1M: 0.15, outputPer1M: 0.60},
	{match: "gemini-2.5-pro", inputPer1M: 1.25, outputPer1M: 10.0},
	{match: "gemini-3", inputPer1M: 1.25, outputPer1M: 10.0},
	{match: "gemini-4", inputPer1M: 1.25, outputPer1M: 10.0},
	{match: "deepseek-reasoner", inputPer1M: 0.55, outputPer1M: 2.19},
	{match: "deepseek-r1", inputPer1M: 0.55, outputPer1M: 2.19},
	{match: "deepseek-chat", inputPer1M: 0.27, outputPer1M: 1.10},
	{match: "deepseek", inputPer1M: 0.27, outputPer1M: 1.10},
	{match: "grok-3-mini", inputPer1M: 0.30, outputPer1M: 0.50},
	{match: "grok", inputPer1M: 3.0, outputPer1M: 15.0},
	{match: "text-embedding-3-large", inputPer1M: 0.13, outputPer1M: 0},
	{match: "text-embedding-3-small", inputPer1M: 0.02, outputPer1M: 0},
	{match: "embedding", inputPer1M: 0.05, outputPer1M: 0},
	{match: "rerank", inputPer1M: 0.05, outputPer1M: 0},
}

// EstimateCostUSD returns an estimated USD cost based on reference public list rates.
// The second return value reports whether the model matched a known reference rate.
func EstimateCostUSD(model string, promptTokens, completionTokens int) (float64, bool) {
	if promptTokens <= 0 && completionTokens <= 0 {
		return 0, false
	}
	lower := strings.ToLower(strings.TrimSpace(model))
	for _, r := range referenceRates {
		if strings.Contains(lower, r.match) {
			usd := (float64(promptTokens)*r.inputPer1M + float64(completionTokens)*r.outputPer1M) / 1_000_000.0
			return usd, true
		}
	}
	return 0, false
}
