package share

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

var start = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

// newLists returns lists holding one list, "Groceries", owned by ann,
// with ed as an editor, vic as a viewer, and one reminder.
func newLists(t *testing.T) (*Lists, string) {
	t.Helper()
	s := New(func() time.Time { return start })
	l, err := s.Create("ann", "Groceries")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(l.ID, "ann", "ed", Editor); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(l.ID, "ann", "vic", Viewer); err != nil {
		t.Fatal(err)
	}
	if err := s.AddReminder(l.ID, "ann", "rem_0001"); err != nil {
		t.Fatal(err)
	}
	return s, l.ID
}

func checkErr(t *testing.T, err, want error) {
	t.Helper()
	if want == nil && err != nil || want != nil && !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

func TestPermissions(t *testing.T) {
	// nolist runs the operation as the owner on a list that doesn't exist.
	actors := []string{"ann", "ed", "vic", "out", "nolist"}
	tests := []struct {
		name string
		do   func(s *Lists, id, by string) error
		want map[string]error
	}{
		{
			name: "get",
			do: func(s *Lists, id, by string) error {
				_, err := s.Get(id, by)
				return err
			},
			want: map[string]error{"out": ErrNotFound, "nolist": ErrNotFound},
		},
		{
			name: "add viewer",
			do:   func(s *Lists, id, by string) error { return s.AddMember(id, by, "zed", Viewer) },
			want: map[string]error{"vic": ErrForbidden, "out": ErrNotFound, "nolist": ErrNotFound},
		},
		{
			name: "add editor",
			do:   func(s *Lists, id, by string) error { return s.AddMember(id, by, "zed", Editor) },
			want: map[string]error{"ed": ErrForbidden, "vic": ErrForbidden, "out": ErrNotFound, "nolist": ErrNotFound},
		},
		{
			name: "add owner",
			do:   func(s *Lists, id, by string) error { return s.AddMember(id, by, "zed", Owner) },
			want: map[string]error{"ann": ErrInvalid, "ed": ErrInvalid, "vic": ErrInvalid, "out": ErrInvalid, "nolist": ErrInvalid},
		},
		{
			name: "promote viewer",
			do:   func(s *Lists, id, by string) error { return s.AddMember(id, by, "vic", Editor) },
			want: map[string]error{"ed": ErrForbidden, "vic": ErrForbidden, "out": ErrNotFound, "nolist": ErrNotFound},
		},
		{
			name: "demote editor",
			do:   func(s *Lists, id, by string) error { return s.AddMember(id, by, "ed", Viewer) },
			want: map[string]error{"ed": ErrForbidden, "vic": ErrForbidden, "out": ErrNotFound, "nolist": ErrNotFound},
		},
		{
			name: "change owner's role",
			do:   func(s *Lists, id, by string) error { return s.AddMember(id, by, "ann", Editor) },
			want: map[string]error{"ann": ErrInvalid, "ed": ErrForbidden, "vic": ErrForbidden, "out": ErrNotFound, "nolist": ErrNotFound},
		},
		{
			name: "remove viewer",
			do:   func(s *Lists, id, by string) error { return s.RemoveMember(id, by, "vic") },
			want: map[string]error{"ed": ErrForbidden, "out": ErrNotFound, "nolist": ErrNotFound},
		},
		{
			name: "remove editor",
			do:   func(s *Lists, id, by string) error { return s.RemoveMember(id, by, "ed") },
			want: map[string]error{"vic": ErrForbidden, "out": ErrNotFound, "nolist": ErrNotFound},
		},
		{
			name: "remove owner",
			do:   func(s *Lists, id, by string) error { return s.RemoveMember(id, by, "ann") },
			want: map[string]error{"ann": ErrForbidden, "ed": ErrForbidden, "vic": ErrForbidden, "out": ErrNotFound, "nolist": ErrNotFound},
		},
		{
			name: "add reminder",
			do:   func(s *Lists, id, by string) error { return s.AddReminder(id, by, "rem_0002") },
			want: map[string]error{"vic": ErrForbidden, "out": ErrNotFound, "nolist": ErrNotFound},
		},
		{
			name: "remove reminder",
			do:   func(s *Lists, id, by string) error { return s.RemoveReminder(id, by, "rem_0001") },
			want: map[string]error{"vic": ErrForbidden, "out": ErrNotFound, "nolist": ErrNotFound},
		},
		{
			name: "rename",
			do:   func(s *Lists, id, by string) error { return s.Rename(id, by, "Errands") },
			want: map[string]error{"ed": ErrForbidden, "vic": ErrForbidden, "out": ErrNotFound, "nolist": ErrNotFound},
		},
		{
			name: "delete",
			do:   func(s *Lists, id, by string) error { return s.Delete(id, by) },
			want: map[string]error{"ed": ErrForbidden, "vic": ErrForbidden, "out": ErrNotFound, "nolist": ErrNotFound},
		},
		{
			name: "transfer",
			do:   func(s *Lists, id, by string) error { return s.TransferOwnership(id, by, "ed") },
			want: map[string]error{"ed": ErrForbidden, "vic": ErrForbidden, "out": ErrNotFound, "nolist": ErrNotFound},
		},
	}
	for _, tt := range tests {
		for _, actor := range actors {
			t.Run(tt.name+"/"+actor, func(t *testing.T) {
				s, id := newLists(t)
				by := actor
				if actor == "nolist" {
					id, by = "lst_9999", "ann"
				}
				checkErr(t, tt.do(s, id, by), tt.want[actor])
			})
		}
	}
}

func TestCreate(t *testing.T) {
	s := New(func() time.Time { return start })
	tests := []struct {
		owner, name string
		wantName    string
		wantErr     error
	}{
		{"ann", "Groceries", "Groceries", nil},
		{"ann", "  Trip  ", "Trip", nil},
		{"ann", "x", "x", nil},
		{"ann", strings.Repeat("a", 80), strings.Repeat("a", 80), nil},
		{"ann", strings.Repeat("é", 80), strings.Repeat("é", 80), nil},
		{"ann", strings.Repeat("a", 81), "", ErrInvalid},
		{"ann", "", "", ErrInvalid},
		{"ann", "   ", "", ErrInvalid},
		{"", "Groceries", "", ErrInvalid},
	}
	for _, tt := range tests {
		l, err := s.Create(tt.owner, tt.name)
		checkErr(t, err, tt.wantErr)
		if err == nil && l.Name != tt.wantName {
			t.Errorf("Create(%q, %q).Name = %q, want %q", tt.owner, tt.name, l.Name, tt.wantName)
		}
	}
}

func TestCreateFields(t *testing.T) {
	s := New(func() time.Time { return start })
	a, err := s.Create("ann", "One")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create("bob", "Two")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "lst_0001" || b.ID != "lst_0002" {
		t.Errorf("IDs = %s, %s, want lst_0001, lst_0002", a.ID, b.ID)
	}
	if a.Owner != "ann" || a.Members["ann"] != Owner || len(a.Members) != 1 {
		t.Errorf("owner = %s, members = %v", a.Owner, a.Members)
	}
	if !a.Created.Equal(start) {
		t.Errorf("Created = %v, want %v", a.Created, start)
	}
}

func TestRenameValidates(t *testing.T) {
	s, id := newLists(t)
	checkErr(t, s.Rename(id, "ann", ""), ErrInvalid)
	checkErr(t, s.Rename(id, "ann", strings.Repeat("a", 81)), ErrInvalid)
	checkErr(t, s.Rename(id, "ann", " Errands "), nil)
	l, err := s.Get(id, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if l.Name != "Errands" {
		t.Errorf("Name = %q, want Errands", l.Name)
	}
}

func TestCopies(t *testing.T) {
	s, id := newLists(t)
	l, err := s.Get(id, "ann")
	if err != nil {
		t.Fatal(err)
	}
	l.Members["out"] = Owner
	l.Reminders[0] = "rem_9999"
	l.Name = "Changed"

	got, err := s.Get(id, "ann")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Members["out"]; ok {
		t.Error("changing a returned Members map changed the list")
	}
	if got.Reminders[0] != "rem_0001" || got.Name != "Groceries" {
		t.Errorf("got %v %q, want [rem_0001] Groceries", got.Reminders, got.Name)
	}

	lists := s.ForUser("ann")
	lists[0].Members["out"] = Viewer
	if _, err := s.Get(id, "out"); !errors.Is(err, ErrNotFound) {
		t.Errorf("changing a list from ForUser changed the list: err = %v", err)
	}
}

func TestMembers(t *testing.T) {
	s, id := newLists(t)
	if err := s.AddMember(id, "ed", "zed", Viewer); err != nil {
		t.Fatal(err)
	}
	if err := s.AddMember(id, "ann", "vic", Editor); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMember(id, "ed", "ed"); err != nil {
		t.Fatal(err)
	}
	l, err := s.Get(id, "ann")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Role{"ann": Owner, "vic": Editor, "zed": Viewer}
	if len(l.Members) != len(want) {
		t.Fatalf("members = %v, want %v", l.Members, want)
	}
	for u, r := range want {
		if l.Members[u] != r {
			t.Errorf("members[%s] = %v, want %v", u, l.Members[u], r)
		}
	}
	checkErr(t, s.RemoveMember(id, "ann", "ed"), ErrNotFound)
	checkErr(t, s.AddMember(id, "ann", "", Viewer), ErrInvalid)
	checkErr(t, s.AddMember(id, "ann", "zed", Role(7)), ErrInvalid)
	checkErr(t, s.AddMember(id, "vic", "zed", Viewer), ErrForbidden)
}

func TestReminders(t *testing.T) {
	s, id := newLists(t)
	for _, r := range []string{"rem_0002", "rem_0001", "rem_0003"} {
		if err := s.AddReminder(id, "ed", r); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RemoveReminder(id, "ed", "rem_0002"); err != nil {
		t.Fatal(err)
	}
	checkErr(t, s.RemoveReminder(id, "ed", "rem_0002"), ErrNotFound)
	checkErr(t, s.AddReminder(id, "ed", ""), ErrInvalid)
	l, err := s.Get(id, "vic")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"rem_0001", "rem_0003"}; !slices.Equal(l.Reminders, want) {
		t.Errorf("reminders = %v, want %v", l.Reminders, want)
	}
}

func TestTransferOwnership(t *testing.T) {
	s, id := newLists(t)
	checkErr(t, s.TransferOwnership(id, "ann", "zed"), ErrInvalid)
	checkErr(t, s.TransferOwnership(id, "ann", "ann"), ErrInvalid)
	if err := s.TransferOwnership(id, "ann", "vic"); err != nil {
		t.Fatal(err)
	}
	l, err := s.Get(id, "vic")
	if err != nil {
		t.Fatal(err)
	}
	if l.Owner != "vic" || l.Members["vic"] != Owner || l.Members["ann"] != Editor {
		t.Errorf("owner = %s, members = %v", l.Owner, l.Members)
	}
	checkErr(t, s.Rename(id, "ann", "Mine"), ErrForbidden)
	checkErr(t, s.RemoveMember(id, "vic", "vic"), ErrForbidden)
	checkErr(t, s.RemoveMember(id, "vic", "ann"), nil)
	checkErr(t, s.Delete(id, "vic"), nil)
	if _, err := s.Get(id, "vic"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete: err = %v, want ErrNotFound", err)
	}
}

func TestForUser(t *testing.T) {
	s := New(func() time.Time { return start })
	for _, c := range []struct{ owner, name string }{
		{"ann", "Work"}, {"bob", "Garden"}, {"ann", "Books"}, {"bob", "Away"},
	} {
		if _, err := s.Create(c.owner, c.name); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddMember("lst_0002", "bob", "ann", Viewer); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, l := range s.ForUser("ann") {
		names = append(names, l.Name)
	}
	if want := []string{"Books", "Garden", "Work"}; !slices.Equal(names, want) {
		t.Errorf("ForUser(ann) = %v, want %v", names, want)
	}
	if got := s.ForUser("nobody"); len(got) != 0 {
		t.Errorf("ForUser(nobody) = %v, want none", got)
	}
}

func TestRoles(t *testing.T) {
	for _, r := range []Role{Viewer, Editor, Owner} {
		got, err := ParseRole(r.String())
		if err != nil || got != r {
			t.Errorf("ParseRole(%q) = %v, %v", r.String(), got, err)
		}
	}
	if _, err := ParseRole("admin"); !errors.Is(err, ErrInvalid) {
		t.Errorf("ParseRole(admin): err = %v, want ErrInvalid", err)
	}
	if got := Role(0).String(); got != "Role(0)" {
		t.Errorf("Role(0).String() = %q", got)
	}
}
