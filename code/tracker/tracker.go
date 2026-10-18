// Package tracker is a small issue tracker that stands in for GitHub or
// Jira in the book: issues with labels and comments, kept in a JSON file
// and served over HTTP.
package tracker

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Errors callers check for.
var (
	ErrNotFound = errors.New("tracker: issue not found")
	ErrInvalid  = errors.New("tracker: invalid issue")

	// ErrKeyReused means an idempotency key came back with a different
	// request from the one it was first used for: a bug in the caller,
	// since a retry repeats its request exactly (chapter 15).
	ErrKeyReused = errors.New("tracker: idempotency key reused " +
		"for a different request")
)

// An Issue is one problem report.
type Issue struct {
	ID          int       `json:"id"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	Labels      []string  `json:"labels,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"` // the crash group it describes
	Status      string    `json:"status"`                // "open" or "closed"
	Created     time.Time `json:"created"`
	Comments    []Comment `json:"comments,omitempty"`

	// Key is the idempotency key the issue was filed with, if any: a
	// second create with the same key returns this issue (chapter 15).
	Key string `json:"key,omitempty"`
}

// A Comment is a note added to an issue.
type Comment struct {
	Body    string    `json:"body"`
	Created time.Time `json:"created"`
	Key     string    `json:"key,omitempty"` // as for Issue.Key
}

// A Query selects issues. Empty fields match everything.
type Query struct {
	Text        string // in the title or body, ignoring case
	Fingerprint string
	Status      string
	Key         string // the idempotency key it was filed with (chapter 15)
}

// Store holds the issues.
type Store struct {
	path string // "" keeps issues in memory only
	now  func() time.Time

	mu     sync.Mutex
	issues []*Issue
}

// Open loads the issues saved at path, if the file exists, and saves
// every change back to it. An empty path keeps issues in memory.
func Open(path string, now func() time.Time) (*Store, error) {
	s := &Store{path: path, now: now}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// load reads the issues saved at the store's path, if there are any.
func (s *Store) load() error {
	if s.path == "" {
		return nil
	}
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open tracker: %w", err)
	}
	var issues []*Issue
	if err := json.Unmarshal(b, &issues); err != nil {
		return fmt.Errorf("open tracker %s: %w", s.path, err)
	}
	s.issues = issues
	return nil
}

// fresh takes the store's file lock and reloads the file, with s.mu
// held, so that two stores on one file, such as a tracker server
// restarted while its old process finishes a request, never lose each
// other's changes (chapter 15). The caller calls release when done.
func (s *Store) fresh() (release func(), err error) {
	if s.path == "" {
		return func() {}, nil
	}
	unlock, err := lockFile(s.path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("lock tracker: %w", err)
	}
	if err := s.load(); err != nil {
		unlock()
		return nil, err
	}
	return unlock, nil
}

// Create files a new issue.
func (s *Store) Create(title, body string, labels []string, fingerprint string) (Issue, error) {
	is, _, err := s.CreateOnce("", title, body, labels, fingerprint)
	return is, err
}

// Get returns one issue.
func (s *Store) Get(id int) (Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.fresh()
	if err != nil {
		return Issue{}, err
	}
	defer release()
	is, err := s.find(id)
	if err != nil {
		return Issue{}, err
	}
	return is.clone(), nil
}

// Comment adds a comment to an issue.
func (s *Store) Comment(id int, body string) (Issue, error) {
	is, _, err := s.CommentOnce(id, "", body)
	return is, err
}

// SetStatus opens or closes an issue.
func (s *Store) SetStatus(id int, status string) (Issue, error) {
	if status != "open" && status != "closed" {
		return Issue{}, fmt.Errorf("%w: status must be open or closed", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.fresh()
	if err != nil {
		return Issue{}, err
	}
	defer release()
	is, err := s.find(id)
	if err != nil {
		return Issue{}, err
	}
	old := is.Status
	is.Status = status
	if err := s.save(); err != nil {
		is.Status = old
		return Issue{}, err
	}
	return is.clone(), nil
}

// MaxKey is the longest idempotency key the tracker accepts.
const MaxKey = 255

// tag::create-once[]

// CreateOnce files a new issue, unless one was already filed with the
// same idempotency key: then it returns that issue and false, filing
// nothing. A key that comes back with a different title, body, labels,
// or fingerprint is ErrKeyReused. An empty key files a new issue every
// time, as Create does (chapter 15).
func (s *Store) CreateOnce(key, title, body string, labels []string,
	fingerprint string) (Issue, bool, error) {
	if strings.TrimSpace(title) == "" {
		return Issue{}, false, fmt.Errorf("%w: a title is required",
			ErrInvalid)
	}
	if err := checkKey(key); err != nil {
		return Issue{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock() // <1>
	release, err := s.fresh()
	if err != nil {
		return Issue{}, false, err
	}
	defer release()
	if is := s.byKey(key); is != nil {
		if is.Title != title || is.Body != body ||
			is.Fingerprint != fingerprint ||
			!slices.Equal(is.Labels, labels) {
			return Issue{}, false, ErrKeyReused
		}
		return is.clone(), false, nil
	}
	// end::create-once[]
	id := 1
	for _, is := range s.issues {
		id = max(id, is.ID+1)
	}
	is := &Issue{
		ID:          id,
		Title:       title,
		Body:        body,
		Labels:      slices.Clone(labels),
		Fingerprint: fingerprint,
		Status:      "open",
		Created:     s.now(),
		Key:         key,
	}
	s.issues = append(s.issues, is)
	if err := s.save(); err != nil {
		s.issues = s.issues[:len(s.issues)-1]
		return Issue{}, false, err
	}
	return is.clone(), true, nil
}

// ByKey returns the issue filed with an idempotency key, or
// ErrNotFound if none was (chapter 15).
func (s *Store) ByKey(key string) (Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.fresh()
	if err != nil {
		return Issue{}, err
	}
	defer release()
	if is := s.byKey(key); is != nil {
		return is.clone(), nil
	}
	return Issue{}, ErrNotFound
}

// CommentOnce adds a comment to an issue, unless a comment with the
// same idempotency key is already on it: then it returns the issue and
// false. As with CreateOnce, a key reused for a different body is
// ErrKeyReused, and an empty key adds a comment every time (chapter
// 15).
func (s *Store) CommentOnce(id int, key, body string) (Issue, bool,
	error) {
	if strings.TrimSpace(body) == "" {
		return Issue{}, false, fmt.Errorf("%w: a comment needs a body", ErrInvalid)
	}
	if err := checkKey(key); err != nil {
		return Issue{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.fresh()
	if err != nil {
		return Issue{}, false, err
	}
	defer release()
	is, err := s.find(id)
	if err != nil {
		return Issue{}, false, err
	}
	for _, c := range is.Comments {
		if key != "" && c.Key == key {
			if c.Body != body {
				return Issue{}, false, ErrKeyReused
			}
			return is.clone(), false, nil
		}
	}
	is.Comments = append(is.Comments,
		Comment{Body: body, Created: s.now(), Key: key})
	if err := s.save(); err != nil {
		is.Comments = is.Comments[:len(is.Comments)-1]
		return Issue{}, false, err
	}
	return is.clone(), true, nil
}

func checkKey(key string) error {
	if len(key) > MaxKey {
		return fmt.Errorf("%w: the idempotency key is longer than %d "+
			"bytes", ErrInvalid, MaxKey)
	}
	return nil
}

// byKey returns the issue filed with key, or nil. The empty key
// matches nothing.
func (s *Store) byKey(key string) *Issue {
	if key == "" {
		return nil
	}
	for _, is := range s.issues {
		if is.Key == key {
			return is
		}
	}
	return nil
}

// Search returns the issues matching q, oldest first.
func (s *Store) Search(q Query) []Issue {
	s.mu.Lock()
	defer s.mu.Unlock()
	// On a failure to reload, the search covers what this store last
	// read.
	if release, err := s.fresh(); err == nil {
		defer release()
	}
	text := strings.ToLower(q.Text)
	var out []Issue
	for _, is := range s.issues {
		switch {
		case q.Fingerprint != "" && is.Fingerprint != q.Fingerprint,
			q.Status != "" && is.Status != q.Status,
			q.Key != "" && is.Key != q.Key,
			text != "" && !strings.Contains(strings.ToLower(is.Title+"\n"+is.Body), text):
			continue
		}
		out = append(out, is.clone())
	}
	return out
}

func (s *Store) find(id int) (*Issue, error) {
	for _, is := range s.issues {
		if is.ID == id {
			return is, nil
		}
	}
	return nil, ErrNotFound
}

// save writes every issue to the store's file: to a temporary file
// first, then renamed over the old one, so a crash never leaves half a
// file.
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	b, err := json.Marshal(s.issues)
	if err != nil {
		return fmt.Errorf("save tracker: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".tracker-*")
	if err != nil {
		return fmt.Errorf("save tracker: %w", err)
	}
	defer os.Remove(tmp.Name()) // fails harmlessly after the rename
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("save tracker: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("save tracker: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path); err != nil {
		return fmt.Errorf("save tracker: %w", err)
	}
	return nil
}

func (is *Issue) clone() Issue {
	c := *is
	c.Labels = slices.Clone(is.Labels)
	c.Comments = slices.Clone(is.Comments)
	return c
}
