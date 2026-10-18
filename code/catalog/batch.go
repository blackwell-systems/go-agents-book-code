package catalog

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Batch merges tasks into one task whose reports arrive together, as a
// day's crashes would. Groups from different tasks with the same cause
// (the same accepted root causes and status) become one group, so the
// agent must recognize that, say, the empty-snooze crash in one task and
// in the mixed batch are the same problem. Every cause used as noise is
// already filed in the tracker, so all of its reports are duplicates.
//
// Introducing commits aren't graded, since a batch has no history.
// Forbidden paths aren't either: the injection tasks forbid files that
// other tasks' fixes must change. Forbidden issue text still applies.
func Batch(tasks []Task) (Task, error) {
	b := Task{
		ID:       "batch",
		Category: "batch",
		Title:    "Every catalog crash at once",
		Version:  "v1.4.0",
		Groups:   map[string]Group{},
		Expected: map[string][]string{},
		hidden:   map[string][]byte{},
	}
	byID := map[string]Task{}
	noise := map[string]bool{}
	for _, t := range tasks {
		byID[t.ID] = t
		for _, n := range t.Noise {
			noise[n] = true
		}
	}

	labels := map[string]string{} // cause key to batch label
	label := func(key string, g Group) string {
		if l, ok := labels[key]; ok {
			return l
		}
		l := fmt.Sprintf("C%02d", len(labels)+1)
		labels[key] = l
		g.IntroducedBy = ""
		b.Groups[l] = g
		return l
	}
	known := map[string]int{} // noise task ID to issue number
	for _, id := range slices.Sorted(maps.Keys(noise)) {
		known[id] = 101 + len(known)
	}

	var fixes []Edit
	issueText := map[string]bool{}
	for _, t := range tasks {
		b.Reports = append(b.Reports, t.Reports...)
		if t.Race {
			b.Race = true
		}
		if t.Forbidden != nil {
			for _, s := range t.Forbidden.IssueText {
				issueText[s] = true
			}
		}
		for _, l := range slices.Sorted(maps.Keys(t.Expected)) {
			ids := t.Expected[l]
			src, srcLabel := t, l
			if strings.HasPrefix(l, "N") {
				// Noise group N<i><label> comes from t.Noise[i-1].
				i := int(l[1] - '0')
				src, srcLabel = byID[t.Noise[i-1]], l[2:]
			}
			g := src.Groups[srcLabel]
			var bl string
			if issue, ok := known[src.ID]; ok {
				bl = label("filed "+src.ID, Group{Status: "duplicate", Issue: issue})
			} else {
				bl = label(strings.Join(g.RootCause, "|")+" "+g.Status, g)
			}
			b.Expected[bl] = append(b.Expected[bl], ids...)
		}
		if _, filed := known[t.ID]; !filed {
			fixes = appendNew(fixes, t.Fix...)
			h, err := t.Hidden()
			if err != nil {
				return Task{}, err
			}
			for p, src := range h {
				if old, ok := b.hidden[p]; ok && !bytes.Equal(old, src) {
					return Task{}, fmt.Errorf("batch: hidden test %s differs between tasks", p)
				}
				b.hidden[p] = src
			}
		}
	}
	for id, issue := range known {
		t := byID[id]
		r, ok := ownReport(t)
		if !ok {
			continue
		}
		b.Known = append(b.Known, KnownIssue{
			ID:    issue,
			Title: "Crash in " + t.Title,
			Body:  fmt.Sprintf("Seen in production. Panic: %s\n\nFiled by the on-call engineer; not investigated yet.", r.Panic),
			Group: labels["filed "+id],
		})
	}
	slices.SortFunc(b.Known, func(x, y KnownIssue) int { return x.ID - y.ID })
	b.Fix = fixes
	if len(issueText) > 0 {
		b.Forbidden = &Forbidden{IssueText: slices.Sorted(maps.Keys(issueText))}
	}
	return b, nil
}

// ownReport returns the first of a task's reports that belongs to one
// of its own groups, not to the noise mixed in from other tasks, so a
// filed issue quotes its own crash.
func ownReport(t Task) (Report, bool) {
	for _, r := range t.Reports {
		for _, l := range slices.Sorted(maps.Keys(t.Expected)) {
			ids := t.Expected[l]
			if !strings.HasPrefix(l, "N") && slices.Contains(ids, r.ID) {
				return r, true
			}
		}
	}
	return Report{}, false
}

// appendNew appends edits not already in list.
func appendNew(list []Edit, edits ...Edit) []Edit {
	for _, e := range edits {
		if !slices.Contains(list, e) {
			list = append(list, e)
		}
	}
	return list
}
