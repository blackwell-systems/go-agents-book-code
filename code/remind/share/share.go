// Package share lets users share lists of reminders with each other.
package share

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Errors callers check for.
var (
	ErrNotFound  = errors.New("share: not found")
	ErrForbidden = errors.New("share: forbidden")
	ErrInvalid   = errors.New("share: invalid")
)

// maxName is the longest list name, in characters.
const maxName = 80

// A Role is what a member may do with a list. Each role may do
// everything the roles below it may.
type Role int

// The roles, from least to most access.
const (
	Viewer Role = iota + 1 // sees the list and its reminders
	Editor                 // also adds reminders and viewers
	Owner                  // also manages members, renames, and deletes
)

// String returns the role's name: "viewer", "editor", or "owner".
func (r Role) String() string {
	switch r {
	case Viewer:
		return "viewer"
	case Editor:
		return "editor"
	case Owner:
		return "owner"
	}
	return fmt.Sprintf("Role(%d)", int(r))
}

// ParseRole returns the role with the given name.
func ParseRole(s string) (Role, error) {
	switch s {
	case "viewer":
		return Viewer, nil
	case "editor":
		return Editor, nil
	case "owner":
		return Owner, nil
	}
	return 0, fmt.Errorf("parse role %q: %w", s, ErrInvalid)
}

// A List is a named set of reminders shared between its members.
type List struct {
	ID        string
	Name      string
	Owner     string          // user ID of the owner, who is also in Members
	Members   map[string]Role // by user ID
	Reminders []string        // reminder IDs, in the order they were added
	Created   time.Time
}

func (l *List) clone() List {
	c := *l
	c.Members = maps.Clone(l.Members)
	c.Reminders = slices.Clone(l.Reminders)
	return c
}

// Lists holds every shared list. Users who are not members of a list
// get ErrNotFound for it; members whose role does not allow an action
// get ErrForbidden. Lists is safe for concurrent use.
type Lists struct {
	now func() time.Time

	mu    sync.RWMutex
	lists map[string]*List
	seq   int
}

// New returns an empty set of lists. now is its clock.
func New(now func() time.Time) *Lists {
	return &Lists{now: now, lists: make(map[string]*List)}
}

// checkName trims space around a list name and checks its length.
func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxName {
		return "", fmt.Errorf("name must be 1 to %d characters: %w", maxName, ErrInvalid)
	}
	return name, nil
}

// member returns the list with the given ID and user's role in it.
func (s *Lists) member(id, user string) (*List, Role, error) {
	l, ok := s.lists[id]
	if !ok {
		return nil, 0, ErrNotFound
	}
	role, ok := l.Members[user]
	if !ok {
		return nil, 0, ErrNotFound
	}
	return l, role, nil
}

// require returns the list with the given ID if by's role in it is at
// least need.
func (s *Lists) require(id, by string, need Role) (*List, error) {
	l, role, err := s.member(id, by)
	if err != nil {
		return nil, err
	}
	if role < need {
		return nil, ErrForbidden
	}
	return l, nil
}

// Create makes a new list owned by owner.
func (s *Lists) Create(owner, name string) (List, error) {
	if owner == "" {
		return List{}, fmt.Errorf("create list: no owner: %w", ErrInvalid)
	}
	name, err := checkName(name)
	if err != nil {
		return List{}, fmt.Errorf("create list: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	l := &List{
		ID:      fmt.Sprintf("lst_%04d", s.seq),
		Name:    name,
		Owner:   owner,
		Members: map[string]Role{owner: Owner},
		Created: s.now(),
	}
	s.lists[l.ID] = l
	return l.clone(), nil
}

// Get returns a list for one of its members.
func (s *Lists) Get(id, user string) (List, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, _, err := s.member(id, user)
	if err != nil {
		return List{}, fmt.Errorf("get list %s: %w", id, err)
	}
	return l.clone(), nil
}

// ForUser returns the lists user is a member of, sorted by name.
func (s *Lists) ForUser(user string) []List {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []List
	for _, l := range s.lists {
		if _, ok := l.Members[user]; ok {
			out = append(out, l.clone())
		}
	}
	slices.SortFunc(out, func(a, b List) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})
	return out
}

// AddMember adds user to a list with the given role, or changes the role
// of an existing member. Owners may add anyone; editors may add new
// viewers. A list has one owner: use TransferOwnership to change it.
func (s *Lists) AddMember(id, by, user string, role Role) error {
	switch {
	case user == "":
		return fmt.Errorf("add member to %s: no user: %w", id, ErrInvalid)
	case role == Owner:
		return fmt.Errorf("add member to %s: a list has one owner: %w", id, ErrInvalid)
	case role != Viewer && role != Editor:
		return fmt.Errorf("add member to %s: %v: %w", id, role, ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, byRole, err := s.member(id, by)
	if err != nil {
		return fmt.Errorf("add member to %s: %w", id, err)
	}
	_, exists := l.Members[user]
	switch {
	case byRole == Owner:
	case byRole == Editor && role == Viewer && !exists:
	default:
		return fmt.Errorf("add %s to %s as %v: %w", user, id, role, ErrForbidden)
	}
	if user == l.Owner {
		return fmt.Errorf("add %s to %s: already the owner: %w", user, id, ErrInvalid)
	}
	l.Members[user] = role
	return nil
}

// RemoveMember removes user from a list. Owners may remove anyone but
// themselves, and other members may remove themselves.
func (s *Lists) RemoveMember(id, by, user string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, byRole, err := s.member(id, by)
	if err != nil {
		return fmt.Errorf("remove member from %s: %w", id, err)
	}
	switch {
	case user == l.Owner:
		return fmt.Errorf("remove owner from %s: %w", id, ErrForbidden)
	case user != by && byRole != Owner:
		return fmt.Errorf("remove %s from %s: %w", user, id, ErrForbidden)
	}
	if _, ok := l.Members[user]; !ok {
		return fmt.Errorf("remove %s from %s: %w", user, id, ErrNotFound)
	}
	delete(l.Members, user)
	return nil
}

// AddReminder adds a reminder to a list. Adding a reminder already on
// the list does nothing. Editors and owners may add reminders.
func (s *Lists) AddReminder(id, by, reminderID string) error {
	if reminderID == "" {
		return fmt.Errorf("add reminder to %s: no reminder: %w", id, ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.require(id, by, Editor)
	if err != nil {
		return fmt.Errorf("add reminder to %s: %w", id, err)
	}
	if !slices.Contains(l.Reminders, reminderID) {
		l.Reminders = append(l.Reminders, reminderID)
	}
	return nil
}

// RemoveReminder takes a reminder off a list. Editors and owners may
// remove reminders.
func (s *Lists) RemoveReminder(id, by, reminderID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.require(id, by, Editor)
	if err != nil {
		return fmt.Errorf("remove reminder from %s: %w", id, err)
	}
	i := slices.Index(l.Reminders, reminderID)
	if i < 0 {
		return fmt.Errorf("remove reminder %s from %s: %w", reminderID, id, ErrNotFound)
	}
	l.Reminders = slices.Delete(l.Reminders, i, i+1)
	return nil
}

// Rename changes a list's name. Only the owner may rename a list.
func (s *Lists) Rename(id, by, name string) error {
	name, err := checkName(name)
	if err != nil {
		return fmt.Errorf("rename %s: %w", id, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.require(id, by, Owner)
	if err != nil {
		return fmt.Errorf("rename %s: %w", id, err)
	}
	l.Name = name
	return nil
}

// Delete removes a list. Only the owner may delete a list.
func (s *Lists) Delete(id, by string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.require(id, by, Owner); err != nil {
		return fmt.Errorf("delete list %s: %w", id, err)
	}
	delete(s.lists, id)
	return nil
}

// RemoveUser deletes the lists user owns and takes user off every other
// list, for when an account is closed. It returns how many lists were
// deleted.
func (s *Lists) RemoveUser(user string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, l := range s.lists {
		if l.Owner == user {
			delete(s.lists, id)
			n++
			continue
		}
		delete(l.Members, user)
	}
	return n
}

// TransferOwnership makes a member the owner of a list, and the old
// owner an editor. Only the owner may transfer a list.
func (s *Lists) TransferOwnership(id, by, to string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := s.require(id, by, Owner)
	if err != nil {
		return fmt.Errorf("transfer %s: %w", id, err)
	}
	if to == by {
		return fmt.Errorf("transfer %s to %s: already the owner: %w", id, to, ErrInvalid)
	}
	if _, ok := l.Members[to]; !ok {
		return fmt.Errorf("transfer %s to %s: not a member: %w", id, to, ErrInvalid)
	}
	l.Members[by] = Editor
	l.Members[to] = Owner
	l.Owner = to
	return nil
}
