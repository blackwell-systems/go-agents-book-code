package api_test

import (
	"net/http"
	"testing"
	"time"
)

func TestHiddenActOnDeleted(t *testing.T) {
	srv := hiddenServer(t, time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC))
	id := hiddenCreate(t, srv, "h-tok-ana", `{"text":"x","in":"1h"}`)
	if code, _ := hiddenDo(t, srv, "h-tok-ana", http.MethodDelete, "/v1/reminders/"+id, ""); code != http.StatusNoContent {
		t.Fatalf("delete: status %d", code)
	}
	for _, tt := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/reminders/" + id + "/done", ""},
		{http.MethodGet, "/v1/reminders/" + id, ""},
		{http.MethodPost, "/v1/reminders/" + id + "/snooze", `{"for":"5m"}`},
		{http.MethodDelete, "/v1/reminders/" + id, ""},
	} {
		if code, body := hiddenDo(t, srv, "h-tok-ana", tt.method, tt.path, tt.body); code != http.StatusNotFound {
			t.Errorf("%s %s after delete: status %d, want 404: %s", tt.method, tt.path, code, body)
		}
	}
}
