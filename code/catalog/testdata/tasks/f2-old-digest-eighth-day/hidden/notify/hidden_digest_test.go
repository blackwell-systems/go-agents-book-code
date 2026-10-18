package notify_test

import (
	"testing"
	"time"

	"example.com/remind/notify"
	"example.com/remind/store"
)

func TestHiddenDigestEighthDay(t *testing.T) {
	from := time.Date(2026, 8, 25, 14, 0, 0, 0, time.UTC)
	occ := []store.Occurrence{{Text: "late", At: from.AddDate(0, 0, 7).Add(-2 * time.Hour)}}
	defer func() {
		if v := recover(); v != nil {
			t.Errorf("BuildDigest panicked: %v", v)
		}
	}()
	d := notify.BuildDigest(from, 7, time.UTC, occ)
	if len(d.Days) != 7 {
		t.Errorf("days = %d, want 7", len(d.Days))
	}
}
