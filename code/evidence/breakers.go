package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	agent8 "github.com/blackwell-systems/go-agents-book/code/ch08/agent"
	or8 "github.com/blackwell-systems/go-agents-book/code/ch08/openrouter"
)

// The cache breakers (chapter 8): one saved request from a batch run,
// sent several times in a row each of four ways, pinned to one provider
// so every send can reach the same cache, with chapter 8's cache marks.
// Each change is one a harness can make by accident. The cached tokens
// on each send show how much of the request the provider still read
// from its cache.
//
//	OPENROUTER_API_KEY=... go run ./evidence -run breakers -provider anthropic

// A breaker changes a request before it is sent.
type breaker struct {
	name   string
	change func(req *agent8.Request, now time.Time)
}

var breakers = []breaker{
	// The request as it was, which fills the cache for the rest.
	{"unchanged", func(*agent8.Request, time.Time) {}},
	// A clock in the system prompt: a different first line every send.
	{"timestamp", func(req *agent8.Request, now time.Time) {
		line := "Current time: " + now.UTC().Format(time.RFC3339) + "\n"
		i := slices.IndexFunc(req.Messages, func(m agent8.Message) bool {
			return m.Role == "system"
		})
		if i < 0 {
			req.Messages = slices.Insert(req.Messages, 0,
				agent8.Message{Role: "system", Content: line})
			return
		}
		req.Messages[i].Content = line + req.Messages[i].Content
	}},
	// The same tools in another order, as from ranging over a map.
	{"tools-reversed", func(req *agent8.Request, _ time.Time) {
		slices.Reverse(req.Tools)
	}},
	// The first tool result rewritten, as a policy that edits history
	// might.
	{"early-result", func(req *agent8.Request, _ time.Time) {
		i := slices.IndexFunc(req.Messages, func(m agent8.Message) bool {
			return m.Role == "tool"
		})
		req.Messages[i].Content += "\n(output changed after the fact)"
	}},
}

// breakerReplyTokens caps each reply when the saved request set no
// limit: only the prompt's cached tokens are measured.
const breakerReplyTokens = 2048

var errNoRequest = errors.New("no saved batch request: pass -request, " +
	"or record a batch run under evidence/testdata/cache first")

func runBreakers(ctx context.Context, e *env) error {
	file, err := breakerRequest(filepath.Join(e.dir, "cache"), e.request)
	if err != nil {
		return err
	}
	rec, err := readRecording(file)
	if err != nil {
		return err
	}
	base, err := rec.Request.agentRequest()
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if !slices.ContainsFunc(base.Messages, func(m agent8.Message) bool {
		return m.Role == "tool"
	}) {
		return fmt.Errorf("%s: the request has no tool results to change", file)
	}
	base.MaxTokens = cmp.Or(base.MaxTokens, breakerReplyTokens)
	model, sends := rec.Request.Model, cmp.Or(e.n, 3)
	dir := filepath.Join(e.dir, "breakers", slug(model), e.provider)
	run, err := nextBreakerRun(dir)
	if err != nil {
		return err
	}
	fmt.Printf("breakers: %s, %d messages, %d tools, from %s, pinned to %s\n",
		model, len(base.Messages), len(base.Tools), file, e.provider)
	for _, b := range breakers {
		t := &tee{dir: dir, script: fmt.Sprintf("run-%02d-%s", run, b.name)}
		c := &or8.Client{APIKey: e.key, Model: model,
			Providers: []string{e.provider}, HTTP: &http.Client{Transport: t}}
		for s := 1; s <= sends; s++ {
			if err := e.spend.check(); err != nil {
				return err
			}
			req := agent8.Request{Messages: slices.Clone(base.Messages),
				Tools: slices.Clone(base.Tools), MaxTokens: base.MaxTokens}
			b.change(&req, time.Now())
			win, err := agent8.CacheMarks{}.Build(ctx, req.Messages,
				agent8.ContextState{})
			if err != nil {
				return fmt.Errorf("breakers: %w", err)
			}
			req.Messages = win.Messages
			reply, err := c.Generate(ctx, req)
			e.spend.add(reply.Usage.Cost)
			if err != nil {
				return fmt.Errorf("breakers: %s send %d: %w", b.name, s, err)
			}
			u := reply.Usage
			fmt.Printf("breakers %s %d: prompt %d, cached %d, written %d, $%.4f\n",
				b.name, s, u.PromptTokens, u.CachedTokens, u.CacheWriteTokens, u.Cost)
		}
		if err := t.err(); err != nil {
			return err
		}
	}
	return nil
}

// breakerRequest returns the request to send: the one named, or else
// turn 10 of the first saved Anthropic batch run under dir.
func breakerRequest(dir, named string) (string, error) {
	if named != "" {
		return named, nil
	}
	var found string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if found != "" || d.IsDir() || !strings.HasSuffix(p, "-10.json") {
			return nil
		}
		rec, err := readRecording(p)
		if err != nil {
			return err
		}
		if strings.HasPrefix(rec.Request.Model, "anthropic/") {
			found = p
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) || err == nil && found == "" {
		return "", errNoRequest
	}
	if err != nil {
		return "", fmt.Errorf("find a saved request: %w", err)
	}
	return found, nil
}

// nextBreakerRun returns the number of the next run saved in dir.
func nextBreakerRun(dir string) (int, error) {
	files, err := filepath.Glob(filepath.Join(dir, "run-*-unchanged-01.json"))
	if err != nil {
		return 0, fmt.Errorf("list breaker runs: %w", err)
	}
	return len(files) + 1, nil
}

// breakerFile matches a breakers recording: run, change, send.
var breakerFile = regexp.MustCompile(`^run-(\d+)-(.+)-(\d+)\.json$`)

// breakersSummary prints each run's cached and newly written tokens
// per send, from the recordings.
func breakersSummary(w io.Writer, dir string) error {
	fmt.Fprintln(w, "== breakers: one batch request sent four ways, "+
		"tokens per send (send 1 / 2 / 3)")
	root := filepath.Join(dir, "breakers")
	type key struct {
		model, pinned, change string
		run                   int
	}
	type send struct {
		n   int
		rec recording
	}
	runs := map[key][]send{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		m := breakerFile.FindStringSubmatch(d.Name())
		if d.IsDir() || m == nil {
			return nil
		}
		rel, err := filepath.Rel(root, filepath.Dir(p))
		if err != nil {
			return fmt.Errorf("breakers: %w", err)
		}
		model, pinned, _ := strings.Cut(filepath.ToSlash(rel), "/")
		model = strings.Replace(model, "_", "/", 1) // undo slug
		run, _ := strconv.Atoi(m[1])
		n, _ := strconv.Atoi(m[3])
		rec, err := readRecording(p)
		if err != nil {
			return err
		}
		k := key{model, pinned, m[2], run}
		runs[k] = append(runs[k], send{n, rec})
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) || err == nil && len(runs) == 0 {
		fmt.Fprintln(w, "no recordings yet")
		fmt.Fprintln(w)
		return nil
	}
	if err != nil {
		return fmt.Errorf("read breakers recordings: %w", err)
	}
	fmt.Fprintln(w, "model\tpinned\trun\tchange\tprompt\tcached\twritten\t"+
		"served by\tcost")
	keys := slices.SortedFunc(maps.Keys(runs), func(a, b key) int {
		order := func(c string) int {
			return slices.IndexFunc(breakers, func(x breaker) bool { return x.name == c })
		}
		return cmp.Or(strings.Compare(a.model, b.model),
			strings.Compare(a.pinned, b.pinned), a.run-b.run,
			order(a.change)-order(b.change), strings.Compare(a.change, b.change))
	})
	for _, k := range keys {
		sends := runs[k]
		slices.SortFunc(sends, func(a, b send) int { return a.n - b.n })
		var cached, written []string
		served := map[string]bool{}
		prompt, cost := 0, 0.0
		for _, s := range sends {
			if s.rec.Status != http.StatusOK {
				cached = append(cached, "-")
				written = append(written, "-")
				continue
			}
			u := s.rec.Response.Usage
			prompt = max(prompt, u.PromptTokens)
			cached = append(cached, strconv.Itoa(u.Details.CachedTokens))
			written = append(written, strconv.Itoa(u.Details.CacheWriteTokens))
			served[s.rec.Response.Provider] = true
			cost += u.Cost
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%d\t%s\t%s\t%s\t$%.4f\n", k.model,
			k.pinned, k.run, k.change, prompt, strings.Join(cached, " / "),
			strings.Join(written, " / "),
			strings.Join(slices.Sorted(maps.Keys(served)), ", "), cost)
	}
	fmt.Fprintln(w)
	return nil
}
