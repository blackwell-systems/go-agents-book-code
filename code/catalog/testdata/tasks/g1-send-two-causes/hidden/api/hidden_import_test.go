package api_test

import (
	"net/http"
	"testing"
	"time"
)

func TestHiddenImportedUserPrefs(t *testing.T) {
	srv := hiddenServer(t, time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC))
	body := `[{"id":"h_cy","name":"Cy","email":"cy@example.com","token":"h-tok-cy"}]`
	if code, resp := hiddenDo(t, srv, "h-tok-root", http.MethodPost, "/v1/admin/users/import", body); code != http.StatusOK {
		t.Fatalf("import: status %d: %s", code, resp)
	}
	if code, resp := hiddenDo(t, srv, "h-tok-cy", http.MethodPut, "/v1/me/prefs", `{"tz":"Africa/Lagos"}`); code != http.StatusOK {
		t.Errorf("imported user sets prefs: status %d: %s", code, resp)
	}
	id := hiddenCreate(t, srv, "h-tok-cy", `{"text":"x","in":"1h"}`)
	if code, resp := hiddenDo(t, srv, "h-tok-cy", http.MethodPost, "/v1/reminders/"+id+"/test", ""); code != http.StatusNoContent {
		t.Errorf("imported user test notification: status %d: %s", code, resp)
	}
}
