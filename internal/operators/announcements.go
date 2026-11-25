package operators

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// Announcements are the owner's word to every operator on the panel,
// and each operator's dismissal of it.
//
// The store is plain in the preset store's way. The owner's half is
// three statements -- write one, read them all, take one back -- and
// the operators' half is a question and a mark: what has this
// operator not yet dismissed, and dismiss this one for them. The
// dismissal is per operator by construction: the read rows carry the
// operator's id, and the undismissed list is a join against the
// caller's own rows alone, so one reseller putting a banner away can
// never put it away for another.
type Announcements struct {
	db  *gorm.DB
	log *slog.Logger
}

// NewAnnouncements builds the announcement store, or returns nil
// when there is no database: a panel with nowhere to keep them
// simply has none, and its announcement endpoints answer as if the
// feature were not there.
func NewAnnouncements(db *gorm.DB, log *slog.Logger) *Announcements {
	if db == nil {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	return &Announcements{db: db, log: log}
}

// Create stores one announcement the owner has just published.
func (a *Announcements) Create(ctx context.Context, text string, createdBy uint) (*model.Announcement, error) {
	row := &model.Announcement{Text: text, CreatedBy: createdBy}
	if err := a.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, fmt.Errorf("operators: store announcement: %w", err)
	}
	return row, nil
}

// List is every announcement there is, newest first -- the owner's
// history of what they have told the room.
func (a *Announcements) List(ctx context.Context) ([]model.Announcement, error) {
	var rows []model.Announcement
	err := a.db.WithContext(ctx).Order("id DESC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("operators: list announcements: %w", err)
	}
	return rows, nil
}

// Delete takes an announcement back: the row goes, and every
// dismissal of it goes with it, so nothing is left pointing at a
// message that is no longer there. It reports whether there was
// one to take.
func (a *Announcements) Delete(ctx context.Context, id uint) (bool, error) {
	deleted := false
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Where("id = ?", id).Delete(&model.Announcement{})
		if res.Error != nil {
			return res.Error
		}
		deleted = res.RowsAffected > 0
		if !deleted {
			return nil
		}
		return tx.Where("announcement_id = ?", id).Delete(&model.AnnouncementRead{}).Error
	})
	if err != nil {
		return false, fmt.Errorf("operators: delete announcement: %w", err)
	}
	return deleted, nil
}

// ListForOperator is the announcements one operator has not yet
// dismissed, newest first: the banners standing over their
// dashboard. The join is fenced to the caller's own dismissal rows
// -- an announcement another operator dismissed is undismissed here
// until this one dismisses it too.
func (a *Announcements) ListForOperator(ctx context.Context, operatorID uint) ([]model.Announcement, error) {
	var rows []model.Announcement
	err := a.db.WithContext(ctx).Model(&model.Announcement{}).
		Joins("LEFT JOIN announcement_reads ON announcement_reads.announcement_id = announcements.id AND announcement_reads.operator_id = ?", operatorID).
		Where("announcement_reads.id IS NULL").
		Order("announcements.id DESC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("operators: list announcements for operator: %w", err)
	}
	return rows, nil
}

// Dismiss marks one announcement as read by one operator. It
// reports whether the announcement is there to dismiss: a dismissal
// of nothing is not written down, because a read row pointing at no
// announcement would be a lie in the table. Dismissing twice is the
// ordinary double-tap, not an error -- the row is found or created,
// and the second call changes nothing.
func (a *Announcements) Dismiss(ctx context.Context, operatorID, id uint) (bool, error) {
	db := a.db.WithContext(ctx)
	var one model.Announcement
	if err := db.First(&one, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("operators: read announcement: %w", err)
	}
	row := &model.AnnouncementRead{AnnouncementID: id, OperatorID: operatorID}
	if err := db.Where("announcement_id = ? AND operator_id = ?", id, operatorID).
		FirstOrCreate(row).Error; err != nil {
		return false, fmt.Errorf("operators: dismiss announcement: %w", err)
	}
	return true, nil
}
