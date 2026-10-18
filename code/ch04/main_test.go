package main

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch04/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch04/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch04/replay"
)

var update = flag.Bool("update", false, "rewrite testdata/out/*.txt")

func noKey(string) string { return "" }

// TestReplayedRuns replays each saved run and compares the output with
// testdata/out/NAME.txt, which the chapter shows.
func TestReplayedRuns(t *testing.T) {
	for _, name := range []string{"fix", "fix-again", "recover"} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			if err := run(t.Context(), noKey, &out, name, "", ""); err != nil {
				t.Fatalf("run: %v\n%s", err, out.String())
			}
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

// TestShownJSON checks that the JSON the chapter shows matches the
// saved run "fix": its first request, with one of the four tools, its
// first reply, and messages 4 to 6 of its fourth request, with long
// tool results shortened. Run with -update to write the files.
func TestShownJSON(t *testing.T) {
	type call struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	type message struct {
		Role       string `json:"role"`
		Content    string `json:"content,omitzero"`
		ToolCalls  []call `json:"tool_calls,omitzero"`
		ToolCallID string `json:"tool_call_id,omitzero"`
	}
	type request struct {
		Model    string           `json:"model"`
		Messages []message        `json:"messages"`
		Tools    []jsontext.Value `json:"tools"`
	}
	var first, fourth struct {
		Request  request        `json:"request"`
		Response jsontext.Value `json:"response"`
	}
	readJSON(t, "testdata/replies/fix-01.json", &first)
	readJSON(t, "testdata/replies/fix-04.json", &fourth)
	first.Request.Tools = first.Request.Tools[1:2] // read_file
	msgs := fourth.Request.Messages
	var report []byte // the first failing run_tests result
	for _, m := range msgs {
		if report == nil && strings.Contains(m.Content, "--- FAIL") {
			report = []byte(m.Content + "\n")
		}
	}
	for i, m := range msgs {
		if line, _, more := strings.Cut(m.Content, "\n"); more {
			line = strings.TrimSpace(line)
			if r := []rune(line); len(r) > 36 {
				line = string(r[:36])
			}
			msgs[i].Content = line + " ..."
		}
	}
	for file, v := range map[string]any{
		"testdata/out/request.json": first.Request,
		"testdata/out/reply.json":   first.Response,
		"testdata/out/turn4.json":   msgs[4:7],
		"testdata/out/report.txt":   report,
	} {
		b, ok := v.([]byte)
		if !ok {
			var err error
			b, err = json.Marshal(v, jsontext.Multiline(true),
				jsontext.WithIndent("  "))
			if err != nil {
				t.Fatal(err)
			}
			b = append(b, '\n')
			b = longSignature.ReplaceAll(b, []byte(`"signature": "$1..."`))
		}
		if *update {
			if err := os.WriteFile(file, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, want) {
			t.Errorf("%s differs from the saved run; run with -update", file)
		}
	}
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
	err := run(t.Context(), noKey, io.Discard, "fix", "", file)
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
var longSignature = regexp.MustCompile(`"signature": "([^"]{24})[^"]*"`)

// TestSaveInvalidArguments checks that a conversation whose tool call
// has broken arguments still saves as valid JSON, with the arguments
// kept as a string, and that the messages passed in are unchanged.
func TestSaveInvalidArguments(t *testing.T) {
	bad := `{"path": "a.go", , "end": 3}`
	msgs := []agent.Message{{Role: "assistant", ToolCalls: []agent.ToolCall{
		{ID: "1", Name: "read", Arguments: []byte(`{"path": "a.go"}`)},
		{ID: "2", Name: "read", Arguments: []byte(bad)},
	}}}
	file := filepath.Join(t.TempDir(), "run.json")
	if err := saveConversation(file, msgs); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"{\"path\": \"a.go\", , \"end\": 3}"`) {
		t.Errorf("saved file doesn't hold the arguments as a string:\n%s", b)
	}
	if got := string(msgs[0].ToolCalls[1].Arguments); got != bad {
		t.Errorf("caller's arguments changed to %s", got)
	}
}
