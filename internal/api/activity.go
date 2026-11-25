package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// The activity page's half of the record: the store itself lives with the
// services (service.Activity), because the API layer does not keep tables
// of its own. actionlog.go and the sign-in handler write to it; this file
// only reads it back.
//
// Who reads how much of it follows who they are. The owner reads the
// whole record: it names every operator and where they work from, and
// that is the owner's to know. Anybody else reads their own lines and
// only theirs -- what they did, and the sign-ins tried under their name
// -- narrowed in the store by the caller's own id, so nothing a caller
// puts in the request (an actor, a search, another operator's id) can
// widen it. The narrowing is not a filter the page applies; it is the
// query itself.

// activityView is one entry as the activity page reads it. The time is a
// string because the page wants it formatted one way -- RFC3339 -- and a
// time.Time would arrive in whatever shape the encoder chose.
type activityView struct {
	ID     uint   `json:"id"`
	At     string `json:"at"`
	Actor  string `json:"actor"`
	Role   string `json:"role"`
	Action string `json:"action"`
	Status int    `json:"status"`
	IP     string `json:"ip"`
	Reason string `json:"reason"`
}

// activityViews is the entries as the pages read them, shared by the
// activity page and the reseller dashboard's shorter list.
func activityViews(entries []model.ActivityEntry) []activityView {
	items := make([]activityView, 0, len(entries))
	for _, e := range entries {
		items = append(items, activityView{
			ID:     e.ID,
			At:     e.CreatedAt.Format(time.RFC3339),
			Actor:  e.Actor,
			Role:   e.Role,
			Action: e.Action,
			Status: e.Status,
			IP:     e.IP,
			Reason: e.Reason,
		})
	}
	return items
}

// handleActivity serves the activity record, newest first: the whole of
// it to the owner, and to any other operator only their own entries.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	if admin == nil {
		// The gate has already refused a machine token; this is the
		// handler keeping its own promise if the gates ever change.
		writeError(w, http.StatusForbidden, "the activity record is an operator's to read")
		return
	}
	items := []activityView{}
	if s.activity != nil {
		limit := 100
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		limit = min(max(limit, 1), 500)
		var before uint
		if v := r.URL.Query().Get("before"); v != "" {
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				before = uint(n)
			}
		}
		var entries []model.ActivityEntry
		var err error
		if admin.Role.ManagesPanel() {
			entries, err = s.activity.List(limit, before)
		} else {
			// The caller's own id and name, taken from their session --
			// never from the request, which is the whole of the rule.
			entries, err = s.activity.ListForActor(admin.ID, admin.Username, limit, before)
		}
		if err != nil {
			fail(w, s.log, err)
			return
		}
		items = activityViews(entries)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
