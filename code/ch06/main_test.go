package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch06/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch06/replay"
)

var update = flag.Bool("update", false, "rewrite testdata/out/*.txt")

func noKey(string) string { return "" }

// The saved runs the chapter shows, and the settings each runs with.
var shown = map[string]config{
	"fix":          {script: "fix"},
	"fix-again":    {script: "fix-again"},
	"stream-error": {script: "stream-error"},
	"stream-cut":   {script: "stream-cut"},
}

// TestReplayedRuns replays each saved run the chapter shows and compares
// the output with testdata/out/NAME.txt.
func TestReplayedRuns(t *testing.T) {
	for name, c := range shown {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			_ = run(t.Context(), noKey, &out, c) // most end in an error
			golden := "testdata/out/" + name + ".txt"
			if *update {
				if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), want) {
				t.Errorf("output differs from %s:\n%s", golden, out.String())
			}
		})
	}
}

// TestStopReasons replays a saved run for every way a run can end and
// checks the error: its family, and what whyStopped says about it.
// tag::stop-reasons[]

func TestStopReasons(t *testing.T) {
	apiErr := func(status int, retryable bool) func(error) bool {
		return func(err error) bool {
			e, ok := errors.AsType[*openrouter.APIError](err)
			return ok && e.Status == status &&
				e.Retryable() == retryable
		}
	}
	is := func(target error) func(error) bool {
		return func(err error) bool { return errors.Is(err, target) }
	}
	tests := []struct {
		c    config
		want func(error) bool
		why  string
	}{
		{config{script: "fix"},
			func(err error) bool { return err == nil },
			"the model answered"},
		// end::stop-reasons[]
		{config{script: "circles", maxTurns: 4}, is(agent.ErrMaxTurns),
			"the turn limit"},
		{config{script: "fix", maxCost: 0.0002}, is(agent.ErrBudgetExceeded),
			"the cost budget"},
		{config{script: "dup-id"}, is(agent.ErrBadToolCall),
			"a reply the loop can't use"},
		{config{script: "truncated"}, is(agent.ErrOutputTruncated),
			"a reply the loop can't use"},
		{config{script: "filtered"}, is(agent.ErrOutputFiltered),
			"a reply the loop can't use"},
		{config{script: "provider-failed"}, is(agent.ErrProviderFailed),
			"a reply the loop can't use"},
		{config{script: "fix", wrap: func(agent.StreamModel) agent.StreamModel {
			return agent.Streamed(timesOut{})
		}}, is(openrouter.ErrCallTimeout),
			"the model call timed out, worth retrying"},
		{config{script: "stream-error"}, is(agent.ErrProviderFailed),
			"a reply the loop can't use"},
		{config{script: "stream-cut"}, is(agent.ErrIncompleteResponse),
			"a reply the loop can't use"},
		{config{script: "fix", wrap: func(agent.StreamModel) agent.StreamModel {
			return goesQuiet{}
		}}, is(openrouter.ErrStreamIdle),
			"the stream went quiet, worth retrying"},
		{config{script: "status-400"}, apiErr(400, false), "API error 400"},
		{config{script: "status-401"}, apiErr(401, false), "API error 401"},
		{config{script: "status-402"}, apiErr(402, false), "API error 402"},
		{config{script: "status-403"}, apiErr(403, false), "API error 403"},
		{config{script: "status-408"}, apiErr(408, true),
			"API error 408, worth retrying"},
		{config{script: "status-500"}, apiErr(500, true),
			"API error 500, worth retrying"},
		{config{script: "status-502"}, apiErr(502, true),
			"API error 502, worth retrying"},
		{config{script: "status-503"}, apiErr(503, true),
			"API error 503, worth retrying"},
		// tag::stop-reasons[]
		{config{script: "status-429"}, apiErr(429, true),
			"API error 429, worth retrying"},
	}
	for _, tt := range tests {
		name := fmt.Sprintf("%s %v", tt.c.script, tt.c.maxCost)
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			err := run(t.Context(), noKey, &out, tt.c)
			if !tt.want(err) {
				t.Errorf("err = %v", err)
			}
			if got := whyStopped(err); got != tt.why {
				t.Errorf("whyStopped = %q, want %q", got, tt.why)
			}
			if !strings.Contains(out.String(), "stopped: "+tt.why) {
				t.Errorf("output doesn't say why it stopped:\n%s",
					out.String())
			}
		})
	}
}

// end::stop-reasons[]

// TestCancelledRun checks the stop reason when the caller cancels.
func TestCancelledRun(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(errors.New("interrupt signal received"))
	err := run(ctx, noKey, io.Discard, config{script: "fix"})
	if !errors.Is(err, context.Canceled) || whyStopped(err) != "cancelled" {
		t.Errorf("err = %v", err)
	}
}

// TestOutcomes checks what each run achieved rather than the words it
// used: the bug is fixed, the tests ran after the last edit, and the
// run ended with an answer.
func TestOutcomes(t *testing.T) {
	for _, name := range []string{"fix", "fix-again", "recover"} {
		t.Run(name, func(t *testing.T) {
			client, err := newClient(noKey, io.Discard, "")
			if err != nil {
				t.Fatal(err)
			}
			ctx := replay.WithScript(t.Context(), name)
			res, files, err := fix(ctx, client)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(files["duration.go"], "'m': time.Minute,") {
				t.Error("duration.go isn't fixed")
			}
			var tools []string
			for _, m := range res.Messages {
				for _, c := range m.ToolCalls {
					tools = append(tools, c.Name)
				}
			}
			if lastIndex(tools, "run_tests") < lastIndex(tools, "edit_file") {
				t.Errorf("the tests didn't run after the last edit: %v", tools)
			}
			last := res.Messages[len(res.Messages)-1]
			if last.Role != "assistant" || len(last.ToolCalls) > 0 {
				t.Errorf("the run didn't end with an answer: %+v", last)
			}
		})
	}
}

// checkSent is a RoundTripper that fails the test when a request body
// differs from the one saved with the response it replays.
type checkSent struct {
	t      *testing.T
	files  fs.FS
	script string
	n      int
	next   http.RoundTripper
}

func (c *checkSent) RoundTrip(req *http.Request) (*http.Response, error) {
	c.n++
	sent, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("read request: %w", err)
	}
	name := fmt.Sprintf("%s-%02d.json", c.script, c.n)
	data, err := fs.ReadFile(c.files, name)
	if err != nil {
		return nil, fmt.Errorf("saved call: %w", err)
	}
	var rec struct {
		Request jsontext.Value `json:"request"`
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("saved call: %w", err)
	}
	var x, y any
	if json.Unmarshal(sent, &x) != nil ||
		json.Unmarshal(rec.Request, &y) != nil || !reflect.DeepEqual(x, y) {
		c.t.Errorf("%s: the request differs from the saved one", name)
	}
	out := req.Clone(req.Context())
	out.Body = io.NopCloser(bytes.NewReader(sent))
	resp, err := c.next.RoundTrip(out)
	if err != nil {
		return nil, fmt.Errorf("replay: %w", err)
	}
	return resp, nil
}

// TestSavedRequests checks that the agent sends exactly what each saved
// run says it sent: the tools, the tool calls, and the tool results,
// including the real output of go test.
func TestSavedRequests(t *testing.T) {
	files := os.DirFS("testdata/replies")
	for _, name := range []string{"fix", "fix-again", "recover"} {
		t.Run(name, func(t *testing.T) {
			c := &openrouter.Client{Model: savedModel(t, name),
				HTTP: &http.Client{Transport: &checkSent{t: t, files: files,
					script: name, next: &replay.Transport{Files: files}}}}
			ctx := replay.WithScript(t.Context(), name)
			if _, _, err := fix(ctx, c); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// lastIndex returns the index of the last s in list, or -1.
func lastIndex(list []string, s string) int {
	for i, v := range slices.Backward(list) {
		if v == s {
			return i
		}
	}
	return -1
}

func readJSON(t *testing.T, file string, v any) {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

// TestSave checks that a saved conversation loads back unchanged.
func TestSave(t *testing.T) {
	file := t.TempDir() + "/run.json"
	err := run(t.Context(), noKey, io.Discard, config{script: "fix", save: file})
	if err != nil {
		t.Fatal(err)
	}
	var msgs []agent.Message
	readJSON(t, file, &msgs)
	if len(msgs) != 11 || msgs[10].Role != "assistant" ||
		msgs[1].ToolCalls[1].Name != "list_files" {
		t.Errorf("loaded %d messages: %+v", len(msgs), msgs)
	}
}

// cancelAfter is a StreamModel that cancels the run once its nth reply
// has finished streaming, as a person pressing Ctrl-C just as that
// reply's tools are about to run.
type cancelAfter struct {
	agent.StreamModel
	n      int
	cancel context.CancelCauseFunc
}

func (m *cancelAfter) Stream(ctx context.Context,
	req agent.Request) iter.Seq2[agent.Delta, error] {
	return func(yield func(agent.Delta, error) bool) {
		for d, err := range m.StreamModel.Stream(ctx, req) {
			if !yield(d, err) {
				return
			}
		}
		if m.n--; m.n == 0 {
			m.cancel(errors.New("interrupt signal received"))
		}
	}
}

// TestCancelledTranscript replays the fix run, cancels it as the tests
// are about to run in turn 3, and compares what the program prints with
// testdata/out/cancelled.txt, which the chapter shows.
func TestCancelledTranscript(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	var out bytes.Buffer
	err := run(ctx, noKey, &out, config{script: "fix",
		wrap: func(m agent.StreamModel) agent.StreamModel {
			return &cancelAfter{StreamModel: m, n: 3, cancel: cancel}
		}})
	golden(t, "testdata/out/cancelled.txt", out.Bytes())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

// golden compares got with a file, or writes the file with -update.
func golden(t *testing.T, file string, got []byte) {
	t.Helper()
	if *update {
		if err := os.WriteFile(file, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output differs from %s:\n%s", file, got)
	}
}

// timesOut is a Model whose every call fails with the client's own
// deadline, as a slow provider's would.
type timesOut struct{}

func (timesOut) Generate(context.Context, agent.Request) (agent.Reply, error) {
	return agent.Reply{}, fmt.Errorf("send request: %w: %w",
		context.DeadlineExceeded, openrouter.ErrCallTimeout)
}

// fix runs the agent on the duration project with the given model, for
// tests that check the outcome rather than the output.
func fix(ctx context.Context, m agent.StreamModel) (agent.Result,
	agent.FS, error) {
	files := files0()
	msgs := []agent.Message{{Role: "user", Content: task}}
	res, err := agent.RunStream(ctx, m, agent.Tools(files), msgs,
		agent.Limits{MaxTurns: 20})
	return res, files, err //nolint:wrapcheck // a test helper
}

// TestShownStream writes the body of a saved stream, a tool call split
// across chunks, for the chapter to show.
func TestShownStream(t *testing.T) {
	var rec struct {
		Text string `json:"response_text"`
	}
	readJSON(t, "testdata/replies/fix-03.json", &rec)
	shown := longSignature.ReplaceAllString(rec.Text, `"signature":"$1..."`)
	golden(t, "testdata/out/stream.txt", []byte(shown))
}

// savedModel returns the model a saved run was recorded with.
func savedModel(t *testing.T, script string) string {
	var rec struct {
		Request struct {
			Model string `json:"model"`
		} `json:"request"`
	}
	readJSON(t, "testdata/replies/"+script+"-01.json", &rec)
	return rec.Request.Model
}

// longSignature matches a reasoning signature, which runs to a
// kilobyte; the chapter shows its first few characters.
var longSignature = regexp.MustCompile(`"signature":"([^"]{24})[^"]*"`)

// goesQuiet is a StreamModel whose stream fails as an idle one does,
// with the kind of cancellation and ErrStreamIdle as the cause.
type goesQuiet struct{}

func (goesQuiet) Stream(context.Context,
	agent.Request) iter.Seq2[agent.Delta, error] {
	return func(yield func(agent.Delta, error) bool) {
		yield(agent.Delta{}, fmt.Errorf("read stream: %w: %w",
			context.Canceled, openrouter.ErrStreamIdle))
	}
}
