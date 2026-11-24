package service

import (
	"log/slog"
	"sync/atomic"

	"gorm.io/gorm"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// The activity record: who did what on the panel, kept in the database so
// it can be browsed in the panel itself -- weeks later, after the journal
// has rotated, and by an owner who was never going to read a journal in
// the first place. The API layer's action log writes the same line to the
// server's log while the trail is warm; this is the kept copy.
//
// It is a record, not a second source of truth: nothing in the panel is
// decided from it, and losing a line to a failed write costs a line of
// history, never the action it described. Writes are therefore
// best-effort -- but a failed write is logged, not dropped in silence, so
// a record that stopped being kept is something the owner can notice.

// activityKeep is how many entries the table holds. At a line per change
// that is years of an ordinary panel's life, and the bound is what stops
// a busy one from growing the database without limit.
const activityKeep = 5000

// Activity is the activity table's writer and reader.
type Activity struct {
	db *gorm.DB
	// calls counts Record calls so the prune below runs once in a while
	// rather than on every write: counting the table to trim it would
	// cost more than the insert it follows.
	calls atomic.Uint64
	log   *slog.Logger
}

// NewActivity builds the store, or returns nil when there is no database:
// a panel with nowhere to keep the record simply keeps the log lines it
// already had.
func NewActivity(db *gorm.DB, log *slog.Logger) *Activity {
	if db == nil {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	return &Activity{db: db, log: log}
}

// Record keeps one entry. The prune rides along every 64th call: delete
// everything past the newest activityKeep rows.
func (a *Activity) Record(e model.ActivityEntry) {
	if a == nil || a.db == nil {
		return
	}
	if err := a.db.Create(&e).Error; err != nil {
		a.log.Warn("activity: keeping an entry failed", "error", err)
	}
	if a.calls.Add(1)%64 == 0 {
		if err := a.db.Exec("DELETE FROM activity_entries WHERE id NOT IN "+
			"(SELECT id FROM activity_entries ORDER BY id DESC LIMIT ?)", activityKeep).Error; err != nil {
			a.log.Warn("activity: trimming the record failed", "error", err)
		}
	}
}

// List reads entries newest first. beforeID pages backwards: only entries
// older than it are returned, so a page is asked for by the last id of
// the one before it and no offset ever has to be counted.
func (a *Activity) List(limit int, beforeID uint) ([]model.ActivityEntry, error) {
	q := a.db.Order("id DESC").Limit(limit)
	if beforeID > 0 {
		q = q.Where("id < ?", beforeID)
	}
	var entries []model.ActivityEntry
	err := q.Find(&entries).Error
	return entries, err
}
