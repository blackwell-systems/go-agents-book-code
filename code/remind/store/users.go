package store

import (
	"cmp"
	"fmt"
	"slices"
)

// A User owns reminders and authenticates with an API token.
type User struct {
	ID    string
	Name  string
	Email string
	Token string `json:"-"`
	Admin bool
	Plan  string // "free" or "pro"; empty means free
	Prefs *Prefs
}

// Prefs are a user's notification settings.
type Prefs struct {
	TZ      string // IANA zone name; empty means UTC
	Channel string // "email" or "webhook"
	Webhook string // URL for the webhook channel
	Lang    string // language for notifications, such as "pt"; empty means English

	// Quiet hours, as "22:00" and "07:00": reminders due in between wait
	// until the end.
	QuietFrom string
	QuietTo   string
}

// DefaultPrefs returns the settings a new user starts with.
func DefaultPrefs() *Prefs {
	return &Prefs{Channel: "email"}
}

// AddUser adds a user, giving it default preferences if it has none.
func (s *Store) AddUser(u User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.users[u.ID]; ok {
		return fmt.Errorf("add user %s: %w", u.ID, ErrConflict)
	}
	if u.Prefs == nil {
		u.Prefs = DefaultPrefs()
	}
	s.users[u.ID] = &u
	if u.Token != "" {
		s.tokens[u.Token] = &u
	}
	return nil
}

// ImportUsers adds users in bulk, as exported from another system.
// Existing IDs are skipped. It returns how many users were added.
func (s *Store) ImportUsers(users []User) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, u := range users {
		if _, ok := s.users[u.ID]; ok {
			continue
		}
		s.users[u.ID] = &u
		if u.Token != "" {
			s.tokens[u.Token] = &u
		}
		n++
	}
	return n
}

// SetPrefs replaces a user's preferences.
func (s *Store) SetPrefs(id string, p Prefs) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return ErrNotFound
	}
	u.Prefs = &p
	return nil
}

// User returns a user by ID.
func (s *Store) User(id string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.users[id]
	if !ok {
		return nil, ErrNotFound
	}
	return u, nil
}

// UserByToken returns the user with the given API token.
func (s *Store) UserByToken(token string) (*User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.tokens[token]
	if !ok {
		return nil, ErrNotFound
	}
	return u, nil
}

// DeleteUser removes a user and all of their reminders.
func (s *Store) DeleteUser(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return ErrNotFound
	}
	delete(s.users, id)
	delete(s.tokens, u.Token)
	for _, r := range s.byUser[id] {
		r.Deleted = true
		s.changed(r)
	}
	return nil
}

// Users returns all users, sorted by ID.
func (s *Store) Users() []*User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*User, 0, len(s.users))
	for _, u := range s.users {
		out = append(out, u)
	}
	slices.SortFunc(out, func(a, b *User) int { return cmp.Compare(a.ID, b.ID) })
	return out
}
