package agent

import (
	"encoding/json/jsontext"
	"fmt"
)

// tag::final[]

// maxNudges is how many times the loop asks a model that answered in
// prose to use the final tool instead, before giving up on the run.
const maxNudges = 2

// finalTool returns the tool marked Final, if the run has one.
func finalTool(tools []Tool) (Tool, bool) {
	for _, t := range tools {
		if t.Final {
			return t, true
		}
	}
	return Tool{}, false
}

// nudge is the message the loop sends when the model answers in prose
// in a run that ends with a final tool.
func nudge(final Tool) Message {
	return Message{Role: "user", Content: fmt.Sprintf(
		"Don't answer in text. When you're done, call %s with your "+
			"answer; if you aren't done, keep working with the tools.",
		final.Def.Name)}
}

// end::final[]

// tag::final-finished[]

// finished reports whether a reply held a successful call to the final
// tool, and returns its arguments, the run's answer. A call that failed
// went back to the model as an error, and the run goes on.
func finished(byName map[string]Tool, calls []ToolCall,
	answers []Message) (jsontext.Value, bool) {
	for i, c := range calls {
		if byName[c.Name].Final && i < len(answers) &&
			!answers[i].IsError {
			return c.Arguments, true
		}
	}
	return nil, false
}

// end::final-finished[]
