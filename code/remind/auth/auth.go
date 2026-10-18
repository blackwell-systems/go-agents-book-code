// Package auth authenticates API requests by bearer token.
package auth

import (
	"context"
	"net/http"
	"strings"

	"example.com/remind/store"
)

type userKey struct{}

// Require lets a request through only if it carries a valid API token,
// and makes the token's user available through UserFrom.
func Require(s *store.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := authenticate(s, r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, u)))
	})
}

// RequireAdmin is Require for administrators only.
func RequireAdmin(s *store.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := authenticate(s, r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if !u.Admin {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, *u)))
	})
}

// UserFrom returns the authenticated user, or nil if there is none.
func UserFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(userKey{}).(*store.User)
	return u
}

func authenticate(s *store.Store, r *http.Request) (*store.User, bool) {
	token, ok := parseBearer(r.Header.Get("Authorization"))
	if !ok {
		return nil, false
	}
	u, err := s.UserByToken(token)
	if err != nil {
		return nil, false
	}
	return u, true
}

// parseBearer extracts the token from "Bearer <token>".
func parseBearer(h string) (string, bool) {
	if !strings.HasPrefix(h, "Bearer") {
		return "", false
	}
	return strings.SplitN(h, " ", 2)[1], true
}
