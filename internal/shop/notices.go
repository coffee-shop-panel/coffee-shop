package shop

import (
	"context"
	"fmt"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// The record of the reminders a shop's bot has sent its customers.
//
// The shop owns the table because the table is the dedupe: the bot's
// reminder scan asks, before it speaks, whether this reminder -- this
// kind, about this ending -- has already gone to this customer, and
// writes the answer down after it speaks. Keeping both acts here
// means the rule "once per occasion" lives in one place, tested
// without a bot on the other end of it, the way the rest of the shop
// is.

// NoticeSent reports whether a reminder of this kind, for this
// customer, under this stamp, has already been recorded as sent.
// The stamp is what makes a renewed plan a new occasion: the same
// kind under a new end date has not been sent, however many times
// the old date's reminder went out.
func (s *Shop) NoticeSent(ctx context.Context, operatorID, clientID uint, kind, stamp string) (bool, error) {
	var n int64
	if err := s.db.WithContext(ctx).Model(&model.ShopNotice{}).
		Where("operator_id = ? AND client_id = ? AND kind = ? AND stamp = ?",
			operatorID, clientID, kind, stamp).
		Count(&n).Error; err != nil {
		return false, fmt.Errorf("shop: read the notices: %w", err)
	}
	return n > 0, nil
}

// RecordNotice writes a reminder down as sent. Recording is
// idempotent by the table's own index, the way recording a code's
// use is: a scan that races itself, or a send retried after its
// recording failed, writes the row once and reads the refusal of
// the second as "already recorded", never as a second reminder.
func (s *Shop) RecordNotice(ctx context.Context, operatorID, clientID uint, kind, stamp string) error {
	row := &model.ShopNotice{
		OperatorID: operatorID, ClientID: clientID,
		Kind: kind, Stamp: stamp, SentAt: time.Now().UTC(),
	}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		if isUniqueViolation(err) {
			return nil
		}
		return fmt.Errorf("shop: record the notice: %w", err)
	}
	return nil
}
