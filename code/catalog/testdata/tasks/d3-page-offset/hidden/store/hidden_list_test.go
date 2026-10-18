package store_test

import (
	"testing"
	"time"

	"example.com/remind/store"
)

func TestHiddenListPastEnd(t *testing.T) {
	s := store.New(time.Now)
	for range 3 {
		s.Create("u1", "x", time.Now(), nil)
	}
	for _, offset := range []int{3, 4, 40} {
		if got := s.List("u1", offset, 50); len(got) != 0 {
			t.Errorf("List(%d, 50) = %d reminders, want 0", offset, len(got))
		}
	}
	if got := s.List("u1", 1, 50); len(got) != 2 {
		t.Errorf("List(1, 50) = %d reminders, want 2", len(got))
	}
}
