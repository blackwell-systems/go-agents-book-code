// Command evidence runs the live measurements the book quotes: the fix
// task on several models, one question to several models, a long chat,
// token counts, the temperature probe, and stream timing. Each call is
// recorded under testdata, and each run's measurements are appended to
// testdata/<experiment>.jsonl. With no key it prints the summary tables
// from the saved results; the book's numbers come from that summary.
//
//	OPENROUTER_API_KEY=... go run ./evidence -run fix -n 10
//	OPENROUTER_API_KEY=... go run ./evidence -run breakers -provider anthropic
//	go run ./evidence
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
)

// The models measured: the book's default and the alternatives named in
// book/attributes.adoc.
var models = []string{
	"anthropic/claude-haiku-5.5",
	"openai/gpt-5.6-luna",
	"z-ai/glm-5.3-flash",
	"deepseek/deepseek-v4.1-flash",
}

// probeModel reports token probabilities (chapter 3).
const probeModel = "meta-llama/llama-3.1-8b-instruct"

// An experiment makes its runs and appends their results.
type experiment struct {
	name string
	run  func(ctx context.Context, e *env) error
}

var experiments = []experiment{
	{"fix", runFix},
	{"savetime", runSaveTime}, // chapter 15, offline
	{"ask", runAsk},
	{"chat", runChat},
	{"tokens", runTokens},
	{"probe", runProbe},
	{"stream", runStream},
	{"overhead", runOverhead},
	{"workflow", runWorkflow},
	{"triage", runTriage},
	{"breakers", runBreakers},
	{"tooltime", runToolTime},     // offline: needs no key
	{"poison", runPoison},         // chapter 10
	{"reasoning", runReasoning},   // chapter 11
	{"sdkretries", runSDKRetries}, // offline: needs no key
	{"generation", runGeneration}, // chapter 12
}

// env is what every experiment shares.
type env struct {
	key   string
	dir   string // testdata
	n     int    // runs per model
	spend *spend

	// The breakers experiment: the saved request to send, and the
	// provider to pin.
	request  string
	provider string
}

func main() {
	which := flag.String("run", "",
		"experiments to run, comma-separated (fix, ask, chat, tokens, "+
			"probe, stream, overhead, workflow, prefer-test, locked, locked-clear, triage, breakers, tooltime, poison, reasoning, sdkretries, generation, savetime); empty prints the summary")
	n := flag.Int("n", 0, "runs per model (sends per change for breakers); 0 for each experiment's default")
	budget := flag.Float64("budget", 3,
		"stop starting runs once this many dollars are spent")
	request := flag.String("request", "",
		"breakers: a saved batch request to send; empty picks turn 10 of a run under testdata/cache")
	provider := flag.String("provider", "anthropic",
		"breakers: the OpenRouter provider to pin every send to")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *which == "" {
		if err := summary(os.Stdout, "evidence/testdata"); err != nil {
			log.Fatal(err)
		}
		return
	}
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" && *which != "tooltime" && *which != "sdkretries" &&
		*which != "savetime" { // offline
		log.Fatal("set OPENROUTER_API_KEY to run experiments")
	}
	e := &env{key: key, dir: "evidence/testdata", n: *n,
		spend: &spend{limit: *budget}, request: *request, provider: *provider}
	for name := range strings.SplitSeq(*which, ",") {
		if v, ok := variants[name]; ok {
			if err := runVariant(ctx, e, v); err != nil {
				log.Fatalf("%s: %v", name, err)
			}
			continue
		}
		x, ok := find(name)
		if !ok {
			log.Fatalf("no experiment %q", name)
		}
		if err := x.run(ctx, e); err != nil {
			log.Fatalf("%s: %v", name, err)
		}
	}
	fmt.Printf("spent $%.4f\n", e.spend.total())
}

func find(name string) (experiment, bool) {
	for _, x := range experiments {
		if x.name == name {
			return x, true
		}
	}
	return experiment{}, false
}

// spend adds up what the runs cost and refuses new runs past a limit.
type spend struct {
	mu    sync.Mutex
	limit float64
	sum   float64
}

func (s *spend) add(cost float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sum += cost
}

func (s *spend) total() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sum
}

// check returns an error once the limit is reached.
func (s *spend) check() error {
	if t := s.total(); t >= s.limit {
		return fmt.Errorf("budget reached: $%.4f of $%.2f", t, s.limit)
	}
	return nil
}

// slug turns a model ID into a directory name.
func slug(model string) string {
	return strings.ReplaceAll(model, "/", "_")
}
