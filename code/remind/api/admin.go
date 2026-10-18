package api

import (
	"net/http"
	"time"

	"example.com/remind/audit"
	"example.com/remind/auth"
	"example.com/remind/store"
)

// audit logs an administrator's action.
func (a *API) audit(r *http.Request, action, target string) {
	admin := auth.UserFrom(r.Context())
	a.Log.Info("admin action", "admin", admin.ID, "action", action, "target", target)
	a.Audit.Record(admin.ID, action, target, "")
}

func (a *API) dispatcherStatus(w http.ResponseWriter, _ *http.Request) {
	a.writeJSON(w, http.StatusOK, a.Notify.Status())
}

func (a *API) auditLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := audit.Filter{Actor: q.Get("actor"), Action: q.Get("action"), Limit: 100}
	if s := q.Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			a.fail(w, http.StatusBadRequest, "since must be an RFC 3339 time")
			return
		}
		f.Since = t
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"entries": a.Audit.Query(f)})
}

// importUsers adds users exported from the old reminders system,
// keeping their API tokens so their apps keep working.
func (a *API) importUsers(w http.ResponseWriter, r *http.Request) {
	var req []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Email string `json:"email"`
		Token string `json:"token"`
	}
	if !a.decode(w, r, &req) {
		return
	}
	users := make([]store.User, len(req))
	for i, u := range req {
		users[i] = store.User{ID: u.ID, Name: u.Name, Email: u.Email, Token: u.Token}
	}
	n := a.Store.ImportUsers(users)
	a.Log.Info("users imported", "added", n, "skipped", len(users)-n)
	a.writeJSON(w, http.StatusOK, map[string]int{"added": n})
}

func (a *API) deleteUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a.audit(r, "delete user", id)
	if err := a.Store.DeleteUser(id); err != nil {
		a.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) stopDispatcher(w http.ResponseWriter, _ *http.Request) {
	a.Notify.Stop()
	a.Log.Warn("dispatcher stopped by an administrator")
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) snapshot(w http.ResponseWriter, _ *http.Request) {
	out, err := a.Snapshot()
	if err != nil {
		a.Log.Error("store: snapshot failed", "err", err)
		a.fail(w, http.StatusInternalServerError, "snapshot failed")
		return
	}
	err = a.Store.Snapshot(out)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		a.Log.Error("store: snapshot failed", "err", err)
		a.fail(w, http.StatusInternalServerError, "snapshot failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
