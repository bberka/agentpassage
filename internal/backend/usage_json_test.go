package backend

import (
	"encoding/json"
	"testing"
)

func TestUsageReadsProviderCacheFields(t *testing.T) {
	for _, raw := range []string{
		`{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":7}}`,
		`{"prompt_tokens":10,"prompt_cache_hit_tokens":7}`,
		`{"prompt_tokens":10,"cache_read_tokens":7}`,
	} {
		var u Usage
		if err := json.Unmarshal([]byte(raw), &u); err != nil || u.PromptTokens != 10 || u.CacheReadTokens != 7 {
			t.Errorf("%s: %+v %v", raw, u, err)
		}
	}
}
