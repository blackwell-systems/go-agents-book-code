package tracker

import (
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

func clock() time.Time { return t0 }

func TestStore(t *testing.T) {
	s, err := Open("", clock)
	if err != nil {
		t.Fatal(err)
	}
	is, err := s.Create("Panic in duration.Parse", "Empty input crashes.", []string{"crash"}, "fp1")
	if err != nil {
		t.Fatal(err)
	}
	if is.ID != 1 || is.Status != "open" {
		t.Errorf("Create = %+v", is)
	}
	if _, err := s.Create(" ", "", nil, ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("Create without a title: %v, want ErrInvalid", err)
	}
	if _, err := s.Comment(1, "Seen again."); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(1, "closed"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Comments) != 1 || got.Status != "closed" {
		t.Errorf("Get = %+v", got)
	}
	if _, err := s.Get(2); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(2): %v, want ErrNotFound", err)
	}
	got.Labels[0] = "changed"
	if again, _ := s.Get(1); again.Labels[0] != "crash" {
		t.Error("Get returned the stored issue, not a copy")
	}
}

func TestSearch(t *testing.T) {
	s, err := Open("", clock)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ title, fp string }{
		{"Panic in duration.Parse", "fp1"},
		{"Deadlock in notify", "fp2"},
		{"Another duration panic", "fp1"},
	} {
		if _, err := s.Create(c.title, "", nil, c.fp); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		q    Query
		want int
	}{
		{Query{}, 3},
		{Query{Fingerprint: "fp1"}, 2},
		{Query{Text: "DURATION"}, 2},
		{Query{Text: "duration", Fingerprint: "fp2"}, 0},
		{Query{Status: "closed"}, 0},
	}
	for _, tt := range tests {
		if got := s.Search(tt.q); len(got) != tt.want {
			t.Errorf("Search(%+v) = %d issues, want %d", tt.q, len(got), tt.want)
		}
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.json")
	s, err := Open(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("Saved", "body", nil, ""); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s2.Get(1); err != nil || got.Title != "Saved" {
		t.Errorf("reopened Get(1) = %+v, %v", got, err)
	}
}

func TestHandler(t *testing.T) {
	s, err := Open("", clock)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s)
	do := func(method, path, body string) (int, string) {
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, r))
		return rec.Code, rec.Body.String()
	}
	tests := []struct {
		method, path, body string
		status             int
	}{
		{http.MethodPost, "/issues", `{"title":"Panic","body":"stack","fingerprint":"fp1"}`, http.StatusCreated},
		{http.MethodPost, "/issues", `{"body":"no title"}`, http.StatusBadRequest},
		{http.MethodPost, "/issues", `not json`, http.StatusBadRequest},
		{http.MethodGet, "/issues/1", "", http.StatusOK},
		{http.MethodGet, "/issues/9", "", http.StatusNotFound},
		{http.MethodGet, "/issues/x", "", http.StatusNotFound},
		{http.MethodPost, "/issues/1/comments", `{"body":"again"}`, http.StatusCreated},
		{http.MethodPatch, "/issues/1", `{"status":"closed"}`, http.StatusOK},
		{http.MethodPatch, "/issues/1", `{"status":"gone"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		if code, body := do(tt.method, tt.path, tt.body); code != tt.status {
			t.Errorf("%s %s: status %d, want %d: %s", tt.method, tt.path, code, tt.status, body)
		}
	}
	_, body := do(http.MethodGet, "/issues?fingerprint=fp1&status=closed", "")
	var got struct {
		Issues []Issue `json:"issues"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Issues) != 1 || len(got.Issues[0].Comments) != 1 {
		t.Errorf("search = %s", body)
	}
}

func TestCreateOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.json")
	s, err := Open(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	first, created, err := s.CreateOnce("k1", "Panic", "stack", []string{"crash"}, "fp1")
	if err != nil || !created || first.Key != "k1" {
		t.Fatalf("CreateOnce = %+v, %v, %v", first, created, err)
	}
	// A reopened store still knows the key, as after a crash.
	s, err = Open(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		key     string
		title   string
		created bool
		err     error
	}{
		{"repeat", "k1", "Panic", false, nil},
		{"reused", "k1", "Another", false, ErrKeyReused},
		{"new key", "k2", "Panic", true, nil},
		{"no key", "", "Panic", true, nil},
		{"too long", strings.Repeat("k", MaxKey+1), "Panic", false, ErrInvalid},
	}
	for _, tt := range tests {
		is, created, err := s.CreateOnce(tt.key, tt.title, "stack", []string{"crash"}, "fp1")
		if !errors.Is(err, tt.err) || created != tt.created {
			t.Errorf("%s: CreateOnce = %v, %v; want %v, %v", tt.name, created, err, tt.created, tt.err)
		}
		if tt.name == "repeat" && is.ID != first.ID {
			t.Errorf("repeat returned #%d, want #%d", is.ID, first.ID)
		}
	}
	if n := len(s.Search(Query{})); n != 3 {
		t.Errorf("%d issues, want 3", n)
	}
	if is, err := s.ByKey("k1"); err != nil || is.ID != first.ID {
		t.Errorf("ByKey(k1) = %+v, %v", is, err)
	}
	for _, key := range []string{"", "missing"} {
		if _, err := s.ByKey(key); !errors.Is(err, ErrNotFound) {
			t.Errorf("ByKey(%q): %v, want ErrNotFound", key, err)
		}
	}
	if got := s.Search(Query{Key: "k2"}); len(got) != 1 || got[0].Key != "k2" {
		t.Errorf("Search(Key: k2) = %+v", got)
	}
}

func TestCommentOnce(t *testing.T) {
	s, err := Open("", clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("Panic", "", nil, ""); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		id      int
		key     string
		body    string
		created bool
		err     error
	}{
		{1, "c1", "seen again", true, nil},
		{1, "c1", "seen again", false, nil},
		{1, "c1", "different", false, ErrKeyReused},
		{1, "", "seen again", true, nil},
		{2, "c2", "seen again", false, ErrNotFound},
	}
	for _, tt := range tests {
		_, created, err := s.CommentOnce(tt.id, tt.key, tt.body)
		if !errors.Is(err, tt.err) || created != tt.created {
			t.Errorf("CommentOnce(%d, %q, %q) = %v, %v; want %v, %v",
				tt.id, tt.key, tt.body, created, err, tt.created, tt.err)
		}
	}
	if is, _ := s.Get(1); len(is.Comments) != 2 {
		t.Errorf("%d comments, want 2", len(is.Comments))
	}
}

func TestHandlerKeys(t *testing.T) {
	s, err := Open("", clock)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s)
	do := func(path, key, body string) int {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
		if key != "" {
			req.Header.Set(KeyHeader, `"`+key+`"`)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	tests := []struct {
		path, key, body string
		status          int
	}{
		{"/issues", "k1", `{"title":"Panic"}`, http.StatusCreated},
		{"/issues", "k1", `{"title":"Panic"}`, http.StatusOK},
		{"/issues", "k1", `{"title":"Other"}`, http.StatusUnprocessableEntity},
		{"/issues/1/comments", "c1", `{"body":"again"}`, http.StatusCreated},
		{"/issues/1/comments", "c1", `{"body":"again"}`, http.StatusOK},
	}
	for _, tt := range tests {
		if code := do(tt.path, tt.key, tt.body); code != tt.status {
			t.Errorf("POST %s %s: status %d, want %d", tt.path, tt.body, code, tt.status)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/issues?key=k1", nil))
	var got struct {
		Issues []Issue `json:"issues"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Issues) != 1 || len(got.Issues[0].Comments) != 1 {
		t.Errorf("GET /issues?key=k1 = %s", rec.Body)
	}
}

// TestKeyHeader checks that the Idempotency-Key header is read as the
// draft's quoted string: "abc" and abc are not two keys, a bare token
// is refused, and the quotes aren't part of the stored key.
func TestKeyHeader(t *testing.T) {
	s, err := Open("", clock)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(s)
	post := func(header string) int {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/issues",
			strings.NewReader(`{"title":"Panic"}`))
		req.Header.Set(KeyHeader, header)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	tests := []struct {
		header string
		status int
	}{
		{`"abc"`, http.StatusCreated},
		{`"abc"`, http.StatusOK},
		{`abc`, http.StatusBadRequest},
		{`"a\"b"`, http.StatusCreated},
		{`"a"b"`, http.StatusBadRequest},
		{`"caf\u00e9"`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		if got := post(tt.header); got != tt.status {
			t.Errorf("%s: status %d, want %d", tt.header, got, tt.status)
		}
	}
	if is, err := s.ByKey("abc"); err != nil || is.Key != "abc" {
		t.Errorf("ByKey(abc) = %+v, %v", is, err)
	}
	if _, err := s.ByKey(`a"b`); err != nil {
		t.Errorf(`ByKey(a"b): %v`, err)
	}
}

// TestTwoStores checks two stores on one file, as when a tracker
// server is restarted while its old process finishes a request: each
// sees the other's issues, and neither loses one.
func TestTwoStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.json")
	a, err := Open(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Create("Other", "", nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Create("Panic", "", nil, ""); err != nil {
		t.Fatal(err)
	}
	c, err := Open(path, clock)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Search(Query{}); len(got) != 2 || got[0].ID == got[1].ID {
		t.Errorf("the file holds %+v, want two issues", got)
	}
}
