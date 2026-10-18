package main

import (
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/catalog"
	agent8 "github.com/blackwell-systems/go-agents-book/code/ch08/agent"
	triage8 "github.com/blackwell-systems/go-agents-book/code/ch08/triage"
)

// Chapter 8's offline measurements: how well crash_similar's score
// finds reports with the same cause, against chapter 7's fingerprints,
// and how close the context policies' token estimate comes to the
// count the provider reports. Both read files already saved, so they
// cost nothing and run with the summary.

// recording is a saved exchange, as tee writes it, with the parts of
// the request and response these measurements read.
type recording struct {
	Status   int          `json:"status"`
	Request  savedRequest `json:"request"`
	Response savedReply   `json:"response"`
	Stream   string       `json:"response_text"` // a streamed reply, as sent
}

type savedRequest struct {
	Model     string         `json:"model"`
	Messages  []savedMessage `json:"messages"`
	Tools     []savedTool    `json:"tools"`
	MaxTokens int            `json:"max_tokens"`
}

type savedMessage struct {
	Role       string         `json:"role"`
	Content    jsontext.Value `json:"content"` // a string or a list of parts
	ToolCallID string         `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
	ReasoningDetails jsontext.Value `json:"reasoning_details"`
}

type savedTool struct {
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  jsontext.Value `json:"parameters"`
		Strict      bool           `json:"strict"`
	} `json:"function"`
}

type savedReply struct {
	Provider string `json:"provider"`
	Usage    struct {
		PromptTokens int `json:"prompt_tokens"`
		Details      struct {
			CachedTokens     int `json:"cached_tokens"`
			CacheWriteTokens int `json:"cache_write_tokens"`
		} `json:"prompt_tokens_details"`
		Cost float64 `json:"cost"`
	} `json:"usage"`
}

func readRecording(file string) (recording, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return recording{}, fmt.Errorf("read recording: %w", err)
	}
	var r recording
	if err := json.Unmarshal(b, &r); err != nil {
		return recording{}, fmt.Errorf("%s: %w", file, err)
	}
	if r.Stream != "" {
		r.Response = streamReply(r.Stream)
	}
	return r, nil
}

// streamReply reads a streamed reply's provider and usage from its
// events: the provider is on every chunk, and the usage on the last.
func streamReply(stream string) savedReply {
	var out savedReply
	for line := range strings.Lines(stream) {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var chunk savedReply
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue // a comment or keep-alive, not a chunk
		}
		out.Provider = cmp.Or(out.Provider, chunk.Provider)
		if chunk.Usage.PromptTokens > 0 {
			out.Usage = chunk.Usage
		}
	}
	return out
}

// agentRequest turns a saved request back into chapter 8's types, as
// the agent held it before the client encoded it. Cache markers are
// dropped; a context policy sets them again.
func (r savedRequest) agentRequest() (agent8.Request, error) {
	out := agent8.Request{MaxTokens: r.MaxTokens}
	for _, m := range r.Messages {
		text, err := contentText(m.Content)
		if err != nil {
			return agent8.Request{}, err
		}
		am := agent8.Message{Role: m.Role, Content: text, ToolCallID: m.ToolCallID}
		if d := m.ReasoningDetails; len(d) > 0 && d.Kind() != 'n' {
			d = slices.Clone(d)
			if err := d.Compact(); err != nil { // recordings are indented
				return agent8.Request{}, fmt.Errorf("reasoning details: %w", err)
			}
			if string(d) != "[]" {
				am.ReasoningDetails = d
			}
		}
		for _, c := range m.ToolCalls {
			am.ToolCalls = append(am.ToolCalls, agent8.ToolCall{ID: c.ID,
				Name: c.Function.Name, Arguments: jsontext.Value(c.Function.Arguments)})
		}
		out.Messages = append(out.Messages, am)
	}
	for _, t := range r.Tools {
		p := slices.Clone(t.Function.Parameters)
		if err := p.Compact(); err != nil {
			return agent8.Request{}, fmt.Errorf("tool %s: %w", t.Function.Name, err)
		}
		out.Tools = append(out.Tools, agent8.ToolDef{Name: t.Function.Name,
			Description: t.Function.Description, Parameters: p,
			Strict: t.Function.Strict})
	}
	return out, nil
}

// contentText reads a message's content: a string, null, or a list of
// text parts, whose texts are joined.
func contentText(v jsontext.Value) (string, error) {
	switch v.Kind() {
	case 0, 'n':
		return "", nil
	case '"':
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return "", fmt.Errorf("content: %w", err)
		}
		return s, nil
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(v, &parts); err != nil {
		return "", fmt.Errorf("content: %w", err)
	}
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String(), nil
}

// similarThresholds are the scores TestSimilarCatalog in ch08/triage
// reports, so the two print the same numbers.
var similarThresholds = []float64{0.3, 0.5, 0.7, 0.9}

// similaritySummary scores every pair of reports in the catalog's
// batch, with fingerprints and with crash_similar's score at several
// thresholds, against the batch's expected groups.
func similaritySummary(w io.Writer, dir string) error {
	tasks, err := catalog.Load(filepath.Join(dir, "..", "..", "catalog",
		"testdata", "tasks"))
	if err != nil {
		return fmt.Errorf("similarity: %w", err)
	}
	batch, err := catalog.Batch(tasks)
	if err != nil {
		return fmt.Errorf("similarity: %w", err)
	}
	b, err := json.Marshal(batch.Reports)
	if err != nil {
		return fmt.Errorf("similarity: %w", err)
	}
	var reports []triage8.Report
	if err := json.Unmarshal(b, &reports); err != nil {
		return fmt.Errorf("similarity: %w", err)
	}
	if len(reports) == 0 {
		return nil
	}
	cause := map[string]string{}
	for c, ids := range batch.Expected {
		for _, id := range ids {
			cause[id] = c
		}
	}
	n := len(reports)
	fmt.Fprintf(w, "== similarity: pairs of batch reports put together, against "+
		"the expected groups (%d reports, %d causes, %d pairs)\n",
		n, len(batch.Expected), n*(n-1)/2)
	fmt.Fprintln(w, "grouping\tprecision\trecall")
	p, r := triage8.PairScore(reports, cause, func(a, b triage8.Report) bool {
		return triage8.Fingerprint(a) == triage8.Fingerprint(b)
	})
	fmt.Fprintf(w, "same fingerprint (chapter 7)\t%.2f\t%.2f\n", p, r)
	for _, th := range similarThresholds {
		p, r := triage8.PairScore(reports, cause, func(a, b triage8.Report) bool {
			return triage8.Similarity(a, b) >= th
		})
		fmt.Fprintf(w, "similarity >= %.1f\t%.2f\t%.2f\n", th, p, r)
	}
	fmt.Fprintln(w)
	return nil
}

// estimateSummary compares agent.Estimate with the prompt tokens the
// provider counted, for every saved request of the fix task and the
// long chat. Estimate counts messages only; the last column adds the
// tool definitions at the same four bytes a token, since the provider
// counts them too.
func estimateSummary(w io.Writer, dir string) error {
	type sample struct {
		set, model        string
		ratio, toolsRatio float64
	}
	var samples []sample
	sets := map[string]string{"fix": filepath.Join(dir, "fix", "*", "*.json"),
		"chat": filepath.Join(dir, "chat", "*", "*.json"),
		"batch": filepath.Join(dir, "..", "..", "ch08", "testdata",
			"replies", "batch-*.json")}
	for _, set := range []string{"fix", "chat", "batch"} {
		files, err := filepath.Glob(sets[set])
		if err != nil {
			return fmt.Errorf("estimate: %w", err)
		}
		for _, f := range files {
			rec, err := readRecording(f)
			if err != nil {
				return err
			}
			actual := rec.Response.Usage.PromptTokens
			if rec.Status != http.StatusOK || actual == 0 {
				continue // a failed call counted nothing
			}
			req, err := rec.Request.agentRequest()
			if err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
			est := agent8.Estimate(req.Messages)
			tools := 0
			for _, t := range req.Tools {
				tools += len(t.Name) + len(t.Description) + len(t.Parameters)
			}
			samples = append(samples, sample{set, rec.Request.Model,
				float64(est) / float64(actual),
				float64(est+tools/4) / float64(actual)})
		}
	}
	if len(samples) == 0 {
		return nil
	}
	fmt.Fprintln(w, "== estimate: agent.Estimate (bytes/4) over the prompt tokens "+
		"counted, per saved request")
	fmt.Fprintln(w, "recordings\tmodel\tcalls\tmean\tmin\tmax\t"+
		"mean with tool definitions")
	for _, set := range []string{"fix", "chat", "batch"} {
		for _, m := range modelsIn(samples, func(s sample) string { return s.model }) {
			var rs, ts []float64
			for _, s := range samples {
				if s.set == set && s.model == m {
					rs = append(rs, s.ratio)
					ts = append(ts, s.toolsRatio)
				}
			}
			if len(rs) == 0 {
				continue
			}
			fmt.Fprintf(w, "%s\t%s\t%d\t%.2f\t%.2f\t%.2f\t%.2f\n", set, m, len(rs),
				mean(rs), slices.Min(rs), slices.Max(rs), mean(ts))
		}
	}
	fmt.Fprintln(w)
	return nil
}
