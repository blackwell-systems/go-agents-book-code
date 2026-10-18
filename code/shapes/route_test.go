package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
)

var errHandler = errors.New("handler failed")

// echoAs returns a handler that reports its name and input.
func echoAs(name string) Handler {
	return func(_ context.Context, in string) (string, error) {
		return name + ": " + in, nil
	}
}

func TestRoute(t *testing.T) {
	handlers := map[string]Handler{
		"crash":    echoAs("crash"),
		"question": echoAs("question"),
		"broken": func(context.Context, string) (string, error) {
			return "", errHandler
		},
	}
	tests := []struct {
		name     string
		reply    agent.Reply // the classifier's
		fallback Handler
		wantKind string
		wantOut  string
		wantErr  error
	}{
		{
			name:     "a kind with a handler",
			reply:    says("crash"),
			wantKind: "crash",
			wantOut:  "crash: the input",
		},
		{
			name:     "case, space, and punctuation are ignored",
			reply:    says(" Question.\n"),
			wantKind: "question",
			wantOut:  "question: the input",
		},
		{
			name:     "any other reply goes to the fallback",
			reply:    says("other"),
			fallback: echoAs("fallback"),
			wantKind: "",
			wantOut:  "fallback: the input",
		},
		{
			name:    "no fallback",
			reply:   says("spam"),
			wantErr: ErrNoRoute,
		},
		{
			name:     "a handler's error",
			reply:    says("broken"),
			wantKind: "broken",
			wantErr:  errHandler,
		},
		{
			name:    "the classifier's reply was cut off",
			reply:   cut("cra"),
			wantErr: agent.ErrOutputTruncated,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Router{
				Classifier: replies(tt.reply),
				Handlers:   handlers,
				Fallback:   tt.fallback,
			}
			kind, out, err := r.Route(t.Context(), "the input")
			if kind != tt.wantKind || out != tt.wantOut ||
				!errors.Is(err, tt.wantErr) {
				t.Errorf("Route = %q, %q, %v; want %q, %q, %v",
					kind, out, err, tt.wantKind, tt.wantOut, tt.wantErr)
			}
		})
	}
}

// TestRouteRequest checks what the classifier receives: the kinds in
// the system message, sorted, and the input alone in the user message.
func TestRouteRequest(t *testing.T) {
	m := script("b")
	r := Router{Classifier: m, Handlers: map[string]Handler{
		"b": echoAs("b"), "a": echoAs("a"), "c": echoAs("c"),
	}}
	if _, _, err := r.Route(t.Context(), "the input"); err != nil {
		t.Fatal(err)
	}
	msgs := m.reqs[0].Messages
	if len(msgs) != 2 || msgs[0].Role != "system" ||
		!strings.Contains(msgs[0].Content, "one of: a, b, c.") {
		t.Fatalf("classifier request = %+v", msgs)
	}
	if msgs[1].Role != "user" || msgs[1].Content != "the input" {
		t.Errorf("user message = %+v, want the input alone", msgs[1])
	}
}
