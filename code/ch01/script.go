package main

import "github.com/blackwell-systems/go-agents-book/code/ch01/agent"

// tag::script[]

// script holds the replies from the run at the start of the chapter,
// recorded from claude-haiku-5.5, so the loop can replay it without a
// model. The model's IDs are shortened, and its text before tool calls
// is left out.
var script = []agent.Message{
	agent.Calls(
		agent.Call("call_1", "run_tests", `{}`),
		agent.Call("call_2", "list_files", `{}`),
	),
	agent.Calls(
		agent.Call("call_3", "read_file", `{"path": "duration.go"}`),
		agent.Call("call_4", "read_file",
			`{"path": "duration_test.go"}`),
	),
	agent.Calls(
		agent.Call("call_5", "edit_file", `{
			"path": "duration.go",
			"old": "'m': time.Second,",
			"new": "'m': time.Minute,"
		}`),
		agent.Call("call_6", "run_tests", `{}`),
	),
	agent.Says("The tests now pass. The cause was a typo in the unit " +
		"table in duration.go: the 'm' (minute) entry mapped to " +
		"time.Second. So \"45m\" parsed as 45 seconds, and \"1h30m\" " +
		"parsed as 1h + 30 seconds. I changed it to time.Minute."),
}

// end::script[]
