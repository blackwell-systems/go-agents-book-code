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

var t0 = time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC) // a Monday

type client struct {
	t   *testing.T
	srv *server.Server
}

func newClient(t *testing.T) *client {
	t.Helper()
	srv, err := server.New(server.Config{
		Now: func() time.Time { return t0 },
		Users: []store.User{
			{ID: "ana", Name: "Ana", Email: "ana@example.com", Token: "tok-ana"},
			{ID: "bo", Name: "Bo", Email: "bo@example.com", Token: "tok-bo"},
			{ID: "root", Name: "Root", Token: "tok-root", Admin: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &client{t: t, srv: srv}
}

// do sends a request as the user with the given token and decodes a JSON
// reply into out, if out is not nil.
func (c *client) do(token, method, path, body string, out any) int {
	c.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequestWithContext(c.t.Context(), method, path, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	c.srv.Handler.ServeHTTP(rec, req)
	if out != nil && rec.Code < 300 {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			c.t.Fatalf("%s %s: decode %q: %v", method, path, rec.Body.String(), err)
		}
	}
	return rec.Code
}

func (c *client) create(token, body string) string {
	c.t.Helper()
	var out struct{ ID string }
	if code := c.do(token, http.MethodPost, "/v1/reminders", body, &out); code != http.StatusCreated {
		c.t.Fatalf("create %s: status %d", body, code)
	}
	return out.ID
}

func TestCreateAndList(t *testing.T) {
	c := newClient(t)
	c.create("tok-ana", `{"text":"call the dentist","in":"1h30m"}`)
	c.create("tok-ana", `{"text":"stand-up","every":"on mon,wed at 09:30"}`)
	c.create("tok-ana", `{"text":"stretch","every":"every 2h"}`)
	c.create("tok-bo", `{"text":"not Ana's","at":"2026-09-08T10:00:00Z"}`)

	var got struct {
		Reminders []struct {
			ID, Text, Preview string
			Due               time.Time
			Schedule          struct{ Spec, Zone string }
		}
	}
	if code := c.do("tok-ana", http.MethodGet, "/v1/reminders", "", &got); code != http.StatusOK {
		t.Fatalf("list: status %d", code)
	}
	if len(got.Reminders) != 3 {
		t.Fatalf("list = %+v, want 3 reminders", got.Reminders)
	}
	if want := t0.Add(90 * time.Minute); !got.Reminders[0].Due.Equal(want) {
		t.Errorf("due = %v, want %v", got.Reminders[0].Due, want)
	}
	if s := got.Reminders[1].Schedule; s.Spec != "on mon,wed at 09:30" || s.Zone != "UTC" {
		t.Errorf("schedule = %+v", s)
	}
	if code := c.do("tok-ana", http.MethodGet, "/v1/reminders?limit=1&offset=2", "", &got); code != http.StatusOK || len(got.Reminders) != 1 {
		t.Errorf("page: status %d, %d reminders", code, len(got.Reminders))
	}
}

func TestCreateErrors(t *testing.T) {
	c := newClient(t)
	for _, body := range []string{
		`{"text":"","in":"1h"}`,
		`{"text":"x"}`,
		`{"text":"x","in":"1h30"}`,
		`{"text":"x","every":"daily"}`,
		`{"text":"x","at":"tomorrow"}`,
		`not json`,
	} {
		if code := c.do("tok-ana", http.MethodPost, "/v1/reminders", body, nil); code != http.StatusBadRequest {
			t.Errorf("create %s: status %d, want 400", body, code)
		}
	}
	if code := c.do("", http.MethodPost, "/v1/reminders", `{"text":"x","in":"1h"}`, nil); code != http.StatusUnauthorized {
		t.Errorf("no token: status %d, want 401", code)
	}
}

func TestOwnership(t *testing.T) {
	c := newClient(t)
	id := c.create("tok-ana", `{"text":"mine","in":"1h"}`)
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/v1/reminders/" + id},
		{http.MethodPost, "/v1/reminders/" + id + "/done"},
		{http.MethodDelete, "/v1/reminders/" + id},
	} {
		if code := c.do("tok-bo", tt.method, tt.path, "", nil); code != http.StatusNotFound {
			t.Errorf("Bo %s %s: status %d, want 404", tt.method, tt.path, code)
		}
	}
	if code := c.do("tok-ana", http.MethodGet, "/v1/reminders/rem_9999", "", nil); code != http.StatusNotFound {
		t.Errorf("missing reminder: status %d, want 404", code)
	}
}

func TestDoneSnoozeDelete(t *testing.T) {
	c := newClient(t)
	id := c.create("tok-ana", `{"text":"water plants","in":"1h"}`)
	if code := c.do("tok-ana", http.MethodPost, "/v1/reminders/"+id+"/snooze", `{"for":"2h"}`, nil); code != http.StatusNoContent {
		t.Fatalf("snooze: status %d", code)
	}
	var got struct {
		Due  time.Time
		Done bool
	}
	c.do("tok-ana", http.MethodGet, "/v1/reminders/"+id, "", &got)
	if want := t0.Add(2 * time.Hour); !got.Due.Equal(want) {
		t.Errorf("due after snooze = %v, want %v", got.Due, want)
	}
	if code := c.do("tok-ana", http.MethodPost, "/v1/reminders/"+id+"/done", "", nil); code != http.StatusNoContent {
		t.Fatalf("done: status %d", code)
	}
	c.do("tok-ana", http.MethodGet, "/v1/reminders/"+id, "", &got)
	if !got.Done {
		t.Error("not done after done")
	}
	if code := c.do("tok-ana", http.MethodDelete, "/v1/reminders/"+id, "", nil); code != http.StatusNoContent {
		t.Fatalf("delete: status %d", code)
	}
}

func TestPrefsAndTestNotify(t *testing.T) {
	c := newClient(t)
	var prefs struct{ TZ, Channel string }
	if code := c.do("tok-ana", http.MethodPut, "/v1/me/prefs", `{"tz":"Europe/Lisbon"}`, &prefs); code != http.StatusOK {
		t.Fatalf("prefs: status %d", code)
	}
	if prefs.TZ != "Europe/Lisbon" || prefs.Channel != "email" {
		t.Errorf("prefs = %+v", prefs)
	}
	for _, body := range []string{`{"channel":"pigeon"}`, `{"webhook":"http://example.com/hook"}`} {
		if code := c.do("tok-ana", http.MethodPut, "/v1/me/prefs", body, nil); code != http.StatusBadRequest {
			t.Errorf("prefs %s: status %d, want 400", body, code)
		}
	}
	id := c.create("tok-ana", `{"text":"test me","in":"1h"}`)
	if code := c.do("tok-ana", http.MethodPost, "/v1/reminders/"+id+"/test", "", nil); code != http.StatusNoContent {
		t.Errorf("test notify: status %d", code)
	}
}

func TestDigest(t *testing.T) {
	c := newClient(t)
	c.create("tok-ana", `{"text":"stand-up","every":"on mon,wed at 09:30"}`)
	c.create("tok-ana", `{"text":"dentist","in":"30h"}`)
	var got struct {
		Days []struct {
			Date  string
			Items []struct{ Text string }
		}
	}
	if code := c.do("tok-ana", http.MethodGet, "/v1/me/digest?days=3", "", &got); code != http.StatusOK {
		t.Fatalf("digest: status %d", code)
	}
	var texts []string
	for _, d := range got.Days {
		for _, it := range d.Items {
			texts = append(texts, d.Date+" "+it.Text)
		}
	}
	want := "2026-09-07 stand-up|2026-09-08 dentist|2026-09-09 stand-up"
	if strings.Join(texts, "|") != want {
		t.Errorf("digest = %q, want %q", texts, want)
	}
}

func TestAdmin(t *testing.T) {
	c := newClient(t)
	var res struct{ Added int }
	body := `[{"id":"cy","name":"Cy","email":"cy@example.com","token":"tok-cy"},{"id":"ana"}]`
	if code := c.do("tok-root", http.MethodPost, "/v1/admin/users/import", body, &res); code != http.StatusOK || res.Added != 1 {
		t.Errorf("import: status %d, added %d", code, res.Added)
	}
	if code := c.do("tok-cy", http.MethodPost, "/v1/reminders", `{"text":"hi","in":"1h"}`, nil); code != http.StatusCreated {
		t.Errorf("imported user create: status %d", code)
	}
	if code := c.do("tok-ana", http.MethodPost, "/v1/admin/users/import", "[]", nil); code != http.StatusForbidden {
		t.Errorf("non-admin import: status %d, want 403", code)
	}
}

func TestHealth(t *testing.T) {
	c := newClient(t)
	if code := c.do("", http.MethodGet, "/healthz", "", nil); code != http.StatusOK {
		t.Errorf("healthz: status %d", code)
	}
}
