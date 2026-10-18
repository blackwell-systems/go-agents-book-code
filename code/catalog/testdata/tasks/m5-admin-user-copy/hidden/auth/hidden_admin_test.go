package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"example.com/remind/auth"
	"example.com/remind/store"
)

func TestHiddenAdminUserFrom(t *testing.T) {
	s := store.New(time.Now)
	if err := s.AddUser(store.User{ID: "root", Token: "tok-admin", Admin: true}); err != nil {
		t.Fatal(err)
	}
	var got *store.User
	h := auth.RequireAdmin(s, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got = auth.UserFrom(r.Context())
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer tok-admin")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got == nil || got.ID != "root" {
		t.Errorf("UserFrom in an admin handler = %v, want root", got)
	}
}
