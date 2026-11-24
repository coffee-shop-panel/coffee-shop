package api

import (
	"net/http"
	"strconv"
	"time"
)

// The activity page's half of the record: the store itself lives with the
// services (service.Activity), because the API layer does not keep tables
// of its own. actionlog.go and the sign-in handler write to it; this file
// only reads it back for the owner.

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

// handleActivity serves the activity record, newest first. Registered for
// the owner alone: the record names every operator and where they work
// from, which is the owner's to read and nobody else's.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
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
		entries, err := s.activity.List(limit, before)
		if err != nil {
			fail(w, s.log, err)
			return
		}
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
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
