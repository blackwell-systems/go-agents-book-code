package triage

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/blackwell-systems/go-agents-book/code/tracker"
)

// A Filed issue is one already in the tracker when a run starts, such
// as a crash an on-call engineer filed by hand.
type Filed struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// OpenTracker returns a tracker kept in dir that holds the filed issues
// with their own numbers, so new issues are numbered after them.
func OpenTracker(dir string, filed []Filed, now func() time.Time) (
	*tracker.Store, error) {
	var issues []tracker.Issue
	for _, f := range filed {
		issues = append(issues, tracker.Issue{ID: f.ID, Title: f.Title,
			Body: f.Body, Status: "open", Created: now()})
	}
	file := filepath.Join(dir, "issues.json")
	b, err := json.Marshal(issues)
	if err != nil {
		return nil, fmt.Errorf("open tracker: %w", err)
	}
	if err := os.WriteFile(file, b, 0o644); err != nil {
		return nil, fmt.Errorf("open tracker: %w", err)
	}
	store, err := tracker.Open(file, now)
	if err != nil {
		return nil, fmt.Errorf("open tracker: %w", err)
	}
	return store, nil
}
