// Package chatml renders a conversation as the single stream of text a
// model reads, following the ChatML layout of Qwen3's chat template,
// with its way of describing tools and writing tool calls. It is for
// reading, not for sending: the provider does this for you.
package chatml

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch03/agent"
)

// tag::special[]

// The names of the special tokens that mark where a message starts
// and ends. Each stands for one reserved token in the model's
// vocabulary, which a well-built server never produces from text a
// user typed.
const (
	start = "<|im_start|>"
	end   = "<|im_end|>"
)

// block writes one message: the start token, the role, the content,
// and the end token.
func block(b *strings.Builder, role, content string) {
	b.WriteString(start + role + "\n" + content + end + "\n")
}

// end::special[]

// Render returns the text a model reads for this conversation and these
// tools, ending where the model's next reply begins.
func Render(msgs []agent.Message, tools []agent.ToolDef) (string, error) {
	var b strings.Builder
	var system string
	if len(msgs) > 0 && msgs[0].Role == "system" {
		system, msgs = msgs[0].Content, msgs[1:]
	}
	if system != "" || len(tools) > 0 {
		desc, err := describeTools(tools)
		if err != nil {
			return "", err
		}
		block(&b, "system", strings.TrimPrefix(system+desc, "\n\n")) // <1>
	}
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Role {
		case "assistant":
			calls, err := toolCalls(m.ToolCalls)
			if err != nil {
				return "", err
			}
			text := m.Content
			if text != "" && calls != "" {
				text += "\n"
			}
			block(&b, "assistant", text+calls)
		case "tool":
			var results []string // <2>
			for ; i < len(msgs) && msgs[i].Role == "tool"; i++ {
				results = append(results, "<tool_response>\n"+
					msgs[i].Content+"\n</tool_response>")
			}
			i-- // the outer loop moves past the last tool message
			block(&b, "user", strings.Join(results, "\n"))
		default:
			block(&b, m.Role, m.Content)
		}
	}
	b.WriteString(start + "assistant\n") // <3>
	return b.String(), nil
}

// The JSON shapes the template writes for a tool and a tool call.
type (
	function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  jsontext.Value `json:"parameters"`
	}
	tool struct {
		Type     string   `json:"type"`
		Function function `json:"function"`
	}
	call struct {
		Name      string         `json:"name"`
		Arguments jsontext.Value `json:"arguments"`
	}
)

// describeTools is the text that tells the model which tools it has
// and how to call them.
func describeTools(tools []agent.ToolDef) (string, error) {
	if len(tools) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("\n\n# Tools\n\nYou may call one or more functions " +
		"to assist with the user query.\n\nYou are provided with " +
		"function signatures within <tools></tools> XML tags:\n<tools>")
	for _, t := range tools {
		line, err := toJSON(tool{"function",
			function{t.Name, t.Description, t.Parameters}})
		if err != nil {
			return "", fmt.Errorf("tool %s: %w", t.Name, err)
		}
		b.WriteString("\n" + line)
	}
	b.WriteString("\n</tools>\n\nFor each function call, return a json " +
		"object with function name and arguments within " +
		"<tool_call></tool_call> XML tags:\n<tool_call>\n" +
		`{"name": <function-name>, "arguments": <args-json-object>}` +
		"\n</tool_call>")
	return b.String(), nil
}

// toolCalls is the text of the tool calls in an assistant message.
func toolCalls(calls []agent.ToolCall) (string, error) {
	var parts []string
	for _, c := range calls {
		line, err := toJSON(call{c.Name, c.Arguments})
		if err != nil {
			return "", fmt.Errorf("tool call %s: %w", c.ID, err)
		}
		parts = append(parts, "<tool_call>\n"+line+"\n</tool_call>")
	}
	return strings.Join(parts, "\n"), nil
}

// toJSON writes v on one line with a space after each colon and comma,
// as the Python that renders these templates does.
func toJSON(v any) (string, error) {
	b, err := json.Marshal(v,
		jsontext.SpaceAfterColon(true), jsontext.SpaceAfterComma(true))
	if err != nil {
		return "", fmt.Errorf("encode: %w", err)
	}
	return string(b), nil
}
