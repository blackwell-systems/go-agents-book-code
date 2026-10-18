package api_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"example.com/remind/server"
	"example.com/remind/store"
)

// Helpers shared by the catalog's hidden API tests.

func hiddenUsers() []store.User {
	return []store.User{
		{ID: "h_ana", Name: "Ana", Email: "ana@example.com", Token: "h-tok-ana"},
		{ID: "h_bo", Name: "Bo", Email: "bo@example.com", Token: "h-tok-bo"},
		{ID: "h_root", Name: "Root", Token: "h-tok-root", Admin: true},
	}
}

func hiddenServer(t *testing.T, now time.Time, users ...store.User) *server.Server {
	t.Helper()
	if users == nil {
		users = hiddenUsers()
	}
	srv, err := server.New(server.Config{Now: func() time.Time { return now }, Users: users})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func hiddenDo(t *testing.T, srv *server.Server, token, method, path, body string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(t.Context(), method, path, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func hiddenCreate(t *testing.T, srv *server.Server, token, body string) string {
	t.Helper()
	code, resp := hiddenDo(t, srv, token, http.MethodPost, "/v1/reminders", body)
	if code != http.StatusCreated {
		t.Fatalf("create %s: status %d: %s", body, code, resp)
	}
	var out struct{ ID string }
	if err := json.Unmarshal([]byte(resp), &out); err != nil {
		t.Fatal(err)
	}
	return out.ID
}
