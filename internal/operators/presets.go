package operators

import (
	"context"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// ClientPresets are an operator's saved shapes for the new-customer
// form: the plans they sell often, kept one click from the next sale.
//
// The store is plain in the passkey store's way. Every read and write is
// scoped to one operator, because a preset is a seller's own price list:
// a list that reached across operators would show one reseller what
// another charges for a plan, and a delete that did the same would
// reach into a book that is not the caller's to keep.
type ClientPresets struct {
	db  *gorm.DB
	log *slog.Logger
}

// NewClientPresets builds the preset store, or returns nil when there
// is no database: a panel with nowhere to keep them simply has none,
// and its preset endpoints answer as if the feature were not there.
func NewClientPresets(db *gorm.DB, log *slog.Logger) *ClientPresets {
	if db == nil {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	return &ClientPresets{db: db, log: log}
}

// List is every preset one operator holds, oldest first -- the order
// they were saved in, which is the order the form offers them in.
func (p *ClientPresets) List(ctx context.Context, adminID uint) ([]model.ClientPreset, error) {
	var rows []model.ClientPreset
	err := p.db.WithContext(ctx).Where("admin_id = ?", adminID).
		Order("id ASC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("operators: list client presets: %w", err)
	}
	return rows, nil
}

// Create stores a preset the operator has just shaped in the form.
func (p *ClientPresets) Create(ctx context.Context, row *model.ClientPreset) error {
	if err := p.db.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("operators: store client preset: %w", err)
	}
	return nil
}

// Delete removes one of the operator's own presets. The scope is in the
// statement, so asking for another operator's preset removes nothing
// and reports false -- the caller answers as if it were never there,
// which to this operator it may as well not be.
func (p *ClientPresets) Delete(ctx context.Context, adminID, id uint) (bool, error) {
	res := p.db.WithContext(ctx).
		Where("id = ? AND admin_id = ?", id, adminID).
		Delete(&model.ClientPreset{})
	if res.Error != nil {
		return false, fmt.Errorf("operators: delete client preset: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}
