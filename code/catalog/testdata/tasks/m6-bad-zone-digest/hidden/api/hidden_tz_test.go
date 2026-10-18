package api_test

import (
	"net/http"
	"testing"
	"time"
)

func TestHiddenPrefsUnknownZone(t *testing.T) {
	srv := hiddenServer(t, time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC))
	if code, resp := hiddenDo(t, srv, "h-tok-ana", http.MethodPut, "/v1/me/prefs", `{"tz":"Europe/Lisboa"}`); code != http.StatusBadRequest {
		t.Errorf("unknown zone: status %d, want 400: %s", code, resp)
	}
	if code, resp := hiddenDo(t, srv, "h-tok-ana", http.MethodGet, "/v1/me/digest", ""); code != http.StatusOK {
		t.Errorf("digest after a rejected zone: status %d: %s", code, resp)
	}
	if code, resp := hiddenDo(t, srv, "h-tok-ana", http.MethodPut, "/v1/me/prefs", `{"tz":"Europe/Lisbon"}`); code != http.StatusOK {
		t.Errorf("known zone: status %d: %s", code, resp)
	}
	if code, resp := hiddenDo(t, srv, "h-tok-ana", http.MethodGet, "/v1/me/digest", ""); code != http.StatusOK {
		t.Errorf("digest: status %d: %s", code, resp)
	}
}
