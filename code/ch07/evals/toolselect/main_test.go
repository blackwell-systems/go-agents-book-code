package main

import (
	"testing"
)

// TestScores replays the recorded runs and checks the scores the
// chapter quotes, with and without the extra tools.
func TestScores(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	want := map[bool]map[string]float64{
		false: {"anthropic/claude-haiku-5.5": 33.5, "openai/gpt-5.6-luna": 36,
			"z-ai/glm-5.3-flash": 34.5, "deepseek/deepseek-v4.1-flash": 30.5},
		true: {"anthropic/claude-haiku-5.5": 35.5, "openai/gpt-5.6-luna": 36,
			"z-ai/glm-5.3-flash": 33, "deepseek/deepseek-v4.1-flash": 30.5},
	}
	for _, more := range []bool{false, true} {
		defs, err := toolDefs(more)
		if err != nil {
			t.Fatal(err)
		}
		for model, score := range want[more] {
			c, err := client(model, "")
			if err != nil {
				t.Fatal(err)
			}
			got, _, err := smoke(t.Context(), c, defs, 3, more)
			if err != nil {
				t.Fatal(err)
			}
			if got != score {
				t.Errorf("%s, distractors %v: %.1f, want %.1f", model, more, got, score)
			}
		}
	}
}
