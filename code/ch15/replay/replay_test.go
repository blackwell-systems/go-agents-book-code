package replay

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"
)

func saved(status int, response string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(`{"status": ` + strconv.Itoa(status) +
		`, "request": {}, "response": ` + response + `}`)}
}

// post sends a request with ctx through rt and returns the status and
// body of the response.
func post(ctx context.Context, rt http.RoundTripper, url string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url,
		strings.NewReader(`{"model":"m"}`))
	if err != nil {
		return 0, "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer secret-key")
	resp, err := (&http.Client{Transport: rt}).Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), err
}

// TestTransport checks that each script replays its own saved calls in
// order, and that a call past the end fails.
func TestTransport(t *testing.T) {
	tr := &Transport{Files: fstest.MapFS{
		"a-01.json": saved(200, `{"n": "a1"}`),
		"a-02.json": saved(500, `{"n": "a2"}`),
		"b-01.json": saved(200, `{"n": "b1"}`),
	}}
	a := WithScript(t.Context(), "a")
	b := WithScript(t.Context(), "b")
	steps := []struct {
		ctx    context.Context
		status int
		body   string
	}{
		{a, 200, `{"n": "a1"}`},
		{b, 200, `{"n": "b1"}`},
		{a, 500, `{"n": "a2"}`},
	}
	for i, s := range steps {
		status, body, err := post(s.ctx, tr, "http://example.com/")
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		if status != s.status || body != s.body {
			t.Errorf("call %d = %d %s, want %d %s", i+1, status, body,
				s.status, s.body)
		}
	}
	_, _, err := post(a, tr, "http://example.com/")
	if !errors.Is(err, ErrNoRecording) {
		t.Errorf("extra call: err = %v, want ErrNoRecording", err)
	}
}

// TestRecordThenReplay records a call to a server and replays it, and
// checks that the API key isn't saved.
func TestRecordThenReplay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
			_, _ = io.WriteString(w, `{"answer": 42}`)
		}))
	defer srv.Close()
	dir := t.TempDir()
	ctx := WithScript(t.Context(), "s")

	rec := &Recorder{Dir: dir, Next: srv.Client().Transport}
	status, body, err := post(ctx, rec, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusTeapot || body != `{"answer": 42}` {
		t.Fatalf("recorded call = %d %s", status, body)
	}
	file, err := os.ReadFile(filepath.Join(dir, "s-01.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(file), "secret-key") {
		t.Errorf("recording contains the API key:\n%s", file)
	}
	if !strings.Contains(string(file), `"model": "m"`) {
		t.Errorf("recording lacks the request body:\n%s", file)
	}

	status, body, err = post(ctx, &Transport{Files: os.DirFS(dir)}, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	v := jsontext.Value(body)
	if err := v.Compact(); err != nil {
		t.Fatal(err)
	}
	if status != http.StatusTeapot || string(v) != `{"answer":42}` {
		t.Errorf("replayed call = %d %s", status, body)
	}
}

// TestRecordNotJSON checks that a response body that isn't JSON, such
// as a gateway's error page, is recorded and replayed unchanged.
func TestRecordNotJSON(t *testing.T) {
	const page = "<html>Bad Gateway</html>"
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, page)
		}))
	defer srv.Close()
	dir := t.TempDir()
	ctx := WithScript(t.Context(), "s")

	rec := &Recorder{Dir: dir, Next: srv.Client().Transport}
	if _, _, err := post(ctx, rec, srv.URL); err != nil {
		t.Fatal(err)
	}
	status, body, err := post(ctx, &Transport{Files: os.DirFS(dir)}, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusBadGateway || body != page {
		t.Errorf("replayed call = %d %q", status, body)
	}
}

// TestRecordStreams checks that the recorder hands a response on as it
// arrives, not after it ends, so a slow stream isn't held back past
// the client's idle timeout, and that it still saves the whole body.
func TestRecordStreams(t *testing.T) {
	read := make(chan struct{})
	var late atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "data: one\n\n")
			w.(http.Flusher).Flush()
			select { // the rest waits for the client to read the start
			case <-read:
			case <-time.After(5 * time.Second):
				late.Store(true)
			}
			_, _ = io.WriteString(w, "data: two\n\n")
		}))
	defer srv.Close()
	dir := t.TempDir()
	req, err := http.NewRequestWithContext(WithScript(t.Context(), "s"),
		http.MethodPost, srv.URL, strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	rec := &Recorder{Dir: dir, Next: srv.Client().Transport}
	resp, err := rec.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, len("data: one\n\n"))
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatal(err)
	}
	close(read)
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if late.Load() {
		t.Error("the recorder held the stream until it ended")
	}
	file, err := os.ReadFile(filepath.Join(dir, "s-01.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(file), "data: two") {
		t.Errorf("recording lacks the end of the stream:\n%s", file)
	}
}

// TestResumedReplay checks that a new process's first call finds its
// place in the script: a request recorded as it is gets its call; a
// request that continues the conversation of a saved call, ending with
// the same assistant message, gets that call's response, with the
// earlier assistant messages deciding between calls that end alike;
// and the calls after it follow in order. A request with no assistant
// message starts at the first call, as a new run does. One the script
// doesn't have, a corrupt recording, or two calls that match alike are
// errors, never another call's reply.
func TestResumedReplay(t *testing.T) {
	rec := func(msgs, response string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte(`{"status": 200, ` +
			`"request": {"messages": [` + msgs + `]}, "response": ` +
			response + `}`)}
	}
	user := `{"role": "user", "content": "go"}`
	a1 := `{"role": "assistant", "tool_calls": [{"id": "c1"}]}`
	a2 := `{"role": "assistant", "tool_calls": [{"id": "c2"}]}`
	ok := `{"role": "assistant", "content": "ok"}`
	tool := `{"role": "tool", "content": "ok"}`
	summary := `{"role": "user", "content": "summarize"}`
	join := func(m ...string) string { return strings.Join(m, ",") }
	files := fstest.MapFS{
		"s-01.json":     rec(user, `{"n": 1}`),
		"s-02.json":     rec(join(user, a1, tool), `{"n": 2}`),
		"s-03.json":     rec(join(user, a1, tool, a2, tool), `{"n": 3}`),
		"s-04.json":     rec(summary, `{"n": 4}`),
		"s-05.json":     rec(join(user, a1, tool, ok, user), `{"n": 5}`),
		"s-06.json":     rec(join(user, a2, tool, ok, user), `{"n": 6}`),
		"bad-01.json":   rec(user, `{"n": 1}`),
		"bad-02.json":   {Data: []byte(`{"status": 200, "request": `)},
		"twice-01.json": rec(join(user, a1, tool), `{"n": 1}`),
		"twice-02.json": rec(join(user, a1, tool), `{"n": 2}`),
	}
	for _, tc := range []struct {
		name, script, body string
		want               []string // responses in order; "" an error
	}{
		{"fresh", "s", join(user), []string{`{"n": 1}`, `{"n": 2}`}},
		{"resumed", "s", join(user, a1,
			`{"role": "tool", "content": "found after a restart"}`),
			[]string{`{"n": 2}`, `{"n": 3}`}},
		{"recorded as is", "s", join(summary), []string{`{"n": 4}`}},
		{"ends alike", "s", join(user, a2, tool, ok,
			`{"role": "user", "content": "go on"}`),
			[]string{`{"n": 6}`}},
		{"unknown", "s", join(user, `{"role": "assistant", `+
			`"content": "other"}`), []string{""}},
		{"corrupt", "bad", join(user, a1, tool), []string{""}},
		{"ambiguous", "twice", join(user, a1,
			`{"role": "tool", "content": "again"}`), []string{""}},
	} {
		tr := &Transport{Files: files} // a new process
		ctx := WithScript(t.Context(), tc.script)
		for i, want := range tc.want {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost,
				"http://example.com",
				strings.NewReader(`{"messages": [`+tc.body+`]}`))
			if err != nil {
				t.Fatal(err)
			}
			resp, err := tr.RoundTrip(req)
			if want == "" {
				if err == nil {
					resp.Body.Close()
					t.Errorf("%s: got a response, want an error", tc.name)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s, call %d: %v", tc.name, i+1, err)
			}
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || string(body) != want {
				t.Errorf("%s, call %d: got %s, %v; want %s", tc.name,
					i+1, body, err, want)
			}
		}
	}
}

// TestRecordResumed checks where a new process's Recorder saves its
// first call: a new run's at 1, replacing the old recording; a resumed
// run's request that repeats a saved one in place of that call, since
// the first process may not have finished it; and one that continues
// past every saved call, after the last.
func TestRecordResumed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(w, r.Body) // the reply is the request
		}))
	defer srv.Close()
	ctx := WithScript(t.Context(), "s")
	user := `{"role": "user", "content": "go"}`
	a1 := `{"role": "assistant", "content": "one"}`
	a2 := `{"role": "assistant", "content": "two"}`
	body := func(m ...string) string {
		return `{"messages": [` + strings.Join(m, ",") + `]}`
	}
	send := func(rec *Recorder, b string) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			srv.URL, strings.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := rec.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	dir := t.TempDir()
	first := &Recorder{Dir: dir, Next: srv.Client().Transport}
	send(first, body(user))
	send(first, body(user, a1, user))
	for _, tc := range []struct {
		name, body, file, holds string
	}{
		{"repeats call 2", body(user, a1, `{"role": "user", "content": `+
			`"again"}`), "s-02.json", "again"},
		{"past the end", body(user, a1, user, a2,
			`{"role": "user", "content": "three"}`), "s-03.json", "three"},
		{"new run", body(`{"role": "user", "content": "anew"}`),
			"s-01.json", "anew"},
	} {
		send(&Recorder{Dir: dir, Next: srv.Client().Transport}, tc.body)
		data, err := os.ReadFile(filepath.Join(dir, tc.file))
		if err != nil || !strings.Contains(string(data), tc.holds) {
			t.Errorf("%s: %s doesn't hold the request (%v)", tc.name,
				tc.file, err)
		}
	}
	if names, _ := filepath.Glob(filepath.Join(dir, "s-*.json")); len(names) != 3 {
		t.Errorf("recordings %v, want three", names)
	}
}
