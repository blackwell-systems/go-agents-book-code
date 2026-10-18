package main

import (
	"context"
	"errors"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch06/agent"
)

func TestVote(t *testing.T) {
	tests := []struct {
		name    string
		answers []string // "!" is a voter whose call fails
		quorum  int
		want    string
		wantErr error
	}{
		{
			name:    "a majority",
			answers: []string{"yes", "no", "yes", "no", "yes"},
			quorum:  3,
			want:    "yes",
		},
		{
			name:    "space around an answer is ignored",
			answers: []string{"yes\n", " yes", "no"},
			quorum:  2,
			want:    "yes",
		},
		{
			name:    "case is not",
			answers: []string{"Yes", "yes", "no"},
			quorum:  2,
			wantErr: ErrNoAgreement,
		},
		{
			name:    "a failed call is no vote",
			answers: []string{"!", "yes", "yes"},
			quorum:  2,
			want:    "yes",
		},
		{
			name:    "too many failed calls",
			answers: []string{"!", "!", "yes"},
			quorum:  2,
			wantErr: agent.ErrScriptExhausted,
		},
		{
			name:    "unanimity",
			answers: []string{"a", "a", "b"},
			quorum:  3,
			wantErr: ErrNoAgreement,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			voters := make([]agent.Model, len(tt.answers))
			for i, a := range tt.answers {
				if a == "!" {
					voters[i] = script() // no replies: the call fails
				} else {
					voters[i] = script(a)
				}
			}
			got, err := Vote(t.Context(), voters, "q", tt.quorum)
			if got != tt.want || !errors.Is(err, tt.wantErr) {
				t.Errorf("Vote = %q, %v; want %q, %v",
					got, err, tt.want, tt.wantErr)
			}
		})
	}
}

// TestVoteCancelsTheRest checks that a decided vote cancels the calls
// still running, with errDecided as the cause, and that none of them
// outlives Vote.
func TestVoteCancelsTheRest(t *testing.T) {
	slow := &blocking{}
	voters := []agent.Model{script("yes"), slow, script("yes")}
	got, err := Vote(t.Context(), voters, "q", 2)
	if got != "yes" || err != nil {
		t.Fatalf("Vote = %q, %v; want yes", got, err)
	}
	if !errors.Is(slow.cause, errDecided) { // set before Vote returned
		t.Errorf("slow voter's cause = %v, want errDecided", slow.cause)
	}
}

// TestVoteCancelled checks that the caller's cancellation reaches every
// call and comes back in the error.
func TestVoteCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	voters := []agent.Model{&blocking{}, &blocking{}}
	_, err := Vote(ctx, voters, "q", 1)
	if !errors.Is(err, ErrNoAgreement) || !errors.Is(err, context.Canceled) {
		t.Errorf("Vote error = %v, want ErrNoAgreement and Canceled", err)
	}
}

func TestVotePanicsOnABadQuorum(t *testing.T) {
	for _, q := range []int{0, 3} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Vote with quorum %d of 2 didn't panic", q)
				}
			}()
			_, _ = Vote(t.Context(), []agent.Model{script(), script()}, "q", q)
		}()
	}
}
