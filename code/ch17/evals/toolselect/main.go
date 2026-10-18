// Command toolselect is chapter 7's smoke test: does the model pick the
// right tool? It sends each short request in cases to each model,
// several times, and scores the first tool the model asks for. With
// OPENROUTER_API_KEY set it calls the live API (and -record saves the
// calls); without it, it replays the saved calls.
//
// It catches large mistakes, such as a misleading description or two
// tools with overlapping names. It can't detect small improvements;
// chapter 13's evals can.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch17/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch17/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch17/replay"
	"github.com/blackwell-systems/go-agents-book/code/ch17/triage"
)

//go:embed testdata/replies
var saved embed.FS

var defaultModels = []string{
	"anthropic/claude-haiku-5.5",
	"openai/gpt-5.6-luna",
	"z-ai/glm-5.3-flash",
	"deepseek/deepseek-v4.1-flash",
}

func main() {
	runs := flag.Int("runs", 3, "times to send each request")
	models := flag.String("models", strings.Join(defaultModels, ","),
		"models to test, comma-separated")
	more := flag.Bool("distractors", false,
		"add 30 plausible tools the agent doesn't have")
	record := flag.String("record", "", "save each call to this directory")
	flag.Parse()
	ctx := context.Background()
	defs, err := toolDefs(*more)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d tools, %d requests, %d runs each\n\n", len(defs), len(cases), *runs)
	for model := range strings.SplitSeq(*models, ",") {
		c, err := client(model, *record)
		if err != nil {
			log.Fatal(err)
		}
		score, misses, err := smoke(ctx, c, defs, *runs, *more)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%-30s %5.1f of %d\n", model, score, len(cases)**runs)
		for _, m := range misses {
			fmt.Printf("    %s\n", m)
		}
	}
}

// tag::smoke[]

// smoke sends every case runs times and adds up the scores. It returns
// a line for each answer that scored below 1.
func smoke(ctx context.Context, m agent.Model, defs []agent.ToolDef,
	runs int, more bool) (float64, []string, error) {
	total := 0.0
	var misses []string
	for i, tc := range cases {
		for run := range runs {
			ctx := replay.WithScript(ctx, script(more, i, run)) // <1>
			reply, err := m.Generate(ctx, agent.Request{MaxTokens: 2000,
				Tools: defs, Messages: []agent.Message{
					{Role: "system", Content: triage.Prompt},
					{Role: "user", Content: tc.ask},
				}})
			if err != nil {
				return 0, nil, fmt.Errorf("case %d: %w", i+1, err)
			}
			got := ""
			if calls := reply.Message.ToolCalls; len(calls) > 0 {
				got = calls[0].Name // <2>
			}
			s := score(tc, got)
			total += s
			if s < 1 {
				misses = append(misses, fmt.Sprintf("%q: got %s, want %s (%.1f)",
					short(tc.ask), cmpName(got), cmpName(tc.best), s))
			}
		}
	}
	return total, misses, nil
}

// tag::score[]

// score is 1 for the best tool, 0.5 for an acceptable one, 0 otherwise.
func score(tc testCase, got string) float64 {
	switch {
	case got == tc.best:
		return 1
	case got != "" && slices.Contains(tc.ok, got):
		return 0.5
	}
	return 0
}

// end::score[]
// end::smoke[]

func cmpName(name string) string {
	if name == "" {
		return "no tool"
	}
	return name
}

func short(s string) string {
	if len(s) > 50 {
		return s[:50] + "..."
	}
	return s
}

// script names one call's saved reply.
func script(more bool, i, run int) string {
	v := "plain"
	if more {
		v = "distractors"
	}
	return fmt.Sprintf("%s-%02d-%d", v, i+1, run+1)
}

// toolDefs returns the triage tools' definitions, and the distractors
// if asked. The tools work on an empty workspace, since none of them
// runs here.
func toolDefs(more bool) ([]agent.ToolDef, error) {
	dir, err := os.MkdirTemp("", "toolselect-")
	if err != nil {
		return nil, fmt.Errorf("tool definitions: %w", err)
	}
	defer os.RemoveAll(dir)
	env, err := triage.NewEnv(dir, dir, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("tool definitions: %w", err)
	}
	defer env.Close()
	var defs []agent.ToolDef
	for _, t := range triage.Tools(env) {
		defs = append(defs, t.Def)
	}
	if more {
		defs = append(defs, distractorDefs()...)
	}
	return defs, nil
}

// client returns a client for model: live with a key, recording if
// asked, and replaying the saved calls without one.
func client(model, record string) (*openrouter.Client, error) {
	c := &openrouter.Client{APIKey: os.Getenv("OPENROUTER_API_KEY"), Model: model}
	slug := strings.ReplaceAll(model, "/", "_")
	switch {
	case c.APIKey == "":
		files, err := fs.Sub(saved, "testdata/replies/"+slug)
		if err != nil {
			return nil, fmt.Errorf("saved replies: %w", err)
		}
		c.HTTP = &http.Client{Transport: &replay.Transport{Files: files}}
	case record != "":
		c.HTTP = &http.Client{Transport: &replay.Recorder{Dir: record + "/" + slug}}
	}
	return c, nil
}
