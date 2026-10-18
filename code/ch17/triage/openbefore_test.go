package triage

import (
	"strings"
	"testing"
)

// TestOpenBefore: a run restarted under a new ID can't file a second
// issue for a group an earlier run already filed; one run filing two
// causes of one group still can.
func TestOpenBefore(t *testing.T) {
	e, tools := newTestEnv(t, "m1-done-after-delete")
	fp := Fingerprint(e.Reports[0])
	file := func(title string) (string, error) {
		return call(t, tools, "issue_file", map[string]any{"title": title,
			"body": "b", "fingerprint": fp})
	}
	if _, err := file("first cause"); err != nil {
		t.Fatal(err)
	}
	if out, err := file("second cause"); err != nil {
		t.Errorf("a second cause in the same run: %q, %v", out, err)
	}

	again, tools2 := newTestEnv(t, "m1-done-after-delete")
	again.Issues = e.Issues // the same tracker, a new run's records
	_, err := call(t, tools2, "issue_file", map[string]any{"title": "x",
		"body": "b", "fingerprint": fp})
	if err == nil || !strings.Contains(err.Error(), "already open") {
		t.Errorf("a restarted run filing again: %v", err)
	}
}
