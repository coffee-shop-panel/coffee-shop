package shopbot

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/abolfazl/w-ui/internal/shop"
)

// The wheel of fortune's conversation: one tap, one spin, and the
// outcome said in the buyer's words. The wheel itself -- its
// weights, its prizes, its once-a-day fence -- is the shop's
// (internal/shop); this file only turns the tap and reports what
// came up.

// spinWheel is the wheel button's tap. A refusal the shop names --
// the day's spin already spent -- is answered with when the next
// turn comes, worked out from the wallet's own record; every other
// failure is the plain error, and the wheel being off (a stale menu
// from before the seller switched it off) is said as what it is.
func (b *bot) spinWheel(ctx context.Context, cb *tgCallback) {
	cfg := b.cfg()
	if !cfg.WheelEnabled {
		b.answer(ctx, cb, "")
		b.editOrSend(ctx, cb, txtWheelOff, b.menuKeyboard(ctx))
		return
	}
	prize, err := b.mgr.shop.SpinWheel(ctx, cfg.OperatorID, cb.From.ID)
	if err != nil {
		if errors.Is(err, shop.ErrWheelTooSoon) {
			b.answer(ctx, cb, "")
			b.editOrSend(ctx, cb,
				fmt.Sprintf(txtWheelWait, b.spinWaitText(ctx, cb.From.ID)),
				b.menuKeyboard(ctx))
			return
		}
		b.log.Warn("shop bot could not spin the wheel", "buyer", cb.From.ID, "error", err)
		b.answer(ctx, cb, "")
		b.editOrSend(ctx, cb, txtError, b.menuKeyboard(ctx))
		return
	}
	b.answer(ctx, cb, "")
	switch prize.Kind {
	case shop.PrizeCode:
		b.editOrSend(ctx, cb, fmt.Sprintf(txtWheelCode,
			faNum(int64(prize.Percent)), escapeHTML(prize.Code)), b.menuKeyboard(ctx))
	case shop.PrizeVolume:
		b.editOrSend(ctx, cb, fmt.Sprintf(txtWheelVolume,
			formatBytes(prize.Bytes)), b.menuKeyboard(ctx))
	default:
		b.editOrSend(ctx, cb, txtWheelNone, b.menuKeyboard(ctx))
	}
}

// spinWaitText says how long is left of the day's fence, in words:
// the hours and minutes until the wallet's last spin is a day old.
func (b *bot) spinWaitText(ctx context.Context, buyerID int64) string {
	w, err := b.mgr.shop.WalletFor(ctx, b.cfg().OperatorID, buyerID)
	if err != nil || w == nil || w.LastSpinAt == nil {
		return "۲۴ ساعت"
	}
	left := time.Until(w.LastSpinAt.Add(24 * time.Hour))
	if left < 0 {
		left = 0
	}
	hours := int(left.Hours())
	minutes := int(left.Minutes()) % 60
	if hours == 0 {
		return fmt.Sprintf("%s دقیقه", faNum(int64(minutes)))
	}
	if minutes == 0 {
		return fmt.Sprintf("%s ساعت", faNum(int64(hours)))
	}
	return fmt.Sprintf("%s ساعت و %s دقیقه", faNum(int64(hours)), faNum(int64(minutes)))
}
