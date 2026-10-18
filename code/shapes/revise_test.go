package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
)

const (
	fail = `{"pass": false, "notes": "add the input"}`
	pass = `{"pass": true}`
)

func TestRevise(t *testing.T) {
	tests := []struct {
		name         string
		drafts       []string
		verdicts     []string // the critic model's replies
		maxRevisions int
		want         string
		wantErr      error
		wantDrafts   int
	}{
		{
			name:         "the first draft passes",
			drafts:       []string{"one"},
			verdicts:     []string{pass},
			maxRevisions: 2,
			want:         "one",
			wantDrafts:   1,
		},
		{
			name:         "a revision passes",
			drafts:       []string{"one", "two"},
			verdicts:     []string{fail, "```json\n" + pass + "\n```"},
			maxRevisions: 2,
			want:         "two",
			wantDrafts:   2,
		},
		{
			name:         "out of revisions",
			drafts:       []string{"one", "two"},
			verdicts:     []string{fail, fail},
			maxRevisions: 1,
			want:         "two", // the last draft, with the error
			wantErr:      ErrNotApproved,
			wantDrafts:   2,
		},
		{
			name:         "no revisions allowed",
			drafts:       []string{"one"},
			verdicts:     []string{fail},
			maxRevisions: 0,
			want:         "one",
			wantErr:      ErrNotApproved,
			wantDrafts:   1,
		},
		{
			name:         "the writer fails",
			drafts:       nil,
			maxRevisions: 2,
			wantErr:      agent.ErrScriptExhausted,
			wantDrafts:   1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writer := script(tt.drafts...)
			critic := ModelCritic(script(tt.verdicts...), "rubric")
			got, err := Revise(t.Context(), writer, critic, "task",
				tt.maxRevisions)
			if got != tt.want || !errors.Is(err, tt.wantErr) {
				t.Errorf("Revise = %q, %v; want %q, %v",
					got, err, tt.want, tt.wantErr)
			}
			if writer.calls() != tt.wantDrafts {
				t.Errorf("%d drafts, want %d", writer.calls(),
					tt.wantDrafts)
			}
		})
	}
}

// TestReviseConversation checks what the writer sees when it revises:
// the task, its own draft, and the critic's notes.
func TestReviseConversation(t *testing.T) {
	writer := script("one", "two")
	critic := ModelCritic(script(fail, pass), "rubric")
	if _, err := Revise(t.Context(), writer, critic, "task", 1); err != nil {
		t.Fatal(err)
	}
	msgs := writer.reqs[1].Messages
	if len(msgs) != 3 || msgs[0].Content != "task" ||
		msgs[1].Role != "assistant" || msgs[1].Content != "one" ||
		!strings.HasSuffix(msgs[2].Content, "add the input") {
		t.Errorf("revision request = %+v", msgs)
	}
}

func TestModelCritic(t *testing.T) {
	tests := []struct {
		reply   string
		want    Verdict
		wantErr bool
	}{
		{reply: pass, want: Verdict{Pass: true}},
		{reply: fail, want: Verdict{Notes: "add the input"}},
		{reply: "```\n" + pass + "\n```", want: Verdict{Pass: true}},
		{reply: "Looks good to me!", wantErr: true},
	}
	for _, tt := range tests {
		m := script(tt.reply)
		got, err := ModelCritic(m, "be brief")(t.Context(), "the draft")
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("verdict on %q = %+v, %v; want %+v, error %v",
				tt.reply, got, err, tt.want, tt.wantErr)
		}
		if p := m.last(0); !strings.Contains(p, "be brief") ||
			!strings.Contains(p, "the draft") {
			t.Errorf("critic prompt %q lacks the rubric or draft", p)
		}
	}
}

// TestAllOf checks that a failing check in code spares the model call.
func TestAllOf(t *testing.T) {
	m := script(pass)
	critic := AllOf(maxLength(5), ModelCritic(m, "rubric"))
	v, err := critic(t.Context(), "far too long")
	if err != nil || v.Pass || !strings.Contains(v.Notes, "5") {
		t.Errorf("verdict = %+v, %v; want a fail asking for 5", v, err)
	}
	if m.calls() != 0 {
		t.Errorf("the model was called %d times, want 0", m.calls())
	}
	v, err = critic(t.Context(), "short")
	if err != nil || !v.Pass || m.calls() != 1 {
		t.Errorf("verdict = %+v, %v after %d calls; want a pass after 1",
			v, err, m.calls())
	}
}

// TestReviseCriticError checks that a critic's error stops the loop and
// keeps the draft.
func TestReviseCriticError(t *testing.T) {
	errCritic := errors.New("critic down")
	critic := func(context.Context, string) (Verdict, error) {
		return Verdict{}, errCritic
	}
	got, err := Revise(t.Context(), script("one"), critic, "task", 2)
	if got != "one" || !errors.Is(err, errCritic) {
		t.Errorf("Revise = %q, %v; want one, errCritic", got, err)
	}
}
