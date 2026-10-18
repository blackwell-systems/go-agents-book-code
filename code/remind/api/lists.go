package api

import (
	"errors"
	"net/http"

	"example.com/remind/auth"
	"example.com/remind/share"
)

// listError answers with the status that matches a share error.
func (a *API) listError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, share.ErrNotFound):
		a.fail(w, http.StatusNotFound, "not found")
	case errors.Is(err, share.ErrForbidden):
		a.fail(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, share.ErrInvalid):
		a.fail(w, http.StatusBadRequest, err.Error())
	default:
		a.writeError(w, err)
	}
}

func (a *API) createList(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	var req struct {
		Name string `json:"name"`
	}
	if !a.decode(w, r, &req) {
		return
	}
	l, err := a.Lists.Create(u.ID, req.Name)
	if err != nil {
		a.listError(w, err)
		return
	}
	a.writeJSON(w, http.StatusCreated, l)
}

func (a *API) listLists(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	a.writeJSON(w, http.StatusOK, map[string]any{"lists": a.Lists.ForUser(u.ID)})
}

// getList returns a list with the reminders on it.
func (a *API) getList(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	l, err := a.Lists.Get(r.PathValue("id"), u.ID)
	if err != nil {
		a.listError(w, err)
		return
	}
	views := make([]reminderView, 0, len(l.Reminders))
	for _, id := range l.Reminders {
		rem, err := a.Store.Get(id)
		if err != nil {
			continue
		}
		views = append(views, viewOf(rem))
	}
	owner, _ := a.Store.User(l.Owner)
	a.writeJSON(w, http.StatusOK, map[string]any{"list": l, "owner": owner.Name, "reminders": views})
}

func (a *API) addMember(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	var req struct {
		User string `json:"user"`
		Role string `json:"role"`
	}
	if !a.decode(w, r, &req) {
		return
	}
	role, err := share.ParseRole(req.Role)
	if err != nil {
		a.fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := a.Store.User(req.User); err != nil {
		a.fail(w, http.StatusBadRequest, "no such user")
		return
	}
	id := r.PathValue("id")
	if err := a.Lists.AddMember(id, u.ID, req.User, role); err != nil {
		a.listError(w, err)
		return
	}
	a.Audit.Record(u.ID, "list.member.add", id, req.User+" as "+role.String())
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) removeMember(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	id, member := r.PathValue("id"), r.PathValue("user")
	if err := a.Lists.RemoveMember(id, u.ID, member); err != nil {
		a.listError(w, err)
		return
	}
	a.Audit.Record(u.ID, "list.member.remove", id, member)
	w.WriteHeader(http.StatusNoContent)
}

// addListReminder puts one of the caller's reminders on a list.
func (a *API) addListReminder(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	var req struct {
		Reminder string `json:"reminder"`
	}
	if !a.decode(w, r, &req) {
		return
	}
	rem, err := a.Store.Get(req.Reminder)
	if err != nil {
		a.writeError(w, err)
		return
	}
	if rem.UserID != u.ID {
		a.fail(w, http.StatusNotFound, "not found")
		return
	}
	if err := a.Lists.AddReminder(r.PathValue("id"), u.ID, rem.ID); err != nil {
		a.listError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) removeListReminder(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if err := a.Lists.RemoveReminder(r.PathValue("id"), u.ID, r.PathValue("reminder")); err != nil {
		a.listError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) deleteList(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	id := r.PathValue("id")
	if err := a.Lists.Delete(id, u.ID); err != nil {
		a.listError(w, err)
		return
	}
	a.Audit.Record(u.ID, "list.delete", id, "")
	w.WriteHeader(http.StatusNoContent)
}
