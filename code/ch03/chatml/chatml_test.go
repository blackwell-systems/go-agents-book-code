package chatml

import (
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch03/agent"
)

func TestRender(t *testing.T) {
	tools := []agent.ToolDef{{
		Name:        "read_file",
		Description: "Read a file.",
		Parameters:  []byte(`{"type":"object","properties":{"path":{"type":"string"}}}`),
	}}
	msgs := []agent.Message{
		{Role: "system", Content: "Be brief."},
		{Role: "user", Content: "Fix it."},
		{Role: "assistant", ToolCalls: []agent.ToolCall{
			{ID: "1", Name: "read_file", Arguments: []byte(`{"path":"a.go"}`)},
			{ID: "2", Name: "read_file", Arguments: []byte(`{"path":"b.go"}`)},
		}},
		{Role: "tool", ToolCallID: "1", Content: "package a"},
		{Role: "tool", ToolCallID: "2", Content: "package b"},
	}
	got, err := Render(msgs, tools)
	if err != nil {
		t.Fatal(err)
	}
	want := `<|im_start|>system
Be brief.

# Tools

You may call one or more functions to assist with the user query.

You are provided with function signatures within <tools></tools> XML tags:
<tools>
{"type": "function", "function": {"name": "read_file", "description": "Read a file.", "parameters": {"type": "object", "properties": {"path": {"type": "string"}}}}}
</tools>

For each function call, return a json object with function name and arguments within <tool_call></tool_call> XML tags:
<tool_call>
{"name": <function-name>, "arguments": <args-json-object>}
</tool_call><|im_end|>
<|im_start|>user
Fix it.<|im_end|>
<|im_start|>assistant
<tool_call>
{"name": "read_file", "arguments": {"path": "a.go"}}
</tool_call>
<tool_call>
{"name": "read_file", "arguments": {"path": "b.go"}}
</tool_call><|im_end|>
<|im_start|>user
<tool_response>
package a
</tool_response>
<tool_response>
package b
</tool_response><|im_end|>
<|im_start|>assistant
`
	if got != want {
		t.Errorf("Render =\n%s\nwant\n%s", got, want)
	}
}

func TestRenderNoSystem(t *testing.T) {
	got, err := Render([]agent.Message{{Role: "user", Content: "Hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "<|im_start|>user\nHi<|im_end|>\n<|im_start|>assistant\n"; got != want {
		t.Errorf("Render = %q, want %q", got, want)
	}
}

func TestRenderBadArguments(t *testing.T) {
	msgs := []agent.Message{{Role: "assistant", ToolCalls: []agent.ToolCall{
		{ID: "1", Name: "read_file", Arguments: []byte(`{"path":`)},
	}}}
	if _, err := Render(msgs, nil); err == nil {
		t.Error("Render accepted arguments that aren't JSON")
	}
}

func TestRenderTextAndCalls(t *testing.T) {
	msgs := []agent.Message{
		{Role: "system", Content: "Be brief."},
		{Role: "assistant", Content: "Let me look.\n", ToolCalls: []agent.ToolCall{
			{ID: "1", Name: "list_files", Arguments: []byte(`{}`)},
		}},
	}
	got, err := Render(msgs, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := "<|im_start|>system\nBe brief.<|im_end|>\n" +
		"<|im_start|>assistant\nLet me look.\n\n<tool_call>\n" +
		`{"name": "list_files", "arguments": {}}` +
		"\n</tool_call><|im_end|>\n<|im_start|>assistant\n"
	if got != want {
		t.Errorf("Render =\n%q\nwant\n%q", got, want)
	}
}
