package api_test

import (
	"net/http"
	"testing"
	"time"
)

func TestHiddenAdminDeleteUser(t *testing.T) {
	srv := hiddenServer(t, time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC))
	if code, resp := hiddenDo(t, srv, "h-tok-root", http.MethodDelete, "/v1/admin/users/h_bo", ""); code != http.StatusNoContent {
		t.Errorf("admin delete user: status %d: %s", code, resp)
	}
	if code, _ := hiddenDo(t, srv, "h-tok-bo", http.MethodGet, "/v1/me", ""); code != http.StatusUnauthorized {
		t.Errorf("deleted user: status %d, want 401", code)
	}
}
