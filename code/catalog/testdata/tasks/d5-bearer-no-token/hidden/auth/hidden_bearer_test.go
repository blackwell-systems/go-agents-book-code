package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"example.com/remind/auth"
	"example.com/remind/store"
)

func TestHiddenBearerWithoutToken(t *testing.T) {
	s := store.New(time.Now)
	if err := s.AddUser(store.User{ID: "u1", Token: "tok1"}); err != nil {
		t.Fatal(err)
	}
	h := auth.Require(s, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, header := range []string{"Bearer", "Bearer ", "Bearertok1"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
		req.Header.Set("Authorization", header)
		rec := httptest.NewRecorder()
		func() {
			defer func() {
				if v := recover(); v != nil {
					t.Errorf("%q: panic: %v", header, v)
				}
			}()
			h.ServeHTTP(rec, req)
		}()
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%q: status %d, want 401", header, rec.Code)
		}
	}
}
