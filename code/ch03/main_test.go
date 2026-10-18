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
	"reflect"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch03/agent"
	"github.com/blackwell-systems/go-agents-book/code/ch03/openrouter"
	"github.com/blackwell-systems/go-agents-book/code/ch03/replay"
)

var update = flag.Bool("update", false, "rewrite testdata/out/*.txt")

// TestExperiments runs each experiment and compares its output with
// testdata/out/NAME.txt, which the chapter shows. Run with -update
// after changing an experiment.
func TestExperiments(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "") // always replay
	for _, e := range experiments {
		t.Run(e.name, func(t *testing.T) {
			var out bytes.Buffer
			if err := run(t.Context(), &out, []string{e.name}); err != nil {
				t.Fatalf("run: %v\n%s", err, out.String())
			}
			golden := "testdata/out/" + e.name + ".txt"
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

func TestUnknownExperiment(t *testing.T) {
	if err := run(t.Context(), io.Discard, []string{"nope"}); err == nil {
		t.Error("run accepted an unknown experiment name")
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
	if json.Unmarshal(sent, &x) != nil || json.Unmarshal(rec.Request, &y) != nil ||
		!reflect.DeepEqual(x, y) {
		c.t.Errorf("%s: sent\n%s\nwant\n%s", name, sent, rec.Request)
	}
	out := req.Clone(req.Context())
	out.Body = io.NopCloser(bytes.NewReader(sent))
	resp, err := c.next.RoundTrip(out)
	if err != nil {
		return nil, fmt.Errorf("replay: %w", err)
	}
	return resp, nil
}

// TestSavedProbeRequests checks that the probe sends what the saved
// calls say it sent.
func TestSavedProbeRequests(t *testing.T) {
	files := os.DirFS("testdata/replies")
	c := &openrouter.Client{Model: probeModel, Providers: []string{"novita"},
		HTTP: &http.Client{
			Transport: &checkSent{t: t, files: files, script: "probe",
				next: &replay.Transport{Files: files}},
		}}
	ctx := replay.WithScript(t.Context(), "probe")
	msgs := []agent.Message{{Role: "user",
		Content: "Describe Go in exactly three words."}}
	for _, temp := range []float64{0, 1.2} {
		if _, err := c.Probe(ctx, msgs, temp, 3, 10); err != nil {
			t.Fatal(err)
		}
	}
}
