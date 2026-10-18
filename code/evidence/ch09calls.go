package main

import (
	"encoding/json/v2"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Chapter 9's first measurement (E1), free and offline: how often a
// model asks for more than one tool in a reply, and how often such a
// reply mixes a call that may change something with others, from the
// saved runs of the fix task (chapter 4's tools) and the triage task
// (chapters 6 and 7). Only requests are read, so the last reply of each
// run isn't counted.

// readOnlyTools are the tools, across the saved runs, that only look
// things up: the ones chapter 9 lets run beside each other.
var readOnlyTools = []string{"read_file", "list_files", "crash_list",
	"crash_read", "crash_search", "crash_logs", "crash_goroutines",
	"crash_similar", "code_list", "code_read", "code_search",
	"issue_search", "issue_read", "notes_read"}

// savedCall is the part of a saved request this measurement reads.
type savedCall struct {
	Request struct {
		Messages []struct {
			Role      string `json:"role"`
			ToolCalls []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	} `json:"request"`
}

// callCounts are one model's replies on one task.
type callCounts struct {
	runs, replies, several, mixed, calls, most int
}

var callNumber = regexp.MustCompile(`^(.*)-(\d+)\.json$`)

// countCalls reads every run saved under dir, one script per run, and
// counts the tool calls in each reply its last request holds.
func countCalls(dir string) (callCounts, error) {
	var c callCounts
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return c, fmt.Errorf("count calls: %w", err)
	}
	last := map[string]string{} // script: its last call's file
	n := map[string]int{}
	for _, f := range files {
		m := callNumber.FindStringSubmatch(filepath.Base(f))
		if m == nil {
			continue
		}
		i, err := strconv.Atoi(m[2])
		if err != nil {
			return c, fmt.Errorf("count calls: %w", err)
		}
		if i > n[m[1]] {
			n[m[1]], last[m[1]] = i, f
		}
	}
	for _, script := range slices.Sorted(maps.Keys(last)) {
		b, err := os.ReadFile(last[script])
		if err != nil {
			return c, fmt.Errorf("count calls: %w", err)
		}
		var saved savedCall
		if err := json.Unmarshal(b, &saved); err != nil {
			return c, fmt.Errorf("count calls: %s: %w", last[script], err)
		}
		c.runs++
		for _, msg := range saved.Request.Messages {
			k := len(msg.ToolCalls)
			if msg.Role != "assistant" || k == 0 {
				continue
			}
			c.replies, c.calls, c.most = c.replies+1, c.calls+k, max(c.most, k)
			if k == 1 {
				continue
			}
			c.several++
			for _, tc := range msg.ToolCalls {
				if !slices.Contains(readOnlyTools, tc.Function.Name) {
					c.mixed++
					break
				}
			}
		}
	}
	return c, nil
}

// callsSummary prints E1's table.
func callsSummary(w io.Writer, dir string) error {
	tasks := []struct{ name, dir string }{
		{"fix", "fix"}, {"triage, ch06 tools", "triage/ch06"},
		{"triage, ch07 tools", "triage/ch07"},
	}
	printed := false
	for _, t := range tasks {
		models, err := os.ReadDir(filepath.Join(dir, t.dir))
		if err != nil {
			continue // no saved runs for this task
		}
		for _, m := range models {
			if !m.IsDir() {
				continue
			}
			c, err := countCalls(filepath.Join(dir, t.dir, m.Name()))
			if err != nil {
				return err
			}
			if c.replies == 0 {
				continue
			}
			if !printed {
				fmt.Fprintln(w, "== calls per reply: replies with tool calls, "+
					"and how many ask for more than one (chapter 9)")
				fmt.Fprintln(w, "task\tmodel\truns\treplies\tseveral\t"+
					"mean calls\tmost\tseveral, with a side effect")
				printed = true
			}
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%.0f%%\t%.2f\t%d\t%d of %d\n",
				t.name, slugModel(m.Name()), c.runs, c.replies,
				100*float64(c.several)/float64(c.replies),
				float64(c.calls)/float64(c.replies), c.most, c.mixed, c.several)
		}
	}
	if printed {
		fmt.Fprintln(w)
	}
	return nil
}

// slugModel turns a recordings directory's name back into a model ID.
func slugModel(dir string) string { return strings.Replace(dir, "_", "/", 1) }
