package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/ch15/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch15/durable"
)

// Chapter 15's E3 and E4, both offline.
//
// E3: how big a checkpoint gets as a run goes on. The recorded replies
// of a run hold every request the run sent; a message's first
// appearance is the message as the conversation holds it, before any
// context policy cleared or trimmed it. So the conversation after turn
// n is the first n requests' messages, each as first seen, and saving
// it with chapter 15's checkpoint gives the file's size at that turn.
//
// E4: how long model calls take, against the time a platform gives a
// program to stop. The chat spans of chapter 12's live runs
// (testdata/ch12runs) record each model call's duration.

// ch15Runs are the recorded runs E3 measures.
var ch15Runs = []struct{ name, dir, script string }{
	{"ch15 m1", "ch15/testdata/replies", "m1-done-after-delete"},
	{"ch08 batch", "ch08/testdata/replies", "batch"},
}

type wireMsg struct {
	Role       string         `json:"role"`
	Content    jsontext.Value `json:"content"` // a string or parts
	ToolCallID string         `json:"tool_call_id"`
	Reasoning  jsontext.Value `json:"reasoning_details"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

// conversations returns the full conversation as of each recorded
// request of a script, in order.
func conversations(dir, script string) ([][]agent.Message, error) {
	files, err := filepath.Glob(filepath.Join(dir, script+"-[0-9]*.json"))
	if err != nil {
		return nil, fmt.Errorf("conversations: %w", err)
	}
	slices.Sort(files)
	var full []agent.Message
	var out [][]agent.Message
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("conversations: %w", err)
		}
		var rec struct {
			Request struct {
				Messages []wireMsg `json:"messages"`
			} `json:"request"`
		}
		if err := json.Unmarshal(b, &rec); err != nil {
			return nil, fmt.Errorf("conversations %s: %w", f, err)
		}
		for _, m := range rec.Request.Messages[min(len(full),
			len(rec.Request.Messages)):] {
			full = append(full, fromWire(m))
		}
		out = append(out, slices.Clone(full[:len(rec.Request.Messages)]))
	}
	return out, nil
}

func fromWire(w wireMsg) agent.Message {
	m := agent.Message{Role: w.Role, Content: text(w.Content),
		ToolCallID: w.ToolCallID}
	if len(w.Reasoning) > 0 && string(w.Reasoning) != "null" {
		m.ReasoningDetails = w.Reasoning
	}
	for _, c := range w.ToolCalls {
		m.ToolCalls = append(m.ToolCalls, agent.ToolCall{ID: c.ID,
			Name: c.Function.Name,
			Arguments: jsontext.Value(cmp.Or(c.Function.Arguments,
				"{}"))})
	}
	return m
}

// text is a message's content: a string, or the text of its parts.
func text(v jsontext.Value) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(v, &parts) != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// everySave is what the loop saves over a run: the conversation after
// each reply and after each answer, which is what the journal writes,
// leaving out markers and the first and last saves.
func everySave(full []agent.Message) [][]agent.Message {
	var out [][]agent.Message
	for k := 1; k <= len(full); k++ {
		if r := full[k-1].Role; r == "assistant" || r == "tool" {
			out = append(out, full[:k])
		}
	}
	return out
}

// saveTimes saves each conversation, with the syncs, and returns how
// long each save took.
func saveTimes(convs [][]agent.Message) ([]time.Duration, error) {
	dir, err := os.MkdirTemp("", "ch15-time-")
	if err != nil {
		return nil, fmt.Errorf("save times: %w", err)
	}
	defer os.RemoveAll(dir)
	cp, err := durable.Open(dir, "run")
	if err != nil {
		return nil, fmt.Errorf("save times: %w", err)
	}
	defer cp.Close()
	var ts []time.Duration
	for _, msgs := range convs {
		start := time.Now()
		if err := cp.Save(context.Background(),
			agent.NewState("run", msgs)); err != nil {
			return nil, fmt.Errorf("save times: %w", err)
		}
		ts = append(ts, time.Since(start))
	}
	slices.Sort(ts)
	return ts, nil
}

// checkpointSizes saves each conversation as a checkpoint and returns
// the files' sizes in bytes.
func checkpointSizes(convs [][]agent.Message) ([]int64, error) {
	dir, err := os.MkdirTemp("", "ch15-size-")
	if err != nil {
		return nil, fmt.Errorf("sizes: %w", err)
	}
	defer os.RemoveAll(dir)
	cp, err := durable.Open(dir, "run")
	if err != nil {
		return nil, fmt.Errorf("sizes: %w", err)
	}
	defer cp.Close()
	var sizes []int64
	for i, msgs := range convs {
		st := agent.NewState("run", msgs)
		st.Turns, st.Phase = i, agent.PhaseModel
		if err := cp.Save(context.Background(), st); err != nil {
			return nil, fmt.Errorf("sizes: %w", err)
		}
		fi, err := os.Stat(filepath.Join(dir, "run.json"))
		if err != nil {
			return nil, fmt.Errorf("sizes: %w", err)
		}
		sizes = append(sizes, fi.Size())
	}
	return sizes, nil
}

// chatSpan matches a model call's span in a printed trace: two spaces,
// "chat", the model, and the call's duration.
var chatSpan = regexp.MustCompile(`^  chat (\S+) (\S+) `)

// chatDurations reads every model call's duration from the saved
// outputs of chapter 12's live runs.
func chatDurations(dir string) ([]time.Duration, error) {
	files, err := filepath.Glob(filepath.Join(dir, "ch12runs", "*.txt"))
	if err != nil {
		return nil, fmt.Errorf("durations: %w", err)
	}
	var ds []time.Duration
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return nil, fmt.Errorf("durations: %w", err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(nil, 1<<20)
		for sc.Scan() {
			m := chatSpan.FindStringSubmatch(sc.Text())
			if m == nil {
				continue
			}
			if d, err := time.ParseDuration(m[2]); err == nil {
				ds = append(ds, d)
			}
		}
		err = sc.Err()
		fh.Close()
		if err != nil {
			return nil, fmt.Errorf("durations %s: %w", f, err)
		}
	}
	slices.Sort(ds)
	return ds, nil
}

func ch15Summary(w io.Writer, dir string) error {
	fmt.Fprintln(w, "== ch15 E3: checkpoint size, and every save the "+
		"loop makes over the run")
	fmt.Fprintln(w, "run\tturns\tturn 1\tmiddle\tlast\tsaves\twritten")
	root := filepath.Join(dir, "..", "..") // the code directory
	for _, r := range ch15Runs {
		convs, err := conversations(filepath.Join(root, r.dir), r.script)
		if err != nil {
			return err
		}
		if len(convs) == 0 {
			continue
		}
		sizes, err := checkpointSizes(convs)
		if err != nil {
			return err
		}
		saves := everySave(convs[len(convs)-1])
		all, err := checkpointSizes(saves)
		if err != nil {
			return err
		}
		var total int64
		for _, s := range all {
			total += s
		}
		// Rounded: the saved time's length varies by a byte or two.
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%s\t%d\t%s\n", r.name,
			len(sizes), kb(sizes[0]), kb(sizes[len(sizes)/2]),
			kb(sizes[len(sizes)-1]), len(saves), mb(total))
	}
	ds, err := chatDurations(dir)
	if err != nil || len(ds) == 0 {
		return err
	}
	fmt.Fprintln(w, "\n== ch15 E4: model-call durations in chapter 12's "+
		"live runs, against shutdown grace periods")
	fmt.Fprintln(w, "calls\tmedian\tp95\tmax\tover 10 s\tover 30 s")
	over := func(limit time.Duration) int {
		return len(ds) - sortIndex(ds, limit)
	}
	fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%d\t%d\n", len(ds),
		ds[len(ds)/2].Round(10*time.Millisecond),
		ds[len(ds)*95/100].Round(10*time.Millisecond),
		ds[len(ds)-1].Round(10*time.Millisecond),
		over(10*time.Second), over(30*time.Second))
	return nil
}

func kb(n int64) string { return fmt.Sprintf("%d KB", (n+500)/1000) }

func mb(n int64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/1e6)
}

// runSaveTime is E3's timing, which changes from run to run and so
// isn't in the summary: every save of the recorded batch run, with the
// syncs, on this machine.
func runSaveTime(_ context.Context, _ *env) error {
	convs, err := conversations("ch08/testdata/replies", "batch")
	if err != nil || len(convs) == 0 {
		return err
	}
	ts, err := saveTimes(everySave(convs[len(convs)-1]))
	if err != nil {
		return err
	}
	fmt.Printf("%d saves: median %s, max %s\n", len(ts),
		ts[len(ts)/2].Round(100*time.Microsecond),
		ts[len(ts)-1].Round(100*time.Microsecond))
	return nil
}

// sortIndex is the number of durations at or below limit.
func sortIndex(ds []time.Duration, limit time.Duration) int {
	i, _ := slices.BinarySearch(ds, limit+1)
	return i
}
