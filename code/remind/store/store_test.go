package store

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"example.com/remind/schedule"
)

var t0 = time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)

func newStore() *Store {
	return New(func() time.Time { return t0 })
}

func TestCreateGet(t *testing.T) {
	s := newStore()
	r := s.Create("u1", "water the plants", t0.Add(time.Hour), nil)
	got, err := s.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "water the plants" || !got.Due.Equal(t0.Add(time.Hour)) {
		t.Errorf("Get = %+v", got)
	}
	if _, err := s.Get("rem_9999"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(missing) error = %v, want ErrNotFound", err)
	}
}

func TestCreateRecurring(t *testing.T) {
	s := newStore()
	rule, err := schedule.Parse("every 2h", nil, t0)
	if err != nil {
		t.Fatal(err)
	}
	r := s.Create("u1", "stretch", time.Time{}, rule)
	if want := t0.Add(2 * time.Hour); !r.Due.Equal(want) {
		t.Errorf("Due = %v, want %v", r.Due, want)
	}
	s.MarkSent(r.ID)
	if want := t0.Add(4 * time.Hour); !r.Due.Equal(want) {
		t.Errorf("after MarkSent, Due = %v, want %v", r.Due, want)
	}
}

func TestList(t *testing.T) {
	s := newStore()
	for _, text := range []string{"a", "b", "c"} {
		s.Create("u1", text, t0, nil)
	}
	if got := s.List("u1", 0, 2); len(got) != 2 || got[0].Text != "a" {
		t.Errorf("List(0, 2) = %v", got)
	}
	if got := s.List("u1", 2, 50); len(got) != 1 || got[0].Text != "c" {
		t.Errorf("List(2, 50) = %v", got)
	}
}

func TestDelete(t *testing.T) {
	s := newStore()
	r := s.Create("u1", "a", t0, nil)
	if err := s.Delete(r.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.List("u1", 0, 50); len(got) != 0 {
		t.Errorf("List after Delete = %v", got)
	}
	if err := s.Delete(r.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete error = %v, want ErrNotFound", err)
	}
	if err := s.Update(r.ID, func(*Reminder) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update after Delete error = %v, want ErrNotFound", err)
	}
}

func TestDue(t *testing.T) {
	s := newStore()
	early := s.Create("u1", "early", t0.Add(-time.Hour), nil)
	s.Create("u1", "later", t0.Add(time.Hour), nil)
	got := s.Due(t0)
	if len(got) != 1 || got[0] != early {
		t.Errorf("Due = %v, want [early]", got)
	}
	s.MarkSent(early.ID)
	if got := s.Due(t0); len(got) != 0 {
		t.Errorf("Due after MarkSent = %v, want none", got)
	}
}

func TestUpcoming(t *testing.T) {
	s := newStore()
	rule, err := schedule.Parse("every 1d", nil, t0)
	if err != nil {
		t.Fatal(err)
	}
	s.Create("u1", "daily", time.Time{}, rule)
	s.Create("u1", "once", t0.Add(30*time.Hour), nil)
	got := s.Upcoming("u1", t0, t0.Add(3*24*time.Hour))
	var texts []string
	for _, o := range got {
		texts = append(texts, o.Text)
	}
	if want := "daily once daily"; strings.Join(texts, " ") != want {
		t.Errorf("Upcoming = %v, want %s", texts, want)
	}
}

func TestOnChange(t *testing.T) {
	s := newStore()
	var seen []string
	s.OnChange(func(r *Reminder) { seen = append(seen, r.ID) })
	r := s.Create("u1", "a", t0, nil)
	if err := s.Update(r.ID, func(r *Reminder) error { r.Done = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Errorf("OnChange calls = %v, want 2", seen)
	}
}

func TestUsers(t *testing.T) {
	s := newStore()
	if err := s.AddUser(User{ID: "u1", Name: "Ana", Token: "tok1"}); err != nil {
		t.Fatal(err)
	}
	u, err := s.UserByToken("tok1")
	if err != nil {
		t.Fatal(err)
	}
	if u.Prefs == nil || u.Prefs.Channel != "email" {
		t.Errorf("new user prefs = %+v, want the defaults", u.Prefs)
	}
	if err := s.AddUser(User{ID: "u1"}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate AddUser error = %v, want ErrConflict", err)
	}
	n := s.ImportUsers([]User{{ID: "u1"}, {ID: "u2", Name: "Bo", Token: "tok2", Prefs: &Prefs{Channel: "email"}}})
	if n != 1 {
		t.Errorf("ImportUsers added %d, want 1", n)
	}
	if err := s.DeleteUser("u2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserByToken("tok2"); !errors.Is(err, ErrNotFound) {
		t.Errorf("token after DeleteUser error = %v, want ErrNotFound", err)
	}
}

func TestSnapshot(t *testing.T) {
	s := newStore()
	if err := s.AddUser(User{ID: "u1", Name: "Ana", Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	s.Create("u1", "a", t0, nil)
	var buf bytes.Buffer
	if err := s.Snapshot(&buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "secret") {
		t.Error("snapshot contains a token")
	}
	if !strings.Contains(buf.String(), "rem_0001") {
		t.Errorf("snapshot = %s, want the reminder", buf.String())
	}
}
