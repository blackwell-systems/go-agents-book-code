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
	"testing"
	"testing/fstest"
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
