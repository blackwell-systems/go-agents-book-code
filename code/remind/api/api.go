// Package api serves remind's HTTP API.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"example.com/remind/audit"
	"example.com/remind/auth"
	"example.com/remind/notify"
	"example.com/remind/quota"
	"example.com/remind/search"
	"example.com/remind/share"
	"example.com/remind/store"
)

// API holds what the handlers need.
type API struct {
	Store    *store.Store
	Notify   *notify.Dispatcher
	Lists    *share.Lists
	Search   *search.Index
	Audit    *audit.Log
	Limiter  *quota.Limiter // per-user rate limits; nil turns them off
	Metrics  http.Handler   // served at /metrics if not nil
	Log      *slog.Logger
	Now      func() time.Time
	Snapshot func() (io.WriteCloser, error) // where admin snapshots go
}

// planOf returns the user's plan, defaulting to the free plan.
func planOf(u *store.User) quota.Plan {
	p, err := quota.PlanByName(u.Plan)
	if err != nil {
		return quota.Free
	}
	return p
}

// limit applies the per-user rate limit, if there is one.
func (a *API) limit(h http.Handler) http.Handler {
	if a.Limiter == nil {
		return h
	}
	return quota.Middleware(a.Limiter, func(r *http.Request) (string, quota.Plan) {
		u := auth.UserFrom(r.Context())
		return u.ID, planOf(u)
	}, h)
}

// Routes returns the API's handler.
func (a *API) Routes() http.Handler {
	mux := http.NewServeMux()
	user := func(h http.HandlerFunc) http.Handler { return auth.Require(a.Store, a.limit(h)) }
	admin := func(h http.HandlerFunc) http.Handler { return auth.RequireAdmin(a.Store, h) }

	mux.Handle("POST /v1/reminders", user(a.createReminder))
	mux.Handle("GET /v1/reminders", user(a.listReminders))
	mux.Handle("GET /v1/reminders/{id}", user(a.getReminder))
	mux.Handle("DELETE /v1/reminders/{id}", user(a.deleteReminder))
	mux.Handle("POST /v1/reminders/{id}/done", user(a.markDone))
	mux.Handle("POST /v1/reminders/{id}/snooze", user(a.snooze))
	mux.Handle("POST /v1/reminders/{id}/test", user(a.testNotify))

	mux.Handle("GET /v1/me", user(a.me))
	mux.Handle("DELETE /v1/me", user(a.closeAccount))
	mux.Handle("PUT /v1/me/prefs", user(a.updatePrefs))
	mux.Handle("GET /v1/me/digest", user(a.digest))
	mux.Handle("GET /v1/me/calendar.ics", user(a.calendarFeed))
	mux.Handle("GET /v1/search", user(a.search))

	mux.Handle("POST /v1/lists", user(a.createList))
	mux.Handle("GET /v1/lists", user(a.listLists))
	mux.Handle("GET /v1/lists/{id}", user(a.getList))
	mux.Handle("DELETE /v1/lists/{id}", user(a.deleteList))
	mux.Handle("POST /v1/lists/{id}/members", user(a.addMember))
	mux.Handle("DELETE /v1/lists/{id}/members/{user}", user(a.removeMember))
	mux.Handle("POST /v1/lists/{id}/reminders", user(a.addListReminder))
	mux.Handle("DELETE /v1/lists/{id}/reminders/{reminder}", user(a.removeListReminder))

	// Read-only views of the caller's own data.
	mux.HandleFunc("GET /v1/me/export", a.export)
	mux.HandleFunc("GET /v1/me/stats", a.stats)

	mux.Handle("POST /v1/admin/users/import", admin(a.importUsers))
	mux.Handle("DELETE /v1/admin/users/{id}", admin(a.deleteUser))
	mux.Handle("POST /v1/admin/dispatcher/stop", admin(a.stopDispatcher))
	mux.Handle("POST /v1/admin/snapshot", admin(a.snapshot))
	mux.Handle("GET /v1/admin/dispatcher/status", admin(a.dispatcherStatus))
	mux.Handle("GET /v1/admin/audit", admin(a.auditLog))

	if a.Metrics != nil {
		mux.Handle("GET /metrics", a.Metrics)
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		a.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return mux
}

// decode reads a JSON request body into v, answering 400 on failure.
func (a *API) decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		a.fail(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return false
	}
	return true
}

func (a *API) writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		a.Log.Error("encode response", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(append(b, '\n')); err != nil {
		a.Log.Debug("write response", "err", err)
	}
}

func (a *API) fail(w http.ResponseWriter, status int, msg string) {
	a.writeJSON(w, status, map[string]string{"error": msg})
}

// writeError answers with the status that matches a store error.
func (a *API) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		a.fail(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrConflict):
		a.fail(w, http.StatusConflict, "conflict")
	default:
		a.Log.Error("request failed", "err", err)
		a.fail(w, http.StatusInternalServerError, "internal error")
	}
}

// page reads offset and limit from the query.
func (a *API) page(w http.ResponseWriter, r *http.Request) (offset, limit int, ok bool) {
	offset, limit = 0, 50
	q := r.URL.Query()
	var err error
	if s := q.Get("offset"); s != "" {
		if offset, err = strconv.Atoi(s); err != nil || offset < 0 {
			a.fail(w, http.StatusBadRequest, "offset must be a whole number")
			return 0, 0, false
		}
	}
	if s := q.Get("limit"); s != "" {
		if limit, err = strconv.Atoi(s); err != nil || limit < 1 || limit > 100 {
			a.fail(w, http.StatusBadRequest, "limit must be between 1 and 100")
			return 0, 0, false
		}
	}
	return offset, limit, true
}
