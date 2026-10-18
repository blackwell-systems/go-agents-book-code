package store_test

import (
	"testing"
	"time"

	"example.com/remind/store"
)

func TestHiddenImportedPrefs(t *testing.T) {
	s := store.New(time.Now)
	s.ImportUsers([]store.User{{ID: "u9", Token: "tok9"}})
	u, err := s.User("u9")
	if err != nil {
		t.Fatal(err)
	}
	if u.Prefs == nil || u.Prefs.Channel == "" {
		t.Errorf("imported user prefs = %+v, want the defaults", u.Prefs)
	}
}
