package anthropic

import (
	_ "embed"
	"encoding/json/v2"
	"fmt"
	"os"

	"github.com/blackwell-systems/go-agents-book/code/ch14/agent"
)

// tag::prices[]

// Prices maps a model ID to its price. The Messages API reports tokens,
// not dollars, so the client prices each reply itself; the cost budget
// (chapter 5) needs a number.
type Prices map[string]Price

// A Price is in dollars per million tokens. Long, if set, replaces the
// price for a request whose prompt is over Long.Over tokens, counting
// cache reads and writes: some models charge more for every token of a
// long request.
type Price struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read"`
	CacheWrite float64 `json:"cache_write"` // a five-minute cache entry
	Long       *Tier   `json:"long,omitzero"`
}

// A Tier is the price above a prompt size.
type Tier struct {
	Over  int `json:"over"` // prompt tokens
	Price `json:",inline"`
}

// end::prices[]

// tag::cost[]

// Cost prices one reply's usage for a model, or returns 0 for a model
// the table doesn't list.
func (p Prices) Cost(model string, u agent.Usage) float64 {
	price, ok := p[model]
	if !ok {
		return 0
	}
	if t := price.Long; t != nil && u.PromptTokens > t.Over {
		price = t.Price
	}
	uncached := u.PromptTokens - u.CachedTokens - u.CacheWriteTokens
	dollars := float64(uncached)*price.Input +
		float64(u.CachedTokens)*price.CacheRead +
		float64(u.CacheWriteTokens)*price.CacheWrite +
		float64(u.CompletionTokens)*price.Output
	return dollars / 1e6
}

// end::cost[]

// LoadPrices reads a price table from a JSON file, so prices can change
// without a new build. A misspelled field is an error, not a price of
// zero.
func LoadPrices(file string) (Prices, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("load prices: %w", err)
	}
	var p Prices
	err = json.Unmarshal(b, &p, json.RejectUnknownMembers(true))
	if err != nil {
		return nil, fmt.Errorf("load prices: %s: %w", file, err)
	}
	return p, nil
}

//go:embed prices.json
var builtinPrices []byte

// DefaultPrices returns the price table built into the client, checked
// against Anthropic's and OpenRouter's price lists in October 2026.
// Prices change; a program that runs for long should load its own with
// LoadPrices.
func DefaultPrices() (Prices, error) {
	var p Prices
	if err := json.Unmarshal(builtinPrices, &p); err != nil {
		return nil, fmt.Errorf("built-in prices: %w", err)
	}
	return p, nil
}
