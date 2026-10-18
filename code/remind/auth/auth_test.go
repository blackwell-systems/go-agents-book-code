package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"example.com/remind/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s := store.New(time.Now)
	if err := s.AddUser(store.User{ID: "u1", Token: "tok1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUser(store.User{ID: "root", Token: "tok-admin", Admin: true}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestRequire(t *testing.T) {
	s := newStore(t)
	var got *store.User
	h := Require(s, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = UserFrom(r.Context())
	}))
	tests := []struct {
		header string
		status int
		user   string
	}{
		{"Bearer tok1", http.StatusOK, "u1"},
		{"Bearer nope", http.StatusUnauthorized, ""},
		{"", http.StatusUnauthorized, ""},
		{"Basic dTE6cHc=", http.StatusUnauthorized, ""},
	}
	for _, tt := range tests {
		got = nil
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		if tt.header != "" {
			req.Header.Set("Authorization", tt.header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tt.status {
			t.Errorf("%q: status %d, want %d", tt.header, rec.Code, tt.status)
		}
		if tt.user != "" && (got == nil || got.ID != tt.user) {
			t.Errorf("%q: user %v, want %s", tt.header, got, tt.user)
		}
	}
}

func TestRequireAdmin(t *testing.T) {
	s := newStore(t)
	h := RequireAdmin(s, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for header, want := range map[string]int{
		"Bearer tok-admin": http.StatusOK,
		"Bearer tok1":      http.StatusForbidden,
		"Bearer nope":      http.StatusUnauthorized,
	} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
		req.Header.Set("Authorization", header)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Errorf("%q: status %d, want %d", header, rec.Code, want)
		}
	}
}
