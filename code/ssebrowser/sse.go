package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
)

// tag::data[]

// data is what one event carries to the page. Each kind of event sets
// only its own fields, and omitzero leaves the rest out of the JSON.
type data struct {
	Text    string `json:"text,omitzero"`     // text
	ID      string `json:"id,omitzero"`       // call, result
	Name    string `json:"name,omitzero"`     // call
	Args    string `json:"args,omitzero"`     // call
	Content string `json:"content,omitzero"`  // result
	IsError bool   `json:"is_error,omitzero"` // result
	Turn    int    `json:"turn,omitzero"`     // turn
	Answer  string `json:"answer,omitzero"`   // stopped
	Error   string `json:"error,omitzero"`    // stopped
}

// end::data[]

// tag::encode[]

// encode formats ev as one event in the stream: a line naming its kind,
// a line of JSON data, and a blank line that ends the event. JSON
// escapes newlines, so the data always fits on its one line.
func encode(ev agent.Event) (string, error) {
	name, d, err := wire(ev)
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(d, jsontext.AllowInvalidUTF8(true)) // <1>
	if err != nil {
		return "", fmt.Errorf("encode %s event: %w", name, err)
	}
	return fmt.Sprintf("event: %s\ndata: %s\n\n", name, b), nil
}

// end::encode[]

// tag::wire[]

// wire names an event's kind for the page and picks out its data.
func wire(ev agent.Event) (string, data, error) {
	switch ev.Kind {
	case agent.TextEvent:
		return "text", data{Text: ev.Text}, nil
	case agent.CallEvent:
		c := ev.Call
		return "call", data{ID: c.ID, Name: c.Name,
			Args: string(c.Arguments)}, nil
	case agent.ToolResultEvent:
		t := ev.Tool
		return "result", data{ID: t.ToolCallID, Content: t.Content,
			IsError: t.IsError}, nil
	case agent.TurnEvent:
		return "turn", data{Turn: ev.Turn}, nil
	case agent.StoppedEvent:
		return "stopped", stopped(ev), nil
	}
	return "", data{}, fmt.Errorf("no name for event kind %d", ev.Kind)
}

// stopped is a StoppedEvent's data: the answer, or why there is none.
func stopped(ev agent.Event) data {
	if ev.Err != nil {
		return data{Error: ev.Err.Error()} // <1>
	}
	msgs := ev.Result.Messages
	return data{Answer: msgs[len(msgs)-1].Content}
}

// end::wire[]
