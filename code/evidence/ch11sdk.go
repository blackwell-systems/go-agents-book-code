package main

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	openrouter "github.com/OpenRouterTeam/go-sdk"
	"github.com/OpenRouterTeam/go-sdk/models/components"
	"github.com/OpenRouterTeam/go-sdk/retry"
)

// Chapter 11's offline SDK experiments: E4, how long OpenRouter's Go SDK
// keeps retrying a failing call with its default settings, and E6,
// whether reasoning records survive a trip through the SDK's types.

// sdkRetryRun is one call to a server that always answers 503.
type sdkRetryRun struct {
	Retries  string    `json:"retries"` // "default" or "none"
	Deadline float64   `json:"deadline_seconds"`
	Attempts int       `json:"attempts"`
	Gaps     []float64 `json:"gaps_seconds"` // between attempts
	Elapsed  float64   `json:"elapsed_seconds"`
	Err      string    `json:"err"`
}

// runSDKRetries calls a local server that answers 503 to everything,
// through the SDK with its default retries and with retries off, each
// under a 10-second deadline. No API key and no network are needed.
func runSDKRetries(ctx context.Context, e *env) error {
	var (
		mu    sync.Mutex
		times []time.Time
	)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			mu.Lock()
			times = append(times, time.Now())
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"code":503,`+
				`"message":"no provider available"}}`)
		}))
	defer srv.Close()
	out := &results{file: filepath.Join(e.dir, "sdkretries.jsonl")}
	for _, mode := range []string{"default", "none"} {
		mu.Lock()
		times = nil
		mu.Unlock()
		opts := []openrouter.SDKOption{openrouter.WithSecurity("test"),
			openrouter.WithServerURL(srv.URL)}
		if mode == "none" {
			opts = append(opts, openrouter.WithRetryConfig(
				retry.Config{Strategy: "none"}))
		}
		sdk := openrouter.New(opts...)
		const deadline = 10 * time.Second
		cctx, cancel := context.WithTimeout(ctx, deadline)
		start := time.Now()
		model := "m"
		_, err := sdk.Chat.Send(cctx, components.ChatRequest{Model: &model,
			Messages: []components.ChatMessages{
				components.CreateChatMessagesUser(components.ChatUserMessage{
					Content: components.CreateChatUserMessageContentStr("hi")})},
		}, nil)
		elapsed := time.Since(start)
		cancel()
		mu.Lock()
		run := sdkRetryRun{Retries: mode, Deadline: deadline.Seconds(),
			Attempts: len(times), Elapsed: round(elapsed.Seconds())}
		for i := 1; i < len(times); i++ {
			run.Gaps = append(run.Gaps, round(times[i].Sub(times[i-1]).Seconds()))
		}
		mu.Unlock()
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			run.Err = "deadline exceeded"
		case err != nil:
			run.Err = "an error after the last attempt"
		}
		if err := out.add(run); err != nil {
			return err
		}
		fmt.Printf("sdkretries %s: %d attempts in %.1fs (%s)\n", mode,
			run.Attempts, run.Elapsed, run.Err)
	}
	return nil
}

func round(s float64) float64 { return float64(int(s*10+0.5)) / 10 }

// sdkRetrySummary prints the SDK retry experiment.
func sdkRetrySummary(w io.Writer, dir string) error {
	runs, err := load[sdkRetryRun](filepath.Join(dir, "sdkretries.jsonl"))
	if err != nil || len(runs) == 0 {
		return err
	}
	fmt.Fprintln(w, "== sdkretries: OpenRouter's Go SDK against a server "+
		"that always answers 503")
	fmt.Fprintln(w, "retries\tdeadline\tattempts\tseconds between "+
		"attempts\telapsed\tended by")
	for _, r := range runs {
		fmt.Fprintf(w, "%s\t%.0fs\t%d\t%v\t%.1fs\t%s\n", r.Retries,
			r.Deadline, r.Attempts, r.Gaps, r.Elapsed, r.Err)
	}
	fmt.Fprintln(w)
	return nil
}

// sdkRoundTripSummary decodes every distinct reasoning record in
// chapters 8 to 10's recorded runs and this harness's recordings into the SDK's union type, encodes
// it again, and counts the records that come back unchanged, compared
// in canonical form.
func sdkRoundTripSummary(w io.Writer, dir string) error {
	var files []string
	for _, ch := range []string{"ch08", "ch09", "ch10"} {
		m, err := filepath.Glob(filepath.Join(dir, "..", "..", ch,
			"testdata", "replies", "*.json"))
		if err != nil {
			return fmt.Errorf("round trip: %w", err)
		}
		files = append(files, m...)
	}
	harness, err := filepath.Glob(filepath.Join(dir, "*", "*", "*.json"))
	if err != nil {
		return fmt.Errorf("round trip: %w", err)
	}
	files = append(files, harness...)
	slices.Sort(files)
	seen := map[string]bool{}
	kinds := map[string]int{}
	total, same := 0, 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return fmt.Errorf("round trip: %w", err)
		}
		var rec struct {
			Request struct {
				Messages []struct {
					ReasoningDetails []jsontext.Value `json:"reasoning_details"`
				} `json:"messages"`
			} `json:"request"`
		}
		if json.Unmarshal(b, &rec) != nil {
			continue
		}
		for _, m := range rec.Request.Messages {
			for _, d := range m.ReasoningDetails {
				key, ok := canonical(d)
				if !ok || seen[key] {
					continue
				}
				seen[key] = true
				total++
				kinds[blockType(d)]++
				var u components.ReasoningDetailUnion
				if json.Unmarshal(d, &u) != nil {
					continue
				}
				back, err := json.Marshal(u)
				if err != nil {
					continue
				}
				if k, ok := canonical(back); ok && k == key {
					same++
				}
			}
		}
	}
	if total == 0 {
		return nil
	}
	fmt.Fprintln(w, "== sdkroundtrip: reasoning records from the "+
		"recorded runs through the SDK's ReasoningDetailUnion")
	fmt.Fprintln(w, "records\tunchanged\tby type")
	types := slices.Sorted(func(yield func(string) bool) {
		for k := range kinds {
			if !yield(k) {
				return
			}
		}
	})
	var parts []string
	for _, k := range types {
		parts = append(parts, fmt.Sprintf("%s %d", k, kinds[k]))
	}
	fmt.Fprintf(w, "%d\t%d\t%v\n\n", total, same, parts)
	return nil
}

// canonical returns a JSON value in its canonical form (RFC 8785), so
// two encodings of the same value compare equal.
func canonical(v []byte) (string, bool) {
	c := jsontext.Value(slices.Clone(v))
	if err := c.Canonicalize(); err != nil {
		return "", false
	}
	return string(c), true
}
