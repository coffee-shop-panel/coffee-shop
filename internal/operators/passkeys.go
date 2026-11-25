package operators

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/fault"
)

// Passkeys are an operator's WebAuthn credentials: the public halves of
// the keys on their phone and laptop, kept so a fingerprint can stand in
// for the password at sign-in.
//
// The store is deliberately plain. Every read and write is scoped to one
// operator, because a passkey is a way into an account: a list that
// reached across operators would hand one reseller the shape of
// another's sign-in, and a delete that did the same would lock them out
// of their own panel.
type Passkeys struct {
	db  *gorm.DB
	log *slog.Logger
}

// NewPasskeys builds the passkey store, or returns nil when there is no
// database: a panel with nowhere to keep the keys simply has none, and
// its passkey endpoints answer as if the feature were not there.
func NewPasskeys(db *gorm.DB, log *slog.Logger) *Passkeys {
	if db == nil {
		return nil
	}
	if log == nil {
		log = slog.Default()
	}
	return &Passkeys{db: db, log: log}
}

// List is every passkey one operator holds, oldest first -- the order
// they were registered in, which is the order the settings list shows.
func (p *Passkeys) List(ctx context.Context, adminID uint) ([]model.PasskeyCredential, error) {
	var rows []model.PasskeyCredential
	err := p.db.WithContext(ctx).Where("admin_id = ?", adminID).
		Order("id ASC").Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("operators: list passkeys: %w", err)
	}
	return rows, nil
}

// Create stores a passkey a ceremony has just proved. The credential id
// is unique across the panel, so registering the same authenticator
// twice -- for two operators, or twice for one -- is refused by the
// index rather than leaving two rows that both answer to one key.
func (p *Passkeys) Create(ctx context.Context, row *model.PasskeyCredential) error {
	if err := p.db.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("operators: store passkey: %w", err)
	}
	return nil
}

// ByCredentialID is the passkey an assertion names, fault.ErrNotFound
// when no operator holds it. This is the lookup a sign-in begins with:
// the credential says who is at the door before any username is spoken.
func (p *Passkeys) ByCredentialID(ctx context.Context, credentialID []byte) (*model.PasskeyCredential, error) {
	var row model.PasskeyCredential
	err := p.db.WithContext(ctx).Where("credential_id = ?", credentialID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fault.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("operators: load passkey: %w", err)
	}
	return &row, nil
}

// Delete removes one of the operator's own passkeys. The scope is in the
// statement, so asking for another operator's passkey removes nothing
// and reports false -- the caller answers as if it were never there,
// which to this operator it may as well not be.
func (p *Passkeys) Delete(ctx context.Context, adminID, id uint) (bool, error) {
	res := p.db.WithContext(ctx).
		Where("id = ? AND admin_id = ?", id, adminID).
		Delete(&model.PasskeyCredential{})
	if res.Error != nil {
		return false, fmt.Errorf("operators: delete passkey: %w", res.Error)
	}
	return res.RowsAffected > 0, nil
}

// Used records a sign-in: the authenticator's new counter, which the
// next assertion is compared against, and when it happened.
func (p *Passkeys) Used(ctx context.Context, id uint, signCount uint32, at time.Time) error {
	err := p.db.WithContext(ctx).Model(&model.PasskeyCredential{}).
		Where("id = ?", id).
		Updates(map[string]any{"sign_count": signCount, "last_used_at": at.UTC()}).Error
	if err != nil {
		return fmt.Errorf("operators: record passkey use: %w", err)
	}
	return nil
}
