package groups

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/blackwell-systems/go-agents-book/code/ch10/taskrun"
	"github.com/blackwell-systems/go-agents-book/code/ch10/triage"
)

// A Report is what the harness made of every group's work.
type Report struct {
	Diagnoses  []triage.Diagnosis
	Unresolved []*Outcome       // agents that stopped without an answer
	Conflicts  map[string]error // groups whose changes were left out
	Outcome    taskrun.Outcome  // the issues filed and the catalog's grade
}

// tag::finish-merge[]

// Finish merges every group's changes into the job's workspace, in
// group order, files one issue per root cause, and grades the result.
// A group that didn't finish is reported; a group whose changes
// conflict with an earlier group's keeps its diagnosis, left for a
// person; the others go on either way.
func Finish(ctx context.Context, j *taskrun.Job, outs []*Outcome) (
	Report, error) {
	r := Report{Conflicts: map[string]error{}}
	for _, o := range outs {
		if o.Err != nil || o.job == nil { // <1>
			r.Unresolved = append(r.Unresolved, o)
			continue
		}
		changes, err := changed(o.job.Env.Original, o.job.Env.Dir)
		if err != nil {
			return r, err
		}
		ds := o.Diagnoses
		err = apply(ctx, j.Env.Dir, j.Env.Original, changes)
		switch {
		case errors.Is(err, errConflict) || errors.Is(err, errNoBuild):
			r.Conflicts[o.Group.Fingerprint] = err // <2>
			ds = leftOut(ds, err)
		case err != nil:
			return r, err // the tree can't be trusted any more
		}
		r.Diagnoses = append(r.Diagnoses, ds...)
	}
	// end::finish-merge[]
	// tag::finish-file[]
	r.Diagnoses = joinCauses(r.Diagnoses) // <1>
	if err := fileIssues(j, r.Diagnoses); err != nil {
		return r, err
	}
	final, err := json.Marshal(map[string]any{"groups": r.Diagnoses})
	if err != nil {
		return r, fmt.Errorf("finish: %w", err)
	}
	if r.Outcome, err = j.Grade(ctx, final); err != nil {
		return r, err //nolint:wrapcheck // Grade's errors say "grade:"
	}
	return r, nil
}

// end::finish-file[]

// tag::left-out[]

// leftOut marks the diagnoses of a group whose changes weren't merged
// for a person to look at: what the agent found still counts, but its
// fix isn't in the tree, so the group isn't fixed.
func leftOut(ds []triage.Diagnosis, err error) []triage.Diagnosis {
	out := slices.Clone(ds)
	for i := range out {
		if out[i].Status == "fixed" {
			out[i].Status = "needs_human"
			out[i].Evidence = fmt.Sprintf("Its fix wasn't merged (%v). %s",
				err, out[i].Evidence)
		}
	}
	return out
}

// end::left-out[]

// tag::join[]

// joinCauses joins diagnoses with the same root cause into one, with
// all their reports: a cause that crashes in two places has two
// fingerprints, so two agents investigated it, and it is one group. A
// fixed diagnosis is kept over one left for a person.
func joinCauses(ds []triage.Diagnosis) []triage.Diagnosis {
	var out []triage.Diagnosis
	at := map[string]int{}
	for _, d := range ds {
		c, ok := cause(d)
		i, seen := at[c]
		if !ok || !seen {
			if ok {
				at[c] = len(out)
			}
			out = append(out, d)
			continue
		}
		reports := append(out[i].Reports, d.Reports...)
		if out[i].Status != "fixed" && d.Status == "fixed" {
			out[i] = d
		}
		out[i].Reports = reports
	}
	return out
}

// end::join[]

// tag::cause[]

// cause names a diagnosis's root cause, so two agents' answers can be
// compared: the file, and the function without receiver punctuation or
// case, since agents write (*API).Routes and API.routes for one method.
// A duplicate has none: its cause is the issue it duplicates.
func cause(d triage.Diagnosis) (string, bool) {
	if d.Status == "duplicate" {
		return "", false
	}
	fn := strings.ToLower(strings.NewReplacer("(", "", ")", "", "*", "").
		Replace(d.RootCause.Function))
	return d.RootCause.File + " " + fn, true
}

// end::cause[]

// tag::file[]

// fileIssues files an issue for each diagnosis that isn't a duplicate,
// in the job's tracker, and sets its issue number: a fix, a cause left
// for a person, and a crash already fixed, which still needs a record of
// which version crashed. joinCauses has already made each root cause
// one diagnosis.
func fileIssues(j *taskrun.Job, ds []triage.Diagnosis) error {
	for i, d := range ds {
		if _, ok := cause(d); !ok {
			continue
		}
		title := fmt.Sprintf("%s in %s: %s", d.RootCause.Function,
			d.RootCause.File, strings.ReplaceAll(d.Category, "_", " "))
		body := fmt.Sprintf("%s\n\nReports: %s\n\nFix: %s", d.Evidence,
			strings.Join(d.Reports, ", "), d.Fix)
		is, err := j.Env.Issues.Create(title, body, []string{"crash"},
			d.Fingerprint)
		if err != nil {
			return fmt.Errorf("file %s: %w", d.Fingerprint, err)
		}
		ds[i].Issue = is.ID
	}
	return nil
}

// end::file[]
