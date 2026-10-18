package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
)

var errShort = errors.New("too short")

// twoSteps is a chain whose first step's reply must be at least five
// characters long.
var twoSteps = []Step{{
	Name:   "draft",
	Prompt: func(in string) string { return "draft: " + in },
	Check: func(out string) error {
		if len(out) < 5 {
			return errShort
		}
		return nil
	},
}, {
	Name:   "title",
	Prompt: func(in string) string { return "title: " + in },
}}

func TestChain(t *testing.T) {
	tests := []struct {
		name      string
		replies   []agent.Reply
		want      string
		wantErr   error
		wantCalls int
	}{
		{
			name:      "every step passes",
			replies:   []agent.Reply{says("a draft"), says("a title")},
			want:      "a title",
			wantCalls: 2,
		},
		{
			name:      "a failed check stops the chain",
			replies:   []agent.Reply{says("abc"), says("a title")},
			wantErr:   errShort,
			wantCalls: 1,
		},
		{
			name:      "a cut-off reply is not an answer",
			replies:   []agent.Reply{says("a draft"), cut("a ti")},
			wantErr:   agent.ErrOutputTruncated,
			wantCalls: 2,
		},
		{
			name:      "a failed call stops the chain",
			replies:   nil,
			wantErr:   agent.ErrScriptExhausted,
			wantCalls: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := replies(tt.replies...)
			got, err := Chain(t.Context(), m, "the input", twoSteps)
			if !errors.Is(err, tt.wantErr) || got != tt.want {
				t.Errorf("Chain = %q, %v; want %q, %v",
					got, err, tt.want, tt.wantErr)
			}
			if m.calls() != tt.wantCalls {
				t.Errorf("%d calls, want %d", m.calls(), tt.wantCalls)
			}
		})
	}
}

// TestChainPassesOutputOn checks that each step's prompt is built from
// the last step's reply, and the first from the input.
func TestChainPassesOutputOn(t *testing.T) {
	m := script("a draft", "a title")
	if _, err := Chain(t.Context(), m, "the input", twoSteps); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"draft: the input", "title: a draft"} {
		if got := m.last(i); got != want {
			t.Errorf("prompt %d = %q, want %q", i, got, want)
		}
	}
}

func TestOneLine(t *testing.T) {
	check := oneLine(10)
	for _, s := range []string{"", "ten chars.", "日本語"} {
		if err := check(s); err != nil {
			t.Errorf("oneLine(10)(%q) = %v, want nil", s, err)
		}
	}
	for _, s := range []string{"eleven chars", "two\nlines"} {
		if err := check(s); err == nil {
			t.Errorf("oneLine(10)(%q) = nil, want an error", s)
		}
	}
	if !strings.Contains(check("two\nlines").Error(), "one line") {
		t.Error("the error doesn't say what was wanted")
	}
}
