package main

import "github.com/blackwell-systems/go-agents-book/code/ch06/agent"

// script is chapter 1's run on the duration project, with a sentence
// before each turn's tool calls, so the page has text to stream.
var script = []agent.Message{
	says("I'll run the tests and look at the files first.",
		agent.Call("call_1", "run_tests", `{}`),
		agent.Call("call_2", "list_files", `{}`)),
	says("TestParse fails on minutes. Let me read the code and test.",
		agent.Call("call_3", "read_file", `{"path": "duration.go"}`),
		agent.Call("call_4", "read_file",
			`{"path": "duration_test.go"}`)),
	says("The unit table maps 'm' to time.Second. I'll fix it and "+
		"run the tests again.",
		agent.Call("call_5", "edit_file", `{
			"path": "duration.go",
			"old": "'m': time.Second,",
			"new": "'m': time.Minute,"
		}`),
		agent.Call("call_6", "run_tests", `{}`)),
	says("The tests now pass. The cause was a typo in the unit " +
		"table in duration.go: the 'm' (minute) entry mapped to " +
		"time.Second. So \"45m\" parsed as 45 seconds, and \"1h30m\" " +
		"parsed as 1h + 30 seconds. I changed it to time.Minute."),
}

// says is a scripted reply with text, and tool calls if any are given.
func says(text string, calls ...agent.ToolCall) agent.Message {
	return agent.Message{Role: "assistant", Content: text,
		ToolCalls: calls}
}
