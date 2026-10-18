package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
)

var update = flag.Bool("update", false, "rewrite testdata/out.txt")

// TestRun runs every demo and compares the output with testdata/out.txt.
func TestRun(t *testing.T) {
	var out bytes.Buffer
	if err := run(t.Context(), &out); err != nil {
		t.Fatal(err)
	}
	const golden = "testdata/out.txt"
	if *update {
		if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Errorf("output differs from %s:\n%s", golden, out.String())
	}
}

// says is a finished reply with text.
func says(text string) agent.Reply {
	return agent.Reply{Message: agent.Says(text), FinishReason: "stop"}
}

// cut is a reply the model stopped at its length limit.
func cut(text string) agent.Reply {
	return agent.Reply{Message: agent.Says(text), FinishReason: "length"}
}

// replies returns a scripted model with the given replies.
func replies(rs ...agent.Reply) *scripted {
	return &scripted{replies: rs}
}

// last returns the text of the last message in call i's request.
func (s *scripted) last(i int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	msgs := s.reqs[i].Messages
	return msgs[len(msgs)-1].Content
}

// blocking is a Model that waits until its call is cancelled and
// records the cause.
type blocking struct {
	cause error
}

func (b *blocking) Generate(ctx context.Context,
	_ agent.Request) (agent.Reply, error) {
	<-ctx.Done()
	b.cause = context.Cause(ctx)
	return agent.Reply{}, fmt.Errorf("wait: %w", ctx.Err())
}
