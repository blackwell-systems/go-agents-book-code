package openrouter

import (
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
)

// TestReasoningBudget checks that a reasoning budget is sent as
// reasoning.max_tokens, and that a request without one sends nothing,
// leaving the model's default.
func TestReasoningBudget(t *testing.T) {
	c := &Client{Model: "m"}
	for _, tc := range []struct {
		budget int
		want   string // in the request, or "" for no reasoning field
	}{
		{4000, `"reasoning":{"max_tokens":4000}`},
		{0, ""},
	} {
		b, err := json.Marshal(c.wireRequest(agent.Request{
			MaxTokens: 8000, ReasoningBudget: tc.budget}))
		if err != nil {
			t.Fatal(err)
		}
		got := string(b)
		if tc.want == "" && strings.Contains(got, `"reasoning"`) ||
			tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("budget %d: request is %s", tc.budget, got)
		}
	}
}
