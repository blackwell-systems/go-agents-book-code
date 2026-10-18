package openrouter

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/blackwell-systems/go-agents-book/code/ch13/agent"
)

// tag::generation[]

// Generation asks OpenRouter what a generation used and cost, by the
// ID every reply and every failed stream carries (Reply.ID). It works
// for a stream that broke before its usage arrived. A generation not
// yet recorded is a 404 *APIError: ask again a little later.
func (c *Client) Generation(ctx context.Context,
	id string) (agent.Usage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL()+"/generation?id="+url.QueryEscape(id), nil)
	if err != nil {
		return agent.Usage{}, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return agent.Usage{}, fmt.Errorf("look up %s: %w", id,
			callError(ctx, err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return agent.Usage{}, fmt.Errorf("read generation: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return agent.Usage{}, statusError(resp, body)
	}
	var g generation
	if err := json.Unmarshal(body, &g); err != nil {
		return agent.Usage{}, fmt.Errorf("decode generation: %w", err)
	}
	return g.usage(), nil
}

// end::generation[]

// generation is the part of GET /generation's answer the book uses.
type generation struct {
	Data struct {
		Prompt     int     `json:"native_tokens_prompt"`
		Completion int     `json:"native_tokens_completion"`
		Cached     int     `json:"native_tokens_cached"`
		Cost       float64 `json:"total_cost"`
	} `json:"data"`
}

func (g generation) usage() agent.Usage {
	return agent.Usage{PromptTokens: g.Data.Prompt,
		CompletionTokens: g.Data.Completion,
		CachedTokens:     g.Data.Cached, Cost: g.Data.Cost}
}
