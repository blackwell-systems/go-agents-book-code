package triage

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch10/agent"
)

// tag::similarity[]

// Similarity scores how much two crashes' stacks share, from 0 to 1:
// the share of remind's functions on either stack that are on both,
// after the same normalizing fingerprints use. Fingerprints compare the
// one frame where each crash happened; this compares the whole path to
// it, so two crashes that reach the same broken code from different
// places still score high.
func Similarity(a, b Report) float64 {
	fa, fb := set(frames(a, 1000)), set(frames(b, 1000))
	if len(fa) == 0 || len(fb) == 0 {
		return 0
	}
	shared := 0
	for f := range fa {
		if fb[f] {
			shared++
		}
	}
	return float64(shared) / float64(len(fa)+len(fb)-shared)
}

// end::similarity[]

// set returns the distinct strings in ss: a function a stack passes
// through twice counts once.
func set(ss []string) map[string]bool {
	out := make(map[string]bool, len(ss))
	for _, s := range ss {
		out[s] = true
	}
	return out
}

// tag::crash-similar[]

type similarArgs struct {
	ID string `json:"id" desc:"a report ID from crash_list"`
}

func crashSimilar(e *Env) agent.Tool {
	return agent.NewTool("crash_similar",
		"List the reports whose stacks share the most with one report, "+
			"with a score from 0 to 1, including reports with other "+
			"fingerprints. A high score suggests a shared cause worth "+
			"checking, not proof of one.",
		func(_ context.Context, a similarArgs) (string, error) {
			r, err := e.report(a.ID)
			if err != nil {
				return "", err
			}
			ms := e.similar(r)
			if len(ms) == 0 {
				return "no report shares much of its stack with " + r.ID, nil
			}
			var b strings.Builder
			for _, m := range ms[:min(len(ms), 10)] {
				fmt.Fprintf(&b, "%s %.2f %s %s\n", m.r.ID, m.score,
					Fingerprint(m.r), short(m.r.Panic, 80))
			}
			return b.String(), nil
		})
}

// minSimilar is the lowest score crash_similar lists.
const minSimilar = 0.5

// end::crash-similar[]

// A match is a report and how much its stack shares with another's.
type match struct {
	r     Report
	score float64
}

// similar returns the other reports that score minSimilar or more
// against r, highest first.
func (e *Env) similar(r Report) []match {
	var ms []match
	for _, o := range e.Reports {
		if s := Similarity(r, o); o.ID != r.ID && s >= minSimilar {
			ms = append(ms, match{o, s})
		}
	}
	slices.SortStableFunc(ms, func(x, y match) int {
		return cmp.Compare(y.score, x.score)
	})
	return ms
}
