package api

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"example.com/remind/auth"
	"example.com/remind/internal/must"
	"example.com/remind/notify"
)

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	a.writeJSON(w, http.StatusOK, map[string]any{
		"id": u.ID, "name": u.Name, "email": u.Email, "prefs": u.Prefs,
	})
}

func (a *API) updatePrefs(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	var req struct {
		TZ        string `json:"tz"`
		Channel   string `json:"channel"`
		Webhook   string `json:"webhook"`
		QuietFrom string `json:"quiet_from"` // "22:00"
		QuietTo   string `json:"quiet_to"`
	}
	if !a.decode(w, r, &req) {
		return
	}
	p := *u.Prefs
	if req.TZ != "" {
		p.TZ = req.TZ
	}
	if req.Channel != "" {
		if !notify.ValidChannel(req.Channel) {
			a.fail(w, http.StatusBadRequest, "unknown channel "+strconv.Quote(req.Channel))
			return
		}
		p.Channel = req.Channel
	}
	if req.Webhook != "" {
		if hook, err := url.Parse(req.Webhook); err != nil || hook.Scheme != "https" {
			a.fail(w, http.StatusBadRequest, "webhook must be an https URL")
			return
		}
		p.Webhook = req.Webhook
	}
	for _, t := range []string{req.QuietFrom, req.QuietTo} {
		if t != "" && !clockPattern.MatchString(t) {
			a.fail(w, http.StatusBadRequest, "quiet hours must look like 22:00")
			return
		}
	}
	if req.QuietFrom != "" || req.QuietTo != "" {
		p.QuietFrom, p.QuietTo = req.QuietFrom, req.QuietTo
	}
	if err := a.Store.SetPrefs(u.ID, p); err != nil {
		a.writeError(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, p)
}

var clockPattern = regexp.MustCompile(`^\d{1,2}:\d{2}$`)

func (a *API) digest(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	days := 7
	if s := r.URL.Query().Get("days"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 31 {
			a.fail(w, http.StatusBadRequest, "days must be between 1 and 31")
			return
		}
		days = n
	}
	loc := must.Get(time.LoadLocation(u.Prefs.TZ))
	from := a.Now()
	occ := a.Store.Upcoming(u.ID, from, from.AddDate(0, 0, days))
	d := notify.BuildDigest(from, days, loc, occ)
	if u.Prefs.QuietFrom != "" && u.Prefs.QuietTo != "" {
		d.QuietHours = notify.QuietHours(u.Prefs.QuietFrom, u.Prefs.QuietTo)
	}
	a.writeJSON(w, http.StatusOK, d)
}

// closeAccount deletes the caller's account and reminders.
func (a *API) closeAccount(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if err := a.Store.DeleteUser(u.ID); err != nil {
		a.writeError(w, err)
		return
	}
	a.Log.Info("account closed", "user", u.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) export(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	rems := a.Store.List(u.ID, 0, 1000)
	views := make([]reminderView, len(rems))
	for i, rem := range rems {
		views[i] = viewOf(rem)
	}
	w.Header().Set("Content-Disposition", `attachment; filename="reminders.json"`)
	a.writeJSON(w, http.StatusOK, views)
}

func (a *API) stats(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	now := a.Now()
	var open, done, overdue int
	for _, rem := range a.Store.List(u.ID, 0, 1000) {
		switch {
		case rem.Done:
			done++
		case rem.Due.Before(now):
			overdue++
		default:
			open++
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]int{"open": open, "done": done, "overdue": overdue})
}
