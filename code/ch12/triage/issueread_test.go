package triage

import "testing"

// TestIssueRead checks that issue_read shows an issue in full, with its
// comments, and names issue_search for a number that doesn't exist.
func TestIssueRead(t *testing.T) {
	_, tools := newTestEnv(t, "m1-done-after-delete")
	if _, err := call(t, tools, "issue_comment",
		map[string]any{"id": 101, "body": "Seen three more times."}); err != nil {
		t.Fatal(err)
	}
	got, err := call(t, tools, "issue_read", map[string]any{"id": 101})
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, got, "#101 Crash in auth.parseBearer", "status: open",
		"Filed by the on-call engineer", "comment 1,", "Seen three more times.")
	if !tools["issue_read"].ReadOnly {
		t.Error("issue_read should be read-only")
	}
	_, err = call(t, tools, "issue_read", map[string]any{"id": 999})
	if err == nil {
		t.Fatal("no error for a missing issue")
	}
	mustContain(t, err.Error(), "no issue #999", "issue_search")
}
