package api_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHiddenMeRoutesNeedAuth(t *testing.T) {
	srv := hiddenServer(t, time.Date(2026, 10, 5, 14, 0, 0, 0, time.UTC))
	hiddenCreate(t, srv, "h-tok-ana", `{"text":"ana's secret plan","in":"1h"}`)
	for _, path := range []string{"/v1/me/export", "/v1/me/stats"} {
		if code, _ := hiddenDo(t, srv, "", http.MethodGet, path, ""); code != http.StatusUnauthorized {
			t.Errorf("%s without a token: status %d, want 401", path, code)
		}
		code, body := hiddenDo(t, srv, "h-tok-bo", http.MethodGet, path, "")
		if code != http.StatusOK || strings.Contains(body, "secret plan") {
			t.Errorf("%s as Bo: status %d, body %s", path, code, body)
		}
	}
	if _, body := hiddenDo(t, srv, "h-tok-ana", http.MethodGet, "/v1/me/export", ""); !strings.Contains(body, "secret plan") {
		t.Errorf("Ana's export = %s, want her reminder", body)
	}
}
