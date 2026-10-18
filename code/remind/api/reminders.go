package api

import (
	"cmp"
	"net/http"
	"strings"
	"time"

	"example.com/remind/auth"
	"example.com/remind/duration"
	"example.com/remind/notify"
	"example.com/remind/quota"
	"example.com/remind/schedule"
	"example.com/remind/store"
)

type createRequest struct {
	Text  string `json:"text"`
	In    string `json:"in"`    // from now, such as "1h30m"
	At    string `json:"at"`    // an RFC 3339 time
	Every string `json:"every"` // a schedule spec, such as "on mon at 09:00"
	TZ    string `json:"tz"`    // for weekly schedules; defaults to the user's
	// Priority is 1 (high), 2 (normal), or 3 (low); omitted means normal.
	Priority *int `json:"priority"`
}

func (a *API) createReminder(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	var req createRequest
	if !a.decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Text) == "" {
		a.fail(w, http.StatusBadRequest, "text is required")
		return
	}
	priority := 2
	if req.Priority != nil {
		if *req.Priority > 3 {
			a.fail(w, http.StatusBadRequest, "priority must be 1, 2, or 3")
			return
		}
		priority = *req.Priority
	}
	var (
		due  time.Time
		rule schedule.Rule
		err  error
	)
	switch {
	case req.Every != "":
		zone := schedule.LookupZone(cmp.Or(req.TZ, u.Prefs.TZ))
		rule, err = schedule.Parse(req.Every, zone, a.Now())
	case req.In != "":
		var d time.Duration
		d, err = duration.Parse(req.In)
		due = a.Now().Add(d)
	case req.At != "":
		due, err = time.Parse(time.RFC3339, req.At)
	default:
		a.fail(w, http.StatusBadRequest, "one of in, at, or every is required")
		return
	}
	if err != nil {
		a.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := quota.CheckReminders(planOf(u), a.Store.Count(u.ID)); err != nil {
		a.fail(w, http.StatusForbidden, err.Error())
		return
	}
	rem := a.Store.Create(u.ID, req.Text, due, rule)
	if priority != 2 {
		err := a.Store.Update(rem.ID, func(r *store.Reminder) error {
			r.Priority = priority
			return nil
		})
		if err != nil {
			a.writeError(w, err)
			return
		}
	}
	a.Search.Add(rem.ID, u.ID, rem.Text)
	a.Log.Info("reminder created", "id", rem.ID, "user", u.ID)
	w.Header().Set("Location", "/v1/reminders/"+rem.ID)
	a.writeJSON(w, http.StatusCreated, map[string]string{"id": rem.ID})
}

type reminderView struct {
	ID       string        `json:"id"`
	Text     string        `json:"text"`
	Preview  string        `json:"preview"`
	Due      time.Time     `json:"due"`
	Schedule schedule.Rule `json:"schedule,omitempty"`
	Priority string        `json:"priority"`
	Done     bool          `json:"done"`
}

var priorityNames = [...]string{"high", "normal", "low"}

// priorityName names a stored priority; 0 is normal.
func priorityName(p int) string {
	if p == 0 {
		return "normal"
	}
	return priorityNames[p-1]
}

func viewOf(r *store.Reminder) reminderView {
	return reminderView{
		ID:       r.ID,
		Text:     r.Text,
		Preview:  notify.Preview(r.Text),
		Due:      r.Due,
		Schedule: r.Rule,
		Priority: priorityName(r.Priority),
		Done:     r.Done,
	}
}

func (a *API) listReminders(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	offset, limit, ok := a.page(w, r)
	if !ok {
		return
	}
	rems := a.Store.List(u.ID, offset, limit)
	views := make([]reminderView, len(rems))
	for i, rem := range rems {
		views[i] = viewOf(rem)
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"reminders": views})
}

// owned returns the caller's reminder with the request's ID, or answers
// 404 and returns nil.
func (a *API) owned(w http.ResponseWriter, r *http.Request) *store.Reminder {
	u := auth.UserFrom(r.Context())
	rem, err := a.Store.Get(r.PathValue("id"))
	if err != nil {
		a.writeError(w, err)
		return nil
	}
	if rem.UserID != u.ID {
		a.fail(w, http.StatusNotFound, "not found")
		return nil
	}
	return rem
}

func (a *API) getReminder(w http.ResponseWriter, r *http.Request) {
	if rem := a.owned(w, r); rem != nil {
		a.writeJSON(w, http.StatusOK, viewOf(rem))
	}
}

func (a *API) deleteReminder(w http.ResponseWriter, r *http.Request) {
	rem := a.owned(w, r)
	if rem == nil {
		return
	}
	if err := a.Store.Delete(rem.ID); err != nil {
		a.writeError(w, err)
		return
	}
	a.Search.Remove(rem.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) markDone(w http.ResponseWriter, r *http.Request) {
	rem := a.owned(w, r)
	if rem == nil {
		return
	}
	err := a.Store.Update(rem.ID, func(r *store.Reminder) error {
		r.Done = true
		return nil
	})
	if err != nil {
		a.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) snooze(w http.ResponseWriter, r *http.Request) {
	rem := a.owned(w, r)
	if rem == nil {
		return
	}
	var req struct {
		For string `json:"for"` // such as "10m"
	}
	if !a.decode(w, r, &req) {
		return
	}
	d, err := duration.Parse(req.For)
	if err != nil {
		a.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	err = a.Store.Update(rem.ID, func(r *store.Reminder) error {
		r.Due = a.Now().Add(d)
		return nil
	})
	if err != nil {
		a.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) testNotify(w http.ResponseWriter, r *http.Request) {
	rem := a.owned(w, r)
	if rem == nil {
		return
	}
	if err := a.Notify.SendNow(r.Context(), auth.UserFrom(r.Context()), rem); err != nil {
		a.fail(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
