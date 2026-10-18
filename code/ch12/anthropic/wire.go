package anthropic

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch12/agent"
)

// The Messages API's JSON shapes. Only the fields the client uses are
// declared; the API ignores none of these, and the decoder skips the
// fields it doesn't know.

// tag::request[]

type request struct {
	Model     string   `json:"model"`
	MaxTokens int      `json:"max_tokens"` // required
	System    []block  `json:"system,omitzero"`
	Messages  []turn   `json:"messages"`
	Tools     []tool   `json:"tools,omitzero"`
	Output    *output  `json:"output_config,omitzero"`
	Thinking  thinking `json:"thinking"`
	Stream    bool     `json:"stream,omitzero"`
}

// thinking is the request's thinking setting: adaptive, as Claude 5
// models require, and stale blocks dropped rather than refused.
type thinking struct {
	Type    string  `json:"type"` // "adaptive"
	Binding binding `json:"block_binding"`
}

type binding struct {
	OnMismatch string `json:"prefix_mismatch_behavior"`
}

// A turn is one message: a role and a list of content blocks. The
// roles are only "user" and "assistant", and they alternate.
type turn struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

// end::request[]

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitzero"`
	InputSchema jsontext.Value `json:"input_schema"`
	Strict      bool           `json:"strict,omitzero"`
}

type output struct {
	Effort string  `json:"effort,omitzero"`
	Format *format `json:"format,omitzero"`
}

type format struct {
	Type   string         `json:"type"` // "json_schema"
	Schema jsontext.Value `json:"schema"`
}

// tag::block[]

// A block is one piece of a message's content. Which fields it uses
// depends on its type: "text", "tool_use", "tool_result", "thinking",
// or "redacted_thinking".
type block struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitzero"`
	ID        string         `json:"id,omitzero"`          // tool_use
	Name      string         `json:"name,omitzero"`        // tool_use
	Input     jsontext.Value `json:"input,omitzero"`       // tool_use
	ToolUseID string         `json:"tool_use_id,omitzero"` // result
	Content   string         `json:"content,omitzero"`     // result
	IsError   bool           `json:"is_error,omitzero"`    // result
	Cache     *cacheControl  `json:"cache_control,omitzero"`
}

type cacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

// end::block[]

// The response: one message, whose content blocks are kept raw so a
// thinking block can be sent back exactly as it came.
type message struct {
	ID         string           `json:"id"` // such as "msg_..." (chapter 12)
	Content    []jsontext.Value `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      usage            `json:"usage"`
}

type usage struct {
	Cost         *float64 `json:"cost"` // added by OpenRouter, if at all
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	CacheRead    int      `json:"cache_read_input_tokens"`
	CacheWrite   int      `json:"cache_creation_input_tokens"`
	Details      struct {
		ThinkingTokens int `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

// tag::wire-request[]

// wireRequest maps the agent's request onto the Messages API: system
// messages move to the top-level system field, the rest become turns,
// and MaxTokens gets a default because the API requires one.
func (c *Client) wireRequest(req agent.Request, stream bool) request {
	out := request{Model: c.Model, Stream: stream,
		MaxTokens: req.MaxTokens, Thinking: thinking{"adaptive",
			binding{"drop_block"}}} // <1>
	if out.MaxTokens == 0 {
		out.MaxTokens = DefaultMaxTokens
	}
	for _, m := range req.Messages {
		if m.Role == "system" {
			if m.Content != "" { // the API rejects an empty block
				out.System = append(out.System, textBlock(m.Content,
					m.CachePoint))
			}
			continue
		}
		out.Messages = addTurn(out.Messages, c.turnFor(m))
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, tool{t.Name, t.Description,
			t.Parameters, t.Strict})
	}
	out.Output = c.output(req.ResponseFormat)
	return out
}

// end::wire-request[]

func (c *Client) output(schema jsontext.Value) *output {
	if c.Effort == "" && schema == nil {
		return nil
	}
	o := &output{Effort: c.Effort}
	if schema != nil {
		o.Format = &format{Type: "json_schema", Schema: schema}
	}
	return o
}

// tag::turn-for[]

// turnFor maps one message onto a turn. A tool message becomes a
// tool_result block in a user turn; addTurn joins it to the results
// before it, since all of one reply's results go in one message.
func (c *Client) turnFor(m agent.Message) turn {
	switch m.Role {
	case "tool":
		b := block{Type: "tool_result", ToolUseID: m.ToolCallID,
			Content: m.Content, IsError: m.IsError} // <1>
		return turn{"user", []block{withCache(b, m.CachePoint)}}
	case "assistant":
		return turn{"assistant", c.assistantBlocks(m)}
	}
	return turn{"user", []block{textBlock(m.Content, m.CachePoint)}}
}

// addTurn appends t, joining it to the last turn when both have the
// same role, since the API requires the roles to alternate.
func addTurn(turns []turn, t turn) []turn {
	if n := len(turns); n > 0 && turns[n-1].Role == t.Role {
		turns[n-1].Content = append(turns[n-1].Content, t.Content...)
		return turns
	}
	return append(turns, t)
}

// end::turn-for[]

// tag::assistant-blocks[]

// assistantBlocks maps an assistant message onto content blocks. A
// reply this client received goes back as it came, from the content
// kept in ReasoningDetails, since the API rejects thinking blocks that
// are reordered or rebuilt; any other message is built from its text
// and its calls.
func (c *Client) assistantBlocks(m agent.Message) []block {
	if m.ReasoningFrom == c.Name() { // <1>
		if raw := rawBlocks(m.ReasoningDetails); len(raw) > 0 {
			return markLast(raw, m.CachePoint)
		}
	}
	var out []block
	if m.Content != "" {
		out = append(out, block{Type: "text", Text: m.Content})
	}
	for _, tc := range m.ToolCalls {
		input := tc.Arguments
		if !input.IsValid() || input.Kind() != '{' { // <2>
			input = jsontext.Value(`{}`)
		}
		out = append(out, block{Type: "tool_use", ID: tc.ID,
			Name: tc.Name, Input: input})
	}
	if n := len(out); n > 0 && m.CachePoint {
		out[n-1] = withCache(out[n-1], true)
	}
	return out
}

// end::assistant-blocks[]

// rawBlocks returns the blocks recorded in ReasoningDetails: the
// reply's whole content, a JSON array of blocks exactly as the API sent
// them.
func rawBlocks(details jsontext.Value) []block {
	var raw []jsontext.Value
	if json.Unmarshal(details, &raw) != nil {
		return nil
	}
	out := make([]block, 0, len(raw))
	for _, r := range raw {
		out = append(out, block{Type: rawType, Input: r})
	}
	return out
}

// markLast puts a cache marker on the last block that can carry one:
// moving a marker doesn't change a thinking block's prefix, but a
// thinking block itself can't be marked.
func markLast(blocks []block, cache bool) []block {
	for i := len(blocks) - 1; cache && i >= 0; i-- {
		var b map[string]jsontext.Value
		if json.Unmarshal(blocks[i].Input, &b) != nil {
			continue
		}
		var t string
		_ = json.Unmarshal(b["type"], &t)
		if t == "thinking" || t == "redacted_thinking" {
			continue
		}
		b["cache_control"] = jsontext.Value(`{"type":"ephemeral"}`)
		if marked, err := json.Marshal(b); err == nil {
			blocks[i].Input = marked
		}
		break
	}
	return blocks
}

// rawType marks a block whose Input holds a whole block to send as is.
const rawType = "raw"

func textBlock(text string, cache bool) block {
	return withCache(block{Type: "text", Text: text}, cache)
}

func withCache(b block, cache bool) block {
	if cache {
		b.Cache = &cacheControl{Type: "ephemeral"}
	}
	return b
}

// MarshalJSONTo writes a raw block exactly as it was received, and
// every other block field by field. An empty text block is rejected by
// the API, so a text block always has text when it's written.
func (b block) MarshalJSONTo(enc *jsontext.Encoder) error {
	if b.Type == rawType {
		return enc.WriteValue(b.Input) //nolint:wrapcheck // json/v2 wraps it
	}
	type plain block                         // without this method
	return json.MarshalEncode(enc, plain(b)) //nolint:wrapcheck // as above
}

// tag::to-reply[]

// toReply maps a response onto the agent's reply: text blocks join into
// the content, tool_use blocks become calls, and the whole content,
// thinking blocks and all, is kept in ReasoningDetails to send back.
func (c *Client) toReply(m message) agent.Reply {
	r := agent.Reply{ID: m.ID}
	var text strings.Builder
	for _, raw := range m.Content {
		var b block
		if json.Unmarshal(raw, &b) != nil {
			continue // a block this client doesn't know; kept below
		}
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "tool_use":
			r.Message.ToolCalls = append(r.Message.ToolCalls,
				agent.ToolCall{ID: b.ID, Name: b.Name,
					Arguments: b.Input})
		}
	}
	r.Message.Role, r.Message.Content = "assistant", text.String()
	if all, err := json.Marshal(m.Content); err == nil { // <1>
		r.Message.ReasoningDetails = all
		r.Message.ReasoningFrom = c.Name()
	}
	r.FinishReason = finishReason(m.StopReason)
	r.Usage = c.toUsage(m.Usage)
	return r
}

// end::to-reply[]

// tag::finish-reason[]

// finishReason maps a stop reason onto the finish reasons the loop
// checks (chapter 5). A refusal is its own reason, which the loop turns
// into ErrRefused. pause_turn comes only from server-side tools, which
// this agent doesn't use, so it can't be continued here.
func finishReason(stop string) string {
	switch stop {
	case "end_turn", "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	case "refusal":
		return "refusal"
	}
	return "error" // pause_turn, or a reason this client doesn't know
}

// end::finish-reason[]

// tag::to-usage[]

// toUsage maps the API's counts onto Usage. The API's input_tokens
// leaves out tokens read from or written to the cache, so the prompt
// is all three together. The cost comes from the price table, unless
// the server reported what it billed, as OpenRouter's does.
func (c *Client) toUsage(u usage) agent.Usage {
	out := agent.Usage{
		PromptTokens:     u.InputTokens + u.CacheRead + u.CacheWrite,
		CompletionTokens: u.OutputTokens,
		CachedTokens:     u.CacheRead,
		CacheWriteTokens: u.CacheWrite,
		ReasoningTokens:  u.Details.ThinkingTokens,
	}
	out.Cost = c.Prices.Cost(c.Model, out)
	out.CostEstimated = true
	if u.Cost != nil { // <1>
		out.Cost, out.CostEstimated = *u.Cost, false
	}
	return out
}

// end::to-usage[]
