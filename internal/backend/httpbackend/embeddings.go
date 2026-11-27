package httpbackend

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/bberka/agentpassage/internal/backend"
	"github.com/bberka/agentpassage/internal/config"
)

// Embed forwards an OpenAI-compatible POST /v1/embeddings request to the upstream account.
func (c *Client) Embed(ctx context.Context, acct config.AccountConfig, apiKey, upstreamModel string, rawBody []byte) ([]byte, backend.Usage, error) {
	bodyBytes, err := rewriteModelField(rawBody, upstreamModel)
	if err != nil {
		return nil, backend.Usage{}, err
	}
	resp, err := c.post(ctx, acct, apiKey, "/embeddings", bodyBytes, "")
	if err != nil {
		return nil, backend.Usage{}, err
	}
	defer resp.Body.Close()

	respBody, err := readBody(resp, 32<<20)
	if err != nil {
		return nil, backend.Usage{}, err
	}

	var meta struct {
		Usage backend.Usage `json:"usage"`
	}
	_ = json.Unmarshal(respBody, &meta)
	if meta.Usage.TotalTokens == 0 && meta.Usage.PromptTokens > 0 {
		meta.Usage.TotalTokens = meta.Usage.PromptTokens
	}
	return respBody, meta.Usage, nil
}

// Rerank forwards a Cohere/Jina/vLLM-compatible POST /v1/rerank request to the upstream account.
func (c *Client) Rerank(ctx context.Context, acct config.AccountConfig, apiKey, upstreamModel string, rawBody []byte) ([]byte, backend.Usage, error) {
	bodyBytes, err := rewriteModelField(rawBody, upstreamModel)
	if err != nil {
		return nil, backend.Usage{}, err
	}
	resp, err := c.post(ctx, acct, apiKey, "/rerank", bodyBytes, "")
	if err != nil {
		return nil, backend.Usage{}, err
	}
	defer resp.Body.Close()

	respBody, err := readBody(resp, 16<<20)
	if err != nil {
		return nil, backend.Usage{}, err
	}

	var parsed struct {
		Usage backend.Usage `json:"usage"`
		Meta  struct {
			Tokens struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"tokens"`
		} `json:"meta"`
	}
	_ = json.Unmarshal(respBody, &parsed)
	u := parsed.Usage
	if u.TotalTokens == 0 && (parsed.Meta.Tokens.InputTokens > 0 || parsed.Meta.Tokens.OutputTokens > 0) {
		u.PromptTokens = parsed.Meta.Tokens.InputTokens
		u.CompletionTokens = parsed.Meta.Tokens.OutputTokens
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return respBody, u, nil
}

func rewriteModelField(rawBody []byte, upstreamModel string) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(rawBody, &m); err != nil {
		return nil, fmt.Errorf("invalid JSON body: %w", err)
	}
	m["model"] = upstreamModel
	return json.Marshal(m)
}
