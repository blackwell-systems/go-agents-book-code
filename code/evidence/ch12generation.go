package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch12/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch12/openrouter"
)

// Chapter 12's E5: a stream cancelled after its first piece of text
// reports no usage. How long until OpenRouter's generation lookup
// knows it, and what does the lookup say it cost?

// generationRun is one cancelled stream and its lookup.
type generationRun struct {
	Model      string  `json:"model"`
	Cancelled  bool    `json:"cancelled"` // false: read to the end
	ID         string  `json:"id"`
	TextSeen   int     `json:"text_bytes_seen"` // before cancelling
	Lookups    int     `json:"lookups"`         // 404s included
	Found      bool    `json:"found"`
	Delay      float64 `json:"delay_seconds"` // cancel to first 200
	Prompt     int     `json:"prompt_tokens"`
	Completion int     `json:"completion_tokens"`
	Cost       float64 `json:"cost"`
	Err        string  `json:"err,omitempty"`
}

// runGeneration starts e.n streams (default 3) that would write a few
// hundred lines and cancels each after its first text, plus one stream
// read to its end, which shows that the lookup works. Then it asks for
// every generation every 10 seconds for up to 20 minutes.
func runGeneration(ctx context.Context, e *env) error {
	const model = "anthropic/claude-haiku-5.5"
	c := &openrouter.Client{APIKey: e.key, Model: model}
	var runs []*generationRun
	var stopped []time.Time
	for i := range max(e.n, 3) + 1 {
		run := stream(ctx, c, i > 0)
		run.Model = model
		runs = append(runs, &run)
		stopped = append(stopped, time.Now())
	}
	for pending := len(runs); pending > 0; {
		pending = 0
		for i, run := range runs {
			if run.Found || run.Err != "" {
				continue
			}
			if time.Since(stopped[i]) > 20*time.Minute {
				run.Err = "not found within 20 minutes"
				continue
			}
			lookUp(ctx, c, run, stopped[i])
			if !run.Found && run.Err == "" {
				pending++
			}
		}
		if pending > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("look up generations: %w", context.Cause(ctx))
			case <-time.After(10 * time.Second):
			}
		}
	}
	out := &results{file: filepath.Join(e.dir, "generation.jsonl")}
	for _, run := range runs {
		e.spend.add(run.Cost)
		if err := out.add(run); err != nil {
			return err
		}
		fmt.Printf("generation cancelled=%v: %d bytes seen, found=%v "+
			"after %.0fs (%d lookups), %d+%d tokens, $%.6f %s\n",
			run.Cancelled, run.TextSeen, run.Found, run.Delay, run.Lookups,
			run.Prompt, run.Completion, run.Cost, run.Err)
	}
	return nil
}

// stream runs one stream, to its first text when cancelled is set and
// to its end otherwise, and keeps its generation ID.
func stream(ctx context.Context, c *openrouter.Client,
	cancelled bool) generationRun {
	run := generationRun{Cancelled: cancelled}
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req := agent.Request{Messages: []agent.Message{{Role: "user",
		Content: "Write the numbers from 1 to 400, one per line, " +
			"and nothing else."}}}
	for d, err := range c.Stream(sctx, req) {
		run.ID = cmp.Or(run.ID, d.ID)
		if err != nil {
			run.Err = err.Error()
			break
		}
		run.TextSeen += len(d.Text)
		if cancelled && d.Text != "" {
			break // stop reading, which closes the response
		}
	}
	if run.ID == "" && run.Err == "" {
		run.Err = "no generation ID"
	}
	return run
}

// lookUp asks once for run's generation.
func lookUp(ctx context.Context, c *openrouter.Client,
	run *generationRun, stopped time.Time) {
	run.Lookups++
	u, err := c.Generation(ctx, run.ID)
	if e, ok := errors.AsType[*openrouter.APIError](err); ok &&
		e.Status == http.StatusNotFound {
		return
	}
	if err != nil {
		run.Err = err.Error()
		return
	}
	run.Found = true
	run.Delay = round(time.Since(stopped).Seconds())
	run.Prompt, run.Completion = u.PromptTokens, u.CompletionTokens
	run.Cost = u.Cost
}

// generationSummary prints E5.
func generationSummary(w io.Writer, dir string) error {
	runs, err := load[generationRun](filepath.Join(dir, "generation.jsonl"))
	if err != nil || len(runs) == 0 {
		return err
	}
	fmt.Fprintln(w, "== generation: streams cancelled after their first "+
		"text, then looked up by ID (chapter 12)")
	fmt.Fprintln(w, "model\tcancelled\tbytes seen\tfound\tseconds to found\tlookups"+
		"\tprompt\tcompletion\tcost\terror")
	for _, r := range runs {
		fmt.Fprintf(w, "%s\t%v\t%d\t%v\t%.1f\t%d\t%d\t%d\t$%.6f\t%s\n",
			r.Model, r.Cancelled, r.TextSeen, r.Found, r.Delay, r.Lookups, r.Prompt,
			r.Completion, r.Cost, strings.TrimSpace(r.Err))
	}
	fmt.Fprintln(w)
	return nil
}
