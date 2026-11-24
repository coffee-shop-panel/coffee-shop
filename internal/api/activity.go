package api

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// The activity table.
//
// actionlog.go writes who did what to the server's log, which is where an
// operator looks while the trail is warm. This is the same record kept in
// the database, so it can be browsed in the panel itself -- weeks later,
// after the journal has rotated, and by an owner who was never going to
// read a journal in the first place.
//
// It is a record, not a second source of truth: nothing in the panel is
// decided from it, and losing a line to a failed write costs a line of
// history, never the action it described. Writes here are therefore
// best-effort and their errors are dropped on purpose.

// activityKeep is how many entries the table holds. At a line per change
// that is years of an ordinary panel's life, and the bound is what stops a
// busy one from growing the database without limit.
const activityKeep = 5000

// activityStore is the activity table's writer and reader.
type activityStore struct {
	db *gorm.DB
	// calls counts Record calls so the prune below runs once in a while
	// rather than on every write: counting the table to trim it would cost
	// more than the insert it follows.
	calls atomic.Uint64
}

// newActivityStore builds the store, or returns nil when there is no
// database -- tests build servers without one, and a panel with nowhere
// to keep the record simply keeps the log lines it already had.
func newActivityStore(db *gorm.DB) *activityStore {
	if db == nil {
		return nil
	}
	return &activityStore{db: db}
}

// Record keeps one entry. The prune rides along every 64th call: delete
// everything past the newest activityKeep rows.
func (a *activityStore) Record(e model.ActivityEntry) {
	if a == nil || a.db == nil {
		return
	}
	_ = a.db.Create(&e).Error
	if a.calls.Add(1)%64 == 0 {
		_ = a.db.Exec("DELETE FROM activity_entries WHERE id NOT IN "+
			"(SELECT id FROM activity_entries ORDER BY id DESC LIMIT ?)", activityKeep).Error
	}
}

// List reads entries newest first. beforeID pages backwards: only entries
// older than it are returned, so a page is asked for by the last id of the
// one before it and no offset ever has to be counted.
func (a *activityStore) List(limit int, beforeID uint) ([]model.ActivityEntry, error) {
	q := a.db.Order("id DESC").Limit(limit)
	if beforeID > 0 {
		q = q.Where("id < ?", beforeID)
	}
	var entries []model.ActivityEntry
	err := q.Find(&entries).Error
	return entries, err
}

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
