package main

import (
	"cmp"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
)

// summary prints a table for each experiment with saved results.
func summary(w io.Writer, dir string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, f := range []func(io.Writer, string) error{
		fixSummary, askSummary, chatSummary, tokenSummary, probeSummary,
		streamSummary, overheadSummary, workflowSummary, variantSummary,
		triageSummary, similaritySummary, estimateSummary, breakersSummary, qualitySummary,
		callsSummary, toolTimeSummary, poisonSummary,
		reasoningSummary, sdkRetrySummary, sdkRoundTripSummary,
		wireSummary, priceSummary, clientSummary, generationSummary,
		ch12RunSummary, injectionSummary, ch15Summary,
	} {
		if err := f(tw, dir); err != nil {
			return err
		}
		if err := tw.Flush(); err != nil {
			return fmt.Errorf("write summary: %w", err)
		}
	}
	return nil
}

func fixSummary(w io.Writer, dir string) error {
	runs, err := load[fixRun](filepath.Join(dir, "fix.jsonl"))
	if err != nil || len(runs) == 0 {
		return err
	}
	fmt.Fprintln(w, "== fix: the duration task, per model")
	fmt.Fprintln(w, "model\truns\tpassed\tanswered\tturns (median, range)\t"+
		"calls/run\tbatched runs\ttool errors\ttest edited\tcost (median)\t"+
		"seconds (median)")
	turnsAll := map[int]int{}
	for _, m := range modelsIn(runs, func(r fixRun) string { return r.Model }) {
		var turns, calls []int
		var costs, secs []float64
		passed, answered, batched, toolErrs, edited := 0, 0, 0, 0, 0
		for _, r := range runs {
			if r.Model != m {
				continue
			}
			turns = append(turns, len(r.Calls))
			turnsAll[len(r.Calls)]++
			n := 0
			for _, c := range r.ToolCalls {
				n += c
			}
			calls = append(calls, n)
			costs = append(costs, r.Total.Cost)
			secs = append(secs, r.Seconds)
			if r.Passed {
				passed++
			}
			if r.Stopped == "answer" {
				answered++
			}
			if slices.ContainsFunc(r.Calls, func(c call) bool { return c.Calls > 1 }) {
				batched++
			}
			toolErrs += r.ToolErrors
			if r.TestEdited {
				edited++
			}
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d (%d-%d)\t%.1f\t%d\t%d\t%d\t$%.4f\t%.1f\n",
			m, len(turns), passed, answered, median(turns), slices.Min(turns),
			slices.Max(turns), mean(calls), batched, toolErrs, edited,
			median(costs), median(secs))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "turns to finish, all runs\truns")
	for _, t := range slices.Sorted(maps.Keys(turnsAll)) {
		fmt.Fprintf(w, "%d\t%d\n", t, turnsAll[t])
	}
	var stops []string
	for _, r := range runs {
		if r.Stopped != "answer" {
			stops = append(stops, fmt.Sprintf("%s %d: %s", r.Model, r.Run,
				r.Stopped))
		}
	}
	if len(stops) > 0 {
		fmt.Fprintln(w, "\nruns that stopped without an answer:")
		for _, s := range stops {
			fmt.Fprintln(w, "  "+s)
		}
	}
	fmt.Fprintln(w)
	return nil
}

func askSummary(w io.Writer, dir string) error {
	runs, err := load[askRun](filepath.Join(dir, "ask.jsonl"))
	if err != nil || len(runs) == 0 {
		return err
	}
	fmt.Fprintln(w, "== ask: one question, per model (medians)")
	fmt.Fprintln(w, "model\truns\tseconds\tprompt\tcompletion\treasoning\t"+
		"answer chars\tcost")
	for _, m := range modelsIn(runs, func(r askRun) string { return r.Model }) {
		var secs, cost []float64
		var prompt, compl, reason, chars []int
		for _, r := range runs {
			if r.Model != m {
				continue
			}
			secs = append(secs, r.Seconds)
			cost = append(cost, r.Cost)
			prompt = append(prompt, r.Prompt)
			compl = append(compl, r.Completion)
			reason = append(reason, r.Reasoning)
			chars = append(chars, len(r.Text))
		}
		fmt.Fprintf(w, "%s\t%d\t%.2f\t%d\t%d\t%d\t%d\t$%.5f\n", m, len(secs),
			median(secs), median(prompt), median(compl), median(reason),
			median(chars), median(cost))
	}
	fmt.Fprintln(w)
	return nil
}

func chatSummary(w io.Writer, dir string) error {
	runs, err := load[chatRun](filepath.Join(dir, "chat.jsonl"))
	if err != nil || len(runs) == 0 {
		return err
	}
	fmt.Fprintln(w, "== chat: 20 turns, prompt tokens sent per turn")
	marks := []int{1, 2, 5, 10, 15, 20}
	var head strings.Builder
	head.WriteString("model")
	for _, t := range marks {
		fmt.Fprintf(&head, "\tturn %d", t)
	}
	fmt.Fprintln(w, head.String()+"\tall prompt\tall completion\tcached\tcost\t"+
		"cost of last turn / first")
	for _, r := range runs {
		var line strings.Builder
		line.WriteString(r.Model)
		var prompt, compl, cached int
		var cost float64
		for _, t := range marks {
			if t <= len(r.Turns) {
				fmt.Fprintf(&line, "\t%d", r.Turns[t-1].Prompt)
			} else {
				line.WriteString("\t-")
			}
		}
		for _, t := range r.Turns {
			prompt += t.Prompt
			compl += t.Completion
			cached += t.Cached
			cost += t.Cost
		}
		ratio := r.Turns[len(r.Turns)-1].Cost / r.Turns[0].Cost
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t$%.4f\t%.1fx\n", line.String(), prompt, compl,
			cached, cost, ratio)
	}
	fmt.Fprintln(w)
	return nil
}

func tokenSummary(w io.Writer, dir string) error {
	runs, err := load[tokenRun](filepath.Join(dir, "tokens.jsonl"))
	if err != nil || len(runs) == 0 {
		return err
	}
	fmt.Fprintln(w, "== tokens: the same text on each tokenizer")
	fmt.Fprintln(w, "model\tprovider\ttext\tbytes\twords\ttokens\tbytes/token\twords/token")
	slices.SortStableFunc(runs, func(a, b tokenRun) int {
		return cmp.Or(strings.Compare(a.Text, b.Text),
			strings.Compare(a.Model, b.Model))
	})
	for _, r := range runs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%.2f\t%.2f\n", r.Model,
			cmp.Or(r.Provider, "(one only)"), r.Text,
			r.Bytes, r.Words, r.Tokens, float64(r.Bytes)/float64(r.Tokens),
			float64(r.Words)/float64(r.Tokens))
	}
	fmt.Fprintln(w)
	return nil
}

// probeReply is the part of a saved probe response the summary reads.
type probeReply struct {
	Provider string `json:"provider"`
	Choices  []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		Logprobs struct {
			Content []struct {
				Token   string  `json:"token"`
				Bytes   []int   `json:"bytes"`
				Logprob float64 `json:"logprob"`
				Top     []struct {
					Token   string  `json:"token"`
					Logprob float64 `json:"logprob"`
				} `json:"top_logprobs"`
			} `json:"content"`
		} `json:"logprobs"`
	} `json:"choices"`
}

func probeSummary(w io.Writer, dir string) error {
	routes, err := os.ReadDir(filepath.Join(dir, "probe"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read probe results: %w", err)
	}
	for _, route := range routes {
		fmt.Fprintf(w, "== probe (%s): %q on %s\n", route.Name(),
			"Describe Go in exactly three words.", probeModel)
		files, err := filepath.Glob(filepath.Join(dir, "probe", route.Name(),
			"t*.json"))
		if err != nil {
			return fmt.Errorf("list probe recordings: %w", err)
		}
		type key struct{ temp, text string }
		count := map[key]int{}
		providers := map[string]int{}
		aligned, runs := 0, 0
		var firstAligned string
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return fmt.Errorf("read probe recording: %w", err)
			}
			var rec struct {
				Response probeReply `json:"response"`
			}
			if err := json.Unmarshal(b, &rec); err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
			temp, _, _ := strings.Cut(strings.TrimPrefix(filepath.Base(f), "t"), "-")
			ch := rec.Response.Choices[0]
			count[key{temp, ch.Message.Content}]++
			providers[rec.Response.Provider]++
			var joined strings.Builder
			for _, t := range ch.Logprobs.Content {
				if t.Bytes == nil {
					joined.WriteString(t.Token)
				}
				for _, x := range t.Bytes {
					joined.WriteByte(byte(x))
				}
			}
			runs++
			if strings.TrimSuffix(joined.String(), "<|eot_id|>") == ch.Message.Content {
				aligned++
				if temp == "0" && firstAligned == "" && len(ch.Logprobs.Content) > 0 {
					for _, t := range ch.Logprobs.Content[0].Top {
						firstAligned += fmt.Sprintf(" %q %.2f", t.Token,
							math.Exp(t.Logprob))
					}
				}
			}
		}
		fmt.Fprintf(w, "providers:")
		for _, p := range slices.Sorted(maps.Keys(providers)) {
			fmt.Fprintf(w, " %s %d", p, providers[p])
		}
		fmt.Fprintf(w, "; logprobs match the text in %d of %d\n", aligned, runs)
		temps := map[string][]string{}
		for k := range count {
			temps[k.temp] = append(temps[k.temp], k.text)
		}
		for _, t := range slices.Sorted(maps.Keys(temps)) {
			texts := temps[t]
			slices.SortFunc(texts, func(a, b string) int {
				return cmp.Or(count[key{t, b}]-count[key{t, a}],
					strings.Compare(a, b))
			})
			fmt.Fprintf(w, "temperature %s: %d different replies\n", t,
				len(texts))
			for _, s := range texts {
				fmt.Fprintf(w, "  %d\t%q\n", count[key{t, s}], s)
			}
		}
		if firstAligned != "" {
			fmt.Fprintf(w, "first token at temperature 0:%s\n", firstAligned)
		}
		fmt.Fprintln(w)
	}
	return nil
}

func streamSummary(w io.Writer, dir string) error {
	runs, err := load[streamRun](filepath.Join(dir, "stream.jsonl"))
	if err != nil || len(runs) == 0 {
		return err
	}
	fmt.Fprintln(w, "== stream: seconds, medians over runs")
	fmt.Fprintln(w, "model\truns\tstream: first piece\tfirst text\twhole\t"+
		"longest gap (max)\tpieces\tgenerate: whole\tcompletion tokens")
	for _, m := range modelsIn(runs, func(r streamRun) string { return r.Model }) {
		var first, text, total, gen, gaps []float64
		var deltas, compl []int
		for _, r := range runs {
			if r.Model != m {
				continue
			}
			if r.Mode == "generate" {
				gen = append(gen, r.Total)
				continue
			}
			first = append(first, r.First)
			text = append(text, r.FirstText)
			total = append(total, r.Total)
			gaps = append(gaps, r.MaxGap)
			deltas = append(deltas, r.Deltas)
			compl = append(compl, r.Completion)
		}
		fmt.Fprintf(w, "%s\t%d\t%.2f\t%.2f\t%.2f\t%.2f\t%d\t%.2f\t%d\n", m,
			len(first), median(first), median(text), median(total),
			slices.Max(gaps), median(deltas), median(gen), median(compl))
	}
	fmt.Fprintln(w)
	return nil
}

// modelsIn returns the models in runs, in the order of models, then any
// others in order of first appearance.
func modelsIn[T any](runs []T, model func(T) string) []string {
	var out []string
	for _, m := range append(slices.Clone(models), probeModel) {
		if slices.ContainsFunc(runs, func(r T) bool { return model(r) == m }) {
			out = append(out, m)
		}
	}
	for _, r := range runs {
		if !slices.Contains(out, model(r)) {
			out = append(out, model(r))
		}
	}
	return out
}

func median[T int | float64](xs []T) T {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Sorted(slices.Values(xs))
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func mean[T int | float64](xs []T) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum T
	for _, x := range xs {
		sum += x
	}
	return float64(sum) / float64(len(xs))
}

func overheadSummary(w io.Writer, dir string) error {
	runs, err := load[overheadRun](filepath.Join(dir, "overhead.jsonl"))
	if err != nil || len(runs) == 0 {
		return err
	}
	fmt.Fprintln(w, "== overhead: prompt tokens for the fix task's first call")
	fmt.Fprintln(w, "model\ttask only\twith the four tools\t"+
		"definitions as text\tadded by the provider")
	slices.SortFunc(runs, func(a, b overheadRun) int {
		return slices.Index(models, a.Model) - slices.Index(models, b.Model)
	})
	for _, r := range runs {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\n", r.Model, r.TaskOnly,
			r.WithTools, r.AsText, r.WithTools-r.AsText)
	}
	fmt.Fprintln(w)
	return nil
}

func workflowSummary(w io.Writer, dir string) error {
	runs, err := load[workflowRun](filepath.Join(dir, "workflow.jsonl"))
	if err != nil || len(runs) == 0 {
		return err
	}
	fixes, err := load[fixRun](filepath.Join(dir, "fix.jsonl"))
	if err != nil {
		return err
	}
	fmt.Fprintln(w, "== workflow: the bug fix in one model call, against the agent (medians)")
	fmt.Fprintln(w, "model\truns\tpassed\tcost\tseconds\tagent passed\tagent cost\t"+
		"agent seconds")
	for _, m := range modelsIn(runs, func(r workflowRun) string { return r.Model }) {
		var cost, secs, acost, asecs []float64
		passed, apassed := 0, 0
		for _, r := range runs {
			if r.Model == m {
				cost = append(cost, r.Cost)
				secs = append(secs, r.Seconds)
				if r.Passed {
					passed++
				}
			}
		}
		for _, r := range fixes {
			if r.Model == m {
				acost = append(acost, r.Total.Cost)
				asecs = append(asecs, r.Seconds)
				if r.Passed {
					apassed++
				}
			}
		}
		fmt.Fprintf(w, "%s\t%d\t%d\t$%.5f\t%.2f\t%d of %d\t$%.5f\t%.2f\n", m,
			len(cost), passed, median(cost), median(secs), apassed, len(acost),
			median(acost), median(asecs))
	}
	for _, r := range runs {
		if !r.Passed {
			fmt.Fprintf(w, "failed: %s %d: %q\n", r.Model, r.Run, r.Edit)
		}
	}
	fmt.Fprintln(w)
	return nil
}

// variantSummary compares the fix task's tool variants per model.
func variantSummary(w io.Writer, dir string) error {
	files, err := filepath.Glob(filepath.Join(dir, "fix-*.jsonl"))
	if err != nil {
		return fmt.Errorf("list variants: %w", err)
	}
	for _, f := range files {
		runs, err := load[fixRun](f)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(filepath.Base(f), ".jsonl")
		fmt.Fprintf(w, "== %s: the fix task with changed tools (medians)\n", name)
		fmt.Fprintln(w, "model\truns\tturns\tedit calls\thit turn limit\t"+
			"paths\tcost")
		for _, m := range modelsIn(runs, func(r fixRun) string { return r.Model }) {
			var turns, edits []int
			var cost []float64
			limit := 0
			paths := map[string]bool{}
			for _, r := range runs {
				if r.Model != m {
					continue
				}
				turns = append(turns, len(r.Calls))
				edits = append(edits, r.ToolCalls["edit_file"])
				cost = append(cost, r.Total.Cost)
				if r.Stopped != "answer" {
					limit++
				}
				paths[strings.Join(r.Path, " | ")] = true
			}
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t$%.4f\n", m, len(turns),
				median(turns), median(edits), limit, len(paths), median(cost))
		}
		fmt.Fprintln(w)
	}
	return nil
}

// triageSummary compares chapter 6's agent with chapter 7's on the
// triage tasks.
func triageSummary(w io.Writer, dir string) error {
	runs, err := load[triageRun](filepath.Join(dir, "triage.jsonl"))
	if err != nil || len(runs) == 0 {
		return err
	}
	fmt.Fprintln(w, "== triage: chapter 6's agent against chapter 7's (medians)")
	fmt.Fprintln(w, "task\tsetup\tmodel\truns\tfix right\tall checks\tissues filed\tturns\tcost")
	for _, task := range triageTasks {
		for _, setup := range []string{"ch06", "ch07"} {
			for _, m := range modelsIn(runs, func(r triageRun) string { return r.Model }) {
				var turns, issues []int
				var cost []float64
				fix, pass := 0, 0
				for _, r := range runs {
					if r.Task != task || r.Setup != setup || r.Model != m {
						continue
					}
					turns = append(turns, r.Turns)
					issues = append(issues, r.Issues)
					cost = append(cost, r.Total.Cost)
					if r.FixRight {
						fix++
					}
					if r.Passed {
						pass++
					}
				}
				if len(turns) == 0 {
					continue
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t$%.4f\n", task, setup,
					m, len(turns), fix, pass, median(issues), median(turns), median(cost))
			}
		}
	}
	fmt.Fprintln(w)
	return nil
}
