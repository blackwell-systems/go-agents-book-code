package groups

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackwell-systems/go-agents-book/code/ch12/triage"
)

// tree writes files into a new directory, a Go module, and returns it.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["go.mod"] = "module m\n\ngo 1.22\n"
	for rel, content := range files {
		if err := write(dir, rel, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func contents(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// tag::test-merge[]

// TestMerge merges three groups' workspaces: two that changed different
// lines of one file merge into one; a third that changed a line the
// first changed too conflicts, and none of its changes are applied, not
// even to a file no one else touched.
func TestMerge(t *testing.T) {
	ctx := t.Context()
	orig := map[string]string{"store.go": goFile("a", "b", "c", "d", "e"),
		"api.go": goFile("x")}
	original, merged := tree(t, maps.Clone(orig)), tree(t, maps.Clone(orig))
	groups := []map[string]string{
		{"store.go": goFile("A", "b", "c", "d", "e")},
		{"store.go": goFile("a", "b", "c", "d", "E"),
			"store_test.go": "package m\n\nimport \"testing\"\n\nfunc TestE(t *testing.T) {}\n"},
		{"store.go": goFile("a2", "b", "c", "d", "e"), "api.go": goFile("y")},
	}
	var errs []error
	for _, g := range groups {
		changes, err := changed(original, tree(t, mergeInto(orig, g)))
		if err != nil {
			t.Fatal(err)
		}
		errs = append(errs, apply(ctx, merged, original, changes))
	}
	if errs[0] != nil || errs[1] != nil || !errors.Is(errs[2], errConflict) {
		t.Fatalf("apply errors = %v", errs)
	}
	if got := contents(t, merged, "store.go"); got != goFile("A", "b", "c", "d", "E") {
		t.Errorf("store.go = %q", got)
	}
	if got := contents(t, merged, "api.go"); got != goFile("x") {
		t.Errorf("api.go = %q: the conflicting group's other change got in", got)
	}
}

// end::test-merge[]

// TestSameChange checks that two groups making the same change don't
// conflict: the second finds the file already as it wants it.
func TestSameChange(t *testing.T) {
	orig := map[string]string{"auth.go": goFile("old")}
	original, merged := tree(t, maps.Clone(orig)), tree(t, maps.Clone(orig))
	for range 2 {
		changes, err := changed(original, tree(t, map[string]string{"auth.go": goFile("new")}))
		if err != nil {
			t.Fatal(err)
		}
		if err := apply(t.Context(), merged, original, changes); err != nil {
			t.Fatal(err)
		}
	}
	if got := contents(t, merged, "auth.go"); got != goFile("new") {
		t.Errorf("auth.go = %q", got)
	}
}

// TestSplit checks that reports are grouped by fingerprint, in order.
func TestSplit(t *testing.T) {
	stack := func(fn string) string {
		return "goroutine 1 [running]:\nexample.com/remind/" + fn +
			"(0x1)\n\t/app/remind/x.go:1 +0x1\n"
	}
	rs := []triage.Report{
		{ID: "cr_1", Kind: "panic", Panic: "runtime error: index out of range", Stack: stack("store.List")},
		{ID: "cr_2", Kind: "panic", Panic: "runtime error: invalid memory address", Stack: stack("api.owned")},
		{ID: "cr_3", Kind: "panic", Panic: "runtime error: index out of range [3]", Stack: stack("store.List")},
	}
	gs := Split(rs)
	if len(gs) != 2 || len(gs[0].Reports) != 2 || gs[0].Reports[1] != "cr_3" ||
		gs[1].Reports[0] != "cr_2" {
		t.Errorf("Split = %+v", gs)
	}
}

// TestJoinCauses checks that two groups reporting one root cause, in
// two spellings, become one diagnosis with both groups' reports, and
// that the fixed one is kept over one left for a person.
func TestJoinCauses(t *testing.T) {
	ds := joinCauses([]triage.Diagnosis{
		{Fingerprint: "fp_a", Reports: []string{"cr_1"}, Status: "needs_human",
			RootCause: triage.Location{File: "api/api.go", Function: "(*API).Routes"}},
		{Fingerprint: "fp_b", Reports: []string{"cr_2"}, Status: "duplicate"},
		{Fingerprint: "fp_c", Reports: []string{"cr_3"}, Status: "fixed",
			RootCause: triage.Location{File: "api/api.go", Function: "API.routes"}},
	})
	if len(ds) != 2 || ds[0].Fingerprint != "fp_c" ||
		strings.Join(ds[0].Reports, ",") != "cr_1,cr_3" {
		t.Errorf("joinCauses = %+v", ds)
	}
}

// tag::test-no-build[]

// TestMergeNoBuild merges two groups that each added a test with the
// same name to one package, in different files: the text merges, the
// package doesn't compile, and the second group's changes are undone.
func TestMergeNoBuild(t *testing.T) {
	orig := map[string]string{"store.go": goFile("a")}
	original, merged := tree(t, maps.Clone(orig)), tree(t, maps.Clone(orig))
	test := "package m\n\nimport \"testing\"\n\nfunc TestGet(t *testing.T) {}\n"
	var errs []error
	for _, name := range []string{"get_test.go", "deleted_test.go"} {
		changes, err := changed(original, tree(t, mergeInto(orig,
			map[string]string{name: test})))
		if err != nil {
			t.Fatal(err)
		}
		errs = append(errs, apply(t.Context(), merged, original, changes))
	}
	if errs[0] != nil || !errors.Is(errs[1], errNoBuild) {
		t.Fatalf("apply errors = %v", errs)
	}
	if _, err := os.Stat(filepath.Join(merged, "deleted_test.go")); err == nil {
		t.Error("the second group's test file is still in the tree")
	}
}

// end::test-no-build[]

// goFile is a Go source file declaring one variable per name, one a line.
func goFile(names ...string) string {
	s := "package m\n\n"
	for _, n := range names {
		s += "var " + n + " = 0\n"
	}
	return s
}

// mergeInto returns the original files with a group's versions on top.
func mergeInto(orig, g map[string]string) map[string]string {
	out := maps.Clone(orig)
	maps.Copy(out, g)
	return out
}

// TestApplyWriteFails makes the second write of a group fail, after the
// first has changed a file: the first file is put back as it was, so a
// group's changes are applied all or nothing.
func TestApplyWriteFails(t *testing.T) {
	orig := map[string]string{"a.go": goFile("a")}
	original, merged := tree(t, maps.Clone(orig)), tree(t, maps.Clone(orig))
	changes := map[string]file{
		"a.go":      []byte(goFile("b")),
		"a.go/x.go": []byte("package x\n"), // a.go is a file, so this fails
	}
	if err := apply(t.Context(), merged, original, changes); err == nil {
		t.Fatal("apply succeeded")
	}
	if got := contents(t, merged, "a.go"); got != goFile("a") {
		t.Errorf("a.go = %q, want it put back", got)
	}
}
