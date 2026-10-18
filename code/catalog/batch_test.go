package catalog

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

func TestBatch(t *testing.T) {
	b, err := Batch(load(t))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for label, ids := range b.Expected {
		for _, id := range ids {
			if other, ok := seen[id]; ok {
				t.Errorf("report %s in groups %s and %s", id, other, label)
			}
			seen[id] = label
		}
	}
	if len(seen) != len(b.Reports) {
		t.Errorf("%d reports, %d in groups", len(b.Reports), len(seen))
	}
	if !slices.Equal(slices.Sorted(maps.Keys(b.Groups)), slices.Sorted(maps.Keys(b.Expected))) {
		t.Errorf("groups %v, expected %v", slices.Sorted(maps.Keys(b.Groups)), slices.Sorted(maps.Keys(b.Expected)))
	}
	var dup, fixed, already int
	for _, g := range b.Groups {
		switch g.Status {
		case "duplicate":
			dup++
		case "fixed":
			fixed++
		case "already_fixed":
			already++
		}
	}
	if dup != len(b.Known) {
		t.Errorf("%d duplicate groups, %d known issues", dup, len(b.Known))
	}
	t.Logf("batch: %d reports in %d groups (%d to fix, %d already fixed, %d filed), %d fix edits",
		len(b.Reports), len(b.Groups), fixed, already, dup, len(b.Fix))

	tree := copyRemind(t)
	for _, e := range b.Fix {
		if err := Apply(tree, e); err != nil {
			t.Errorf("fixes don't combine: %v", err)
		}
	}
	if g := GradeAnswer(b, perfect(b)); !g.Passed() {
		t.Errorf("the expected batch answer fails %v", g.Failed())
	}
	for _, l := range slices.Sorted(maps.Keys(b.Groups)) {
		if g := b.Groups[l]; g.Status != "duplicate" && len(g.RootCause) > 0 && strings.Contains(g.RootCause[0], "parseDays") {
			if n := len(b.Expected[l]); n < 8 {
				t.Errorf("injection cause has %d reports, want one per injection task", n)
			}
		}
	}
}

// TestBatchTests checks the batch's merged hidden tests: they fail on the
// seeded code and pass with every fix applied. Run with -tasks.
func TestBatchTests(t *testing.T) {
	if !*runTasks {
		t.Skip("run with -tasks")
	}
	b, err := Batch(load(t))
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := b.Hidden()
	if err != nil {
		t.Fatal(err)
	}
	if out, ok := goTest(t, hidden, nil, b.Race); ok {
		t.Errorf("batch hidden tests pass on the seeded code:\n%s", out)
	}
	if out, ok := goTest(t, hidden, b.Fix, b.Race); !ok {
		t.Errorf("batch hidden tests fail with every fix:\n%s", tail(out))
	}
}

// TestBatchKnownIssues checks that each issue filed before a batch run
// quotes the panic of a report in the group it describes, not one of
// the other tasks' crashes mixed into its task.
func TestBatchKnownIssues(t *testing.T) {
	tasks, err := Load("testdata/tasks")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Batch(tasks)
	if err != nil {
		t.Fatal(err)
	}
	panics := map[string]string{}
	for _, r := range b.Reports {
		panics[r.ID] = r.Panic
	}
	for _, k := range b.Known {
		ok := false
		for _, id := range b.Expected[k.Group] {
			ok = ok || strings.Contains(k.Body, "Panic: "+panics[id]+"\n")
		}
		if !ok {
			t.Errorf("#%d (%s) quotes a panic from outside its group:\n%s",
				k.ID, k.Title, k.Body)
		}
	}
}
