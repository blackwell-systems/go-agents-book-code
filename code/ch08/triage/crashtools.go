package triage

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch08/agent"
)

// report returns the report with an ID, or an error the model can act
// on.
func (e *Env) report(id string) (Report, error) {
	for _, r := range e.Reports {
		if r.ID == id {
			return r, nil
		}
	}
	return Report{}, fmt.Errorf("no report %q; crash_list shows the IDs", id)
}

// tag::crash-list[]

func crashList(e *Env) agent.Tool {
	return agent.NewTool("crash_list",
		"List every crash report, one line each: its ID, fingerprint, "+
			"how many times it happened, the version that crashed, the "+
			"function inside remind where it happened, and the panic "+
			"message, cut short. Reports with the same fingerprint "+
			"usually share a cause, but check. Start here.",
		func(context.Context, struct{}) (string, error) {
			rs := slices.Clone(e.Reports)
			slices.SortStableFunc(rs, func(a, b Report) int { // <1>
				return strings.Compare(Fingerprint(a), Fingerprint(b))
			})
			var b strings.Builder
			fmt.Fprintf(&b, "%d reports:\n", len(rs))
			for _, r := range rs {
				where := "(no frame in remind)"
				if fs := frames(r, 1); len(fs) > 0 {
					where = fs[0]
				}
				fmt.Fprintf(&b, "%s %s x%d %s %s %s: %s\n", r.ID,
					Fingerprint(r), r.Count, r.Kind, r.Version, where,
					short(r.Panic, 100))
			}
			return b.String(), nil
		})
}

// end::crash-list[]

// short cuts text to n runes on one line, marking the cut.
func short(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

// tag::crash-read[]

type idArgs struct {
	ID string `json:"id" desc:"a report ID from crash_list, such as cr_1e2ea0"`
}

type crashReadArgs struct {
	ID   string `json:"id" desc:"a report ID from crash_list, such as cr_1e2ea0"`
	Full bool   `json:"full,omitzero" desc:"show every stack frame, not just remind's"`
}

func crashRead(e *Env) agent.Tool {
	return agent.NewTool("crash_read",
		"Read one crash report: when and how often it happened, the "+
			"panic, the request being served, and the stack. By default "+
			"the stack shows only remind's own frames, with file and line; "+
			"set full for every frame. Log lines are in crash_logs.",
		func(_ context.Context, a crashReadArgs) (string, error) {
			r, err := e.report(a.ID)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%s: %s in %s (commit %s), %d times, %s to %s\n",
				r.ID, r.Kind, r.Version, r.Commit, r.Count,
				r.FirstSeen.Format("2006-01-02 15:04"),
				r.LastSeen.Format("2006-01-02 15:04"))
			fmt.Fprintf(&b, "fingerprint: %s\npanic: %s\n", Fingerprint(r), r.Panic)
			writeRequest(&b, r.Request)
			switch {
			case r.Kind == "stuck":
				fmt.Fprintf(&b, "stack: a dump of %d goroutines; "+
					"crash_goroutines summarizes it\n", strings.Count(r.Stack, "goroutine "))
			case a.Full:
				fmt.Fprintf(&b, "stack:\n%s\n", limitText(r.Stack, 8<<10))
			default:
				fmt.Fprintf(&b, "stack, remind's frames, innermost first:\n%s",
					remindFrames(r.Stack))
			}
			fmt.Fprintf(&b, "log lines: %d; read them with crash_logs\n", len(r.Logs))
			return b.String(), nil
		})
}

// end::crash-read[]

func writeRequest(b *strings.Builder, req Request) {
	target := req.Path
	if req.Query != "" {
		target += "?" + req.Query
	}
	fmt.Fprintf(b, "request: %s %s\n", req.Method, target)
	for _, k := range slices.Sorted(maps.Keys(req.Headers)) {
		fmt.Fprintf(b, "  %s: %s\n", k, req.Headers[k])
	}
	if req.Body != "" {
		fmt.Fprintf(b, "  body: %s\n", limitText(req.Body, 500))
	}
}

// fileLine matches a stack line's location, such as
// "\t/app/remind/api/reminders.go:42 +0x1c".
var fileLine = regexp.MustCompile(`^\s+\S*?/remind/(\S+:\d+)`)

// remindFrames lists the frames of a stack that belong to remind, with
// their file and line, skipping the crash reporter.
func remindFrames(stack string) string {
	lines := strings.Split(stack, "\n")
	var b strings.Builder
	for i := 0; i+1 < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "example.com/remind/") {
			continue
		}
		fn := normalize(lines[i])
		if strings.HasPrefix(fn, "crash.") {
			continue
		}
		loc := ""
		if m := fileLine.FindStringSubmatch(lines[i+1]); m != nil {
			loc = m[1]
		}
		fmt.Fprintf(&b, "  %s  %s\n", fn, loc)
	}
	return b.String()
}

// limitText cuts s to n bytes, saying so.
func limitText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("\n[%d more bytes not shown]", len(s)-n)
}

type queryArgs struct {
	Query string `json:"query" desc:"text to look for, such as a function name, a path, or words from a panic"`
}

func crashSearch(e *Env) agent.Tool {
	return agent.NewTool("crash_search",
		"Search every crash report for text, ignoring case: panic "+
			"messages, request paths and bodies, stacks, and log lines. "+
			"Lists each match with the report's ID and where it was found.",
		func(_ context.Context, a queryArgs) (string, error) {
			q := strings.ToLower(a.Query)
			if q == "" {
				return "", fmt.Errorf("give a query; crash_list lists everything")
			}
			var hits []string
			for _, r := range e.Reports {
				fields := []struct{ where, text string }{
					{"panic", r.Panic}, {"request", r.Request.Method + " " +
						r.Request.Path + " " + r.Request.Body},
					{"stack", r.Stack}, {"logs", strings.Join(r.Logs, "\n")},
				}
				for _, f := range fields {
					for line := range strings.Lines(f.text) {
						if strings.Contains(strings.ToLower(line), q) {
							hits = append(hits, fmt.Sprintf("%s %s: %s",
								r.ID, f.where, short(line, 160)))
							break // one hit per report and field
						}
					}
				}
			}
			if len(hits) == 0 {
				return "no matches", nil
			}
			out := strings.Join(hits[:min(len(hits), maxMatches)], "\n")
			if len(hits) > maxMatches {
				out += fmt.Sprintf("\n... %d more; narrow the query", len(hits)-maxMatches)
			}
			return out, nil
		})
}

func crashLogs(e *Env) agent.Tool {
	return agent.NewTool("crash_logs",
		"Read the log lines remind wrote just before a crash.",
		func(_ context.Context, a idArgs) (string, error) {
			r, err := e.report(a.ID)
			if err != nil {
				return "", err
			}
			if len(r.Logs) == 0 {
				return "no log lines in " + r.ID, nil
			}
			return limitText(strings.Join(r.Logs, "\n"), 8<<10), nil
		})
}

// tag::crash-goroutines[]

func crashGoroutines(e *Env) agent.Tool {
	return agent.NewTool("crash_goroutines",
		"Summarize the goroutine dump of a stuck request: goroutines with "+
			"identical stacks are grouped and counted, so a dump of "+
			"thousands reads as a few groups. Use it "+
			"for reports of kind stuck.",
		func(_ context.Context, a idArgs) (string, error) {
			r, err := e.report(a.ID)
			if err != nil {
				return "", err
			}
			if r.Kind != "stuck" {
				return "", fmt.Errorf("%s is a panic, not a goroutine dump; "+
					"crash_read shows its stack", r.ID)
			}
			return limitText(summarize(r.Stack), 8<<10), nil
		})
}

// summarize groups the goroutines of a dump whose stacks are the same
// once their arguments are removed, largest group first.
func summarize(dump string) string {
	type group struct {
		state, stack string
		n            int
	}
	var groups []*group
	total := 0
	for g := range strings.SplitSeq(strings.TrimSpace(dump), "\n\n") {
		header, stack, _ := strings.Cut(g, "\n")
		state := header[strings.Index(header, "[")+1:] // "semacquire, 3 minutes]:"
		state = strings.TrimSuffix(state, "]:")
		key := argList.ReplaceAllString(stack, "(...)")
		key = offsets.ReplaceAllString(key, "")
		total++
		i := slices.IndexFunc(groups, func(x *group) bool {
			return x.stack == key && x.state == state
		})
		if i < 0 {
			groups = append(groups, &group{state: state, stack: key})
			i = len(groups) - 1
		}
		groups[i].n++
	}
	slices.SortStableFunc(groups, func(a, b *group) int { return b.n - a.n })
	var b strings.Builder
	fmt.Fprintf(&b, "%d goroutines in %d groups of identical stacks:\n\n", total, len(groups))
	for _, g := range groups {
		fmt.Fprintf(&b, "%d goroutine(s) [%s]:\n%s\n\n", g.n, g.state, g.stack)
	}
	return b.String()
}

var (
	argList = regexp.MustCompile(`(?m)\([^()\n]*\)$`) // a call's arguments
	offsets = regexp.MustCompile(` \+0x[0-9a-f]+`)    // program counter offsets
)

// end::crash-goroutines[]
