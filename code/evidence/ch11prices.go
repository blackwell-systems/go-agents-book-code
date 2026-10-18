package main

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	agent11 "github.com/blackwell-systems/go-agents-book/code/ch11/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch11/anthropic"
	"github.com/blackwell-systems/go-agents-book/code/ch11/replay"
)

// Chapter 11's price check (E1): every call of the recorded runs
// through the Anthropic client, replayed through the client itself, so
// its price table prices each reply; OpenRouter's Messages API reports
// what it billed for the same call, which the client ignores.

// priceSummary compares the two, per recorded run.
func priceSummary(w io.Writer, dir string) error {
	runs, err := filepath.Glob(filepath.Join(dir, "ch11prices", "*"))
	if err != nil {
		return fmt.Errorf("prices: %w", err)
	}
	if len(runs) == 0 {
		return nil
	}
	prices, err := anthropic.DefaultPrices()
	if err != nil {
		return fmt.Errorf("prices: %w", err)
	}
	fmt.Fprintln(w, "== prices: the Anthropic client's price table "+
		"against OpenRouter's bill, per call of the recorded runs")
	fmt.Fprintln(w, "run\tcalls\tpriced\tbilled\tlargest gap per call\t"+
		"calls over 100,000 prompt tokens")
	for _, run := range runs {
		row, err := priceRun(run, prices)
		if err != nil {
			return err
		}
		fmt.Fprintf(w, "%s\t%s\n", filepath.Base(run), row)
	}
	fmt.Fprintln(w)
	return nil
}

// priceRun replays one recorded run's calls in order through the
// client and compares each reply's priced cost with the billed one.
func priceRun(dir string, prices anthropic.Prices) (string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return "", fmt.Errorf("prices: %w", err)
	}
	slices.SortFunc(files, func(a, b string) int {
		return callIndex(a) - callIndex(b)
	})
	if len(files) == 0 {
		return "", nil
	}
	script := callNumber.FindStringSubmatch(filepath.Base(files[0]))[1]
	c := &anthropic.Client{Model: "anthropic/claude-haiku-5.5",
		Prices: prices, BaseURL: "http://replay",
		HTTP: &http.Client{Transport: &replay.Transport{
			Files: os.DirFS(dir)}}}
	ctx := replay.WithScript(context.Background(), script)
	var priced, billed, gap float64
	over := 0
	for _, f := range files {
		r, err := agent11.Collect(c.Stream(ctx, agent11.Request{}))
		if err != nil {
			return "", fmt.Errorf("%s: %w", f, err)
		}
		bill, err := billedCost(f)
		if err != nil {
			return "", err
		}
		table := prices.Cost(c.Model, r.Usage) // the table alone
		priced += table
		billed += bill
		gap = max(gap, math.Abs(table-bill))
		if r.Usage.PromptTokens > 100_000 {
			over++
		}
	}
	return fmt.Sprintf("%d\t$%.6f\t$%.6f\t$%.7f\t%d", len(files), priced,
		billed, gap, over), nil
}

// billedCost reads the cost OpenRouter reported in a recorded stream's
// last usage.
func billedCost(file string) (float64, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return 0, fmt.Errorf("prices: %w", err)
	}
	var rec struct {
		Stream string `json:"response_text"`
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return 0, fmt.Errorf("%s: %w", file, err)
	}
	costs := regexp.MustCompile(`"cost":([0-9.eE-]+)`).
		FindAllStringSubmatch(rec.Stream, -1)
	if len(costs) == 0 {
		return 0, nil
	}
	var cost float64
	_, err = fmt.Sscan(costs[len(costs)-1][1], &cost)
	return cost, err //nolint:wrapcheck // a number from the recording
}

// clientRun is one run of m1-done-after-delete through one of chapter
// 11's clients (E5), as the program printed it.
type clientRun struct {
	Client  string  `json:"client"`
	Run     int     `json:"run"`
	Turns   int     `json:"turns"`
	Input   int     `json:"input"`
	Cached  int     `json:"cached"`
	Output  int     `json:"output"`
	Cost    float64 `json:"cost"`
	Passed  int     `json:"passed"`
	Checks  int     `json:"checks"`
	Seconds int     `json:"seconds"`
}

// clientSummary prints the runs per client.
func clientSummary(w io.Writer, dir string) error {
	runs, err := load[clientRun](filepath.Join(dir, "ch11clients.jsonl"))
	if err != nil || len(runs) == 0 {
		return err
	}
	fmt.Fprintln(w, "== clients: m1-done-after-delete through each of "+
		"chapter 11's clients")
	fmt.Fprintln(w, "client\trun\tturns\tinput\tfrom the cache\toutput\t"+
		"cost\tchecks\tseconds")
	for _, r := range runs {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%.0f%%\t%d\t$%.4f\t%d of %d\t%d\n",
			r.Client, r.Run, r.Turns, r.Input,
			100*float64(r.Cached)/float64(r.Input), r.Output, r.Cost,
			r.Passed, r.Checks, r.Seconds)
	}
	fmt.Fprintln(w)
	return nil
}
