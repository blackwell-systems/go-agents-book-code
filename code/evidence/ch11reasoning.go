package main

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Chapter 11's reasoning experiment (E3): a thinking block from one
// provider of the default model, sent back with the tool result it led
// to, through the same provider and through others, through
// OpenRouter's Messages API. Each provider runs the model under its own
// account, and Anthropic binds a Claude Haiku 5.5 thinking block to the
// account that produced it.

// reasoningOrigin is the provider the thinking block comes from: the
// one that served every call of chapter 8 to 10's recorded runs.
const reasoningOrigin = "claude-on-aws"

// reasoningRoutes are where the continuation goes. "same" sends the
// block back where it came from; "dropped" sends the same request with
// the thinking block left out, for comparison.
// The "edited" routes change the first user message, which comes
// before the thinking block, the way a context policy that clears or
// trims history changes it, and send the block with no binding
// setting, with "drop_block", and with "error".
var reasoningRoutes = []struct{ name, provider, binding string }{
	{"same", reasoningOrigin, ""},
	{"dropped", reasoningOrigin, ""},
	{"edited", reasoningOrigin, ""},
	{"edited, drop_block", reasoningOrigin, "drop_block"},
	{"edited, error", reasoningOrigin, "error"},
	{"anthropic", "anthropic", ""},
	{"azure", "azure/global", ""},
	{"google-vertex", "google-vertex/global", ""},
}

// reasoningRun is one continuation.
type reasoningRun struct {
	Route        string  `json:"route"`
	Run          int     `json:"run"`
	Status       int     `json:"status"`
	Error        string  `json:"error,omitzero"`
	ServedBy     string  `json:"served_by,omitzero"`
	InputTokens  int     `json:"input_tokens"`
	ThinkTokens  int     `json:"thinking_tokens"` // in the continuation
	Cost         float64 `json:"cost"`
	OriginTokens int     `json:"origin_thinking_tokens"`
}

const reasoningPrompt = "A Go service panics with 'index out of range " +
	"[-1]' in duration.Parse when given an empty string. Think about " +
	"which file to read first, then call code_read on it."

const reasoningFile = "package duration\n\n// Parse reads a duration " +
	"such as \"90m\".\nfunc Parse(s string) (Duration, error) {\n\tunit " +
	":= s[len(s)-1]\n\t// ...\n}\n"

var reasoningTools = jsontext.Value(`[{"name":"code_read",` +
	`"description":"Read a Go source file","input_schema":{"type":` +
	`"object","properties":{"path":{"type":"string"}},` +
	`"required":["path"]}}]`)

// runReasoning makes e.n first replies (2 by default) that carry a
// thinking block, and sends each one's continuation along every route.
func runReasoning(ctx context.Context, e *env) error {
	n := e.n
	if n == 0 {
		n = 2
	}
	dir := filepath.Join(e.dir, "reasoning")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("reasoning: %w", err)
	}
	out := &results{file: filepath.Join(e.dir, "reasoning.jsonl")}
	for run := 1; run <= n; run++ {
		first, think, err := e.firstReply(ctx, dir, run)
		if err != nil {
			return err
		}
		for _, r := range reasoningRoutes {
			if err := e.spend.check(); err != nil {
				return err
			}
			content := first
			if r.name == "dropped" {
				content = withoutThinking(first)
			}
			prompt := reasoningPrompt
			if strings.HasPrefix(r.name, "edited") {
				prompt += " (edited)"
			}
			body := reasoningRequest(r.provider, []any{
				map[string]any{"role": "user", "content": prompt},
				map[string]any{"role": "assistant", "content": content},
				map[string]any{"role": "user", "content": toolResults(first)},
			})
			if r.binding != "" {
				body["thinking"] = map[string]any{"type": "adaptive",
					"block_binding": map[string]any{
						"prefix_mismatch_behavior": r.binding}}
			}
			res, err := e.messages(ctx, body, filepath.Join(dir,
				fmt.Sprintf("%02d-%s.json", run,
					strings.NewReplacer(", ", "-", " ", "-").Replace(r.name))))
			if err != nil {
				return err
			}
			res.Route, res.Run, res.OriginTokens = r.name, run, think
			if err := out.add(res); err != nil {
				return err
			}
			fmt.Printf("reasoning %d %s: %d %s input %d\n", run, r.name,
				res.Status, res.ServedBy, res.InputTokens)
		}
	}
	return nil
}

// firstReply asks until a reply holds a thinking block and a tool
// call, up to five times, and returns its content blocks.
func (e *env) firstReply(ctx context.Context, dir string,
	run int) ([]jsontext.Value, int, error) {
	for try := 1; try <= 5; try++ {
		if err := e.spend.check(); err != nil {
			return nil, 0, err
		}
		body := reasoningRequest(reasoningOrigin, []any{map[string]any{
			"role": "user", "content": reasoningPrompt}})
		file := filepath.Join(dir, fmt.Sprintf("%02d-first-%d.json", run, try))
		res, err := e.messages(ctx, body, file)
		if err != nil {
			return nil, 0, err
		}
		var reply struct {
			Content []jsontext.Value `json:"content"`
		}
		if err := readResponse(file, &reply); err != nil {
			return nil, 0, err
		}
		if hasThinking(reply.Content) && len(toolResults(reply.Content)) > 0 {
			return reply.Content, res.ThinkTokens, nil
		}
	}
	return nil, 0, fmt.Errorf("reasoning: no reply with thinking and a call in five tries")
}

func reasoningRequest(provider string, msgs []any) map[string]any {
	return map[string]any{
		"model": "anthropic/claude-haiku-5.5", "max_tokens": 4000,
		"output_config": map[string]any{"effort": "high"},
		"tools":         reasoningTools, "messages": msgs,
		"provider": map[string]any{"only": []string{provider},
			"allow_fallbacks": false},
	}
}

// messages sends one request to OpenRouter's Messages API, saves the
// exchange to file, and returns what it measured.
func (e *env) messages(ctx context.Context, body any,
	file string) (reasoningRun, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return reasoningRun{}, fmt.Errorf("encode: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://openrouter.ai/api/v1/messages", bytes.NewReader(b))
	if err != nil {
		return reasoningRun{}, fmt.Errorf("request: %w", err)
	}
	req.Header.Set("x-api-key", e.key)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "thinking-binding-controls-2026-08-01")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return reasoningRun{}, fmt.Errorf("send: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return reasoningRun{}, fmt.Errorf("read: %w", err)
	}
	rec, err := json.Marshal(map[string]any{"status": resp.StatusCode,
		"request": jsontext.Value(b), "response": jsontext.Value(data)})
	if err != nil {
		rec, _ = json.Marshal(map[string]any{"status": resp.StatusCode,
			"request": jsontext.Value(b), "response_text": string(data)})
	}
	if err := os.WriteFile(file, rec, 0o644); err != nil {
		return reasoningRun{}, fmt.Errorf("save: %w", err)
	}
	return measure(resp.StatusCode, data, e.spend), nil
}

func measure(status int, data []byte, s *spend) reasoningRun {
	var r struct {
		Provider string `json:"provider"`
		Dropped  []struct {
			Reason string `json:"reason"`
		} `json:"input_transformations"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Usage struct {
			Input   int     `json:"input_tokens"`
			Read    int     `json:"cache_read_input_tokens"`
			Write   int     `json:"cache_creation_input_tokens"`
			Cost    float64 `json:"cost"`
			Details struct {
				Thinking int `json:"thinking_tokens"`
			} `json:"output_tokens_details"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(data, &r) // an error body leaves the counts at 0
	s.add(r.Usage.Cost)
	msg := r.Error.Message
	for _, d := range r.Dropped {
		msg = "dropped: " + d.Reason
	}
	return reasoningRun{Status: status, Error: msg,
		ServedBy: r.Provider, Cost: r.Usage.Cost,
		InputTokens: r.Usage.Input + r.Usage.Read + r.Usage.Write,
		ThinkTokens: r.Usage.Details.Thinking}
}

func readResponse(file string, v any) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return fmt.Errorf("read %s: %w", file, err)
	}
	var rec struct {
		Response jsontext.Value `json:"response"`
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	if err := json.Unmarshal(rec.Response, v); err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	return nil
}

func blockType(b jsontext.Value) string {
	var t struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(b, &t)
	return t.Type
}

func hasThinking(blocks []jsontext.Value) bool {
	for _, b := range blocks {
		if t := blockType(b); t == "thinking" || t == "redacted_thinking" {
			return true
		}
	}
	return false
}

func withoutThinking(blocks []jsontext.Value) []jsontext.Value {
	var out []jsontext.Value
	for _, b := range blocks {
		if t := blockType(b); t != "thinking" && t != "redacted_thinking" {
			out = append(out, b)
		}
	}
	return out
}

// toolResults answers every tool_use block with the same file.
func toolResults(blocks []jsontext.Value) []any {
	var out []any
	for _, b := range blocks {
		var u struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if json.Unmarshal(b, &u) == nil && u.Type == "tool_use" {
			out = append(out, map[string]any{"type": "tool_result",
				"tool_use_id": u.ID, "content": reasoningFile})
		}
	}
	return out
}

// reasoningSummary prints each continuation: whether the route took the
// thinking block, and what it counted.
func reasoningSummary(w io.Writer, dir string) error {
	runs, err := load[reasoningRun](filepath.Join(dir, "reasoning.jsonl"))
	if err != nil || len(runs) == 0 {
		return err
	}
	fmt.Fprintln(w, "== reasoning: a Claude Haiku 5.5 thinking block from "+
		reasoningOrigin+", sent back with its tool result")
	fmt.Fprintln(w, "run\troute\tstatus\tserved by\tinput tokens\t"+
		"thinking tokens (origin, continuation)\tcost\terror or note")
	for _, r := range runs {
		fmt.Fprintf(w, "%d\t%s\t%d\t%s\t%d\t%d, %d\t$%.6f\t%s\n", r.Run,
			r.Route, r.Status, r.ServedBy, r.InputTokens, r.OriginTokens,
			r.ThinkTokens, r.Cost, strings.ReplaceAll(r.Error, "\t", " "))
	}
	fmt.Fprintln(w)
	return nil
}
