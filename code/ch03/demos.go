package main

import (
	"context"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch03/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch03/attention"
	"github.com/blackwell-systems/go-agents-book/code/ch03/bpe"
	"github.com/blackwell-systems/go-agents-book/code/ch03/chatml"
	"github.com/blackwell-systems/go-agents-book/code/ch03/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch03/replay"
	"github.com/blackwell-systems/go-agents-book/code/ch03/toy"
)

// tag::toy[]

// runToy trains the toy model on chapters 1 and 2 and lets it write.
func runToy(_ context.Context, p *printer) error {
	text, err := fs.ReadFile(files, "testdata/corpus.txt")
	if err != nil {
		return fmt.Errorf("read corpus: %w", err)
	}
	m := toy.Train(string(text))
	p.printf("After %q, the next word can be:\n", "the model")
	for _, t := range []float64{1, 0.5, 2} {
		p.printf("  temperature %-3v", t)
		for _, c := range m.Next("the", "model", t)[:4] {
			p.printf(" %s %.2f", c.Word, c.P)
		}
		p.printf("\n")
	}
	p.printf("\nGreedy, temperature 0:\n%s\n",
		wrap(m.Generate("The", "model", 60, 0, nil)))
	p.printf("\nSampled, temperature 1, three runs:\n")
	for seed := range uint64(3) {
		rng := rand.New(rand.NewPCG(seed, 3))
		p.printf("%s\n", wrap(m.Generate("The", "model", 30, 1, rng)))
	}
	return nil
}

// end::toy[]

// tag::bpe[]

// runBPE learns a vocabulary from chapters 1 and 2, then splits an
// English sentence, the same sentence in Spanish, and Go code with it.
func runBPE(_ context.Context, p *printer) error {
	text, err := fs.ReadFile(files, "testdata/corpus.txt")
	if err != nil {
		return fmt.Errorf("read corpus: %w", err)
	}
	tok := bpe.Train(string(text), 1000)
	p.printf("Vocabulary: %d tokens\n", tok.Vocab())
	p.printf("First merges: %s\n\n", strings.Join(tok.First(8), " "))
	for _, s := range []string{
		"The model reads the conversation.",
		"El modelo lee la conversación.",
		"d, err := time.ParseDuration(s)",
	} {
		toks := tok.Encode(s)
		p.printf("%d tokens, %d bytes: %s\n", len(toks), len(s), s)
		p.printf("  %s\n", show(toks))
	}
	src, err := fs.ReadFile(files, "testdata/duration.go")
	if err != nil {
		return fmt.Errorf("read duration.go: %w", err)
	}
	p.printf("\nduration.go: %d bytes, %d tokens\n",
		len(src), len(tok.Encode(string(src))))
	return nil
}

// end::bpe[]

// vec formats a vector as numbers separated by commas.
func vec(v []float64) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = strconv.FormatFloat(x, 'f', 1, 64)
	}
	return strings.Join(parts, ", ")
}

// show prints tokens separated by |, with invisible characters escaped.
func show(toks []string) string {
	var parts []string
	for _, t := range toks {
		q := strconv.Quote(t)
		parts = append(parts, q[1:len(q)-1])
	}
	return strings.Join(parts, "|")
}

// tag::network[]

// runNetwork compares made-up embeddings and shows softmax turning
// scores into probabilities at three temperatures.
func runNetwork(_ context.Context, p *printer) error {
	vecs := []struct {
		word string
		v    []float64
	}{
		{"goroutine", []float64{0.9, 0.8, 0.0}},
		{"thread", []float64{0.9, 0.2, 0.0}},
		{"channel", []float64{0.7, 0.9, 0.1}},
		{"banana", []float64{0.0, 0.1, 0.9}},
	}
	p.printf("%-10s %-16s %s\n", "word", "vector",
		"similarity to "+vecs[0].word)
	for _, w := range vecs {
		p.printf("%-10s %-16s %.2f\n", w.word, vec(w.v),
			attention.Cosine(vecs[0].v, w.v))
	}
	words := []string{"fast", "simple", "slow"}
	scores := []float64{2.0, 1.0, -1.0}
	p.printf("\nScores %v for %v become probabilities:\n", scores, words)
	for _, t := range []float64{1, 0.5, 2} {
		probs := attention.Softmax(attention.WithTemperature(scores, t))
		p.printf("  temperature %-3v %.2f\n", t, probs)
	}
	return nil
}

// end::network[]

// tag::attention[]

// runAttention works one attention step: the word "it" looking back
// at the three words before it.
func runAttention(_ context.Context, p *printer) error {
	words := []string{"test", "failed", "because"}
	keys := [][]float64{{1.0, 0.2}, {0.2, 1.0}, {0.1, 0.1}}
	values := [][]float64{{1, 0}, {0, 1}, {0.5, 0.5}}
	query := []float64{2.0, 0.0} // "it" looks for a thing
	weights, blend := attention.Attend(query, keys, values)
	p.printf("query for \"it\": %s\n\n", vec(query))
	p.printf("%-8s %-10s %-10s %s\n", "word", "key", "value", "weight")
	for i, w := range words {
		p.printf("%-8s %-10s %-10s %.2f\n", w, vec(keys[i]),
			vec(values[i]), weights[i])
	}
	p.printf("\nWhat \"it\" takes from the context: %.2f\n", blend)

	// The same, with 100 more filler words between "test" and "it".
	for range 100 {
		keys = append(keys, keys[2])
		values = append(values, values[2])
	}
	weights, _ = attention.Attend(query, keys, values)
	p.printf("With 100 more words like \"because\": %q gets %.2f\n",
		"test", weights[0])
	return nil
}

// end::attention[]

// tag::template[]

// runTemplate renders the start of chapter 1's bug fix as the text a
// model reads.
func runTemplate(_ context.Context, p *printer) error {
	var defs []agent.ToolDef
	for _, t := range agent.Tools(agent.FS{}) {
		if t.Def.Name == "read_file" { // one tool keeps it short
			defs = append(defs, t.Def)
		}
	}
	msgs := []agent.Message{
		{Role: "user", Content: "The tests in this project fail. " +
			"Find out why and fix it."},
		agent.Calls(agent.Call("call_1", "read_file",
			`{"path": "duration_test.go"}`)),
		{Role: "tool", ToolCallID: "call_1",
			Content: "package duration\n..."},
	}
	text, err := chatml.Render(msgs, defs)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	p.printf("%s", text)
	return nil
}

// end::template[]

// tag::probe[]

// runProbe asks a model to describe Go in three words at temperature 0
// and 1.2, and prints how likely each token it wrote was.
func runProbe(ctx context.Context, p *printer) error {
	client, err := probeClient(p)
	if err != nil {
		return err
	}
	ctx = replay.WithScript(ctx, "probe")
	msgs := []agent.Message{{Role: "user",
		Content: "Describe Go in exactly three words."}}
	for _, t := range []float64{0, 1.2} {
		reply, err := client.Probe(ctx, msgs, t, 3, 10)
		if err != nil {
			return fmt.Errorf("temperature %v: %w", t, err)
		}
		p.printf("temperature %v, finish reason %s:\n", t,
			reply.FinishReason)
		p.printf("  %-14s %-5s  %s\n", "token", "p", "best other")
		for _, tok := range reply.Tokens {
			p.printf("  %-14q %.2f", tok.Text, tok.P)
			if alt, ok := runnerUp(tok); ok {
				p.printf("   %-14q %.2f", alt.Text, alt.P)
			}
			p.printf("\n")
		}
	}
	return nil
}

// end::probe[]

// runnerUp returns the most likely token at tok's position other than
// tok itself.
func runnerUp(tok openrouter.Token) (openrouter.Token, bool) {
	for _, alt := range tok.Top {
		if alt.Text != tok.Text {
			return alt, true
		}
	}
	return openrouter.Token{}, false
}

// probeClient returns a client for the probe: live with a key, and
// replaying the saved calls without one.
func probeClient(p *printer) (*openrouter.Client, error) {
	c := &openrouter.Client{
		APIKey:  os.Getenv("OPENROUTER_API_KEY"),
		Model:   probeModel,
		BaseURL: os.Getenv("OPENROUTER_BASE_URL"),
		// Pinned: another provider of this model reports logprobs that
		// don't match its own text.
		Providers: []string{"novita"},
	}
	if c.APIKey != "" {
		return c, nil
	}
	saved, err := fs.Sub(files, "testdata/replies")
	if err != nil {
		return nil, fmt.Errorf("saved replies: %w", err)
	}
	c.HTTP = &http.Client{Transport: &replay.Transport{Files: saved}}
	p.printf("No OPENROUTER_API_KEY: replaying saved calls.\n")
	p.printf("Model: %s\n\n", probeModel)
	return c, nil
}
