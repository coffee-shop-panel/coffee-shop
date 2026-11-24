package shop

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/fault"
	"github.com/abolfazl/w-ui/internal/service"
)

// The wheel of fortune: a daily turn a shop offers its buyers, for
// the small prizes that bring a buyer back -- a discount code for
// their next purchase, or volume added to the plan they hold.
//
// The wheel is the shop's, not the bot's, because everything it
// hands out is the shop's own goods: a code is a row in the codes
// table, volume is the customers service's Adjust, and the once-a-
// day fence is a column on the buyer's wallet. The bot only spins
// and says what came up.

// Prize kinds, as SpinWheel reports them.
const (
	PrizeNone   = "none"
	PrizeCode   = "code"
	PrizeVolume = "volume"
)

// Prize is what one spin won. Only the fields of its kind are set:
// a code prize carries the code's text and its percent, a volume
// prize the bytes added. Label is left for the caller -- what a
// prize is called is the bot's language, not the shop's.
type Prize struct {
	Kind    string
	Percent int
	Code    string
	Bytes   uint64
	Label   string
}

// ErrWheelTooSoon is a spin refused because the buyer spun within
// the last day. It is its own error, checkable with errors.Is,
// because the bot answers it differently from every other refusal:
// not "something broke", but "come back at this hour".
var ErrWheelTooSoon = errors.New("shop: the wheel was spun within the last day")

// wheelRoll draws the spin, out of 100. A variable so tests can
// stand the wheel on the band they mean to exercise; production
// never changes it.
var wheelRoll = func() int {
	n, err := rand.Int(rand.Reader, big.NewInt(100))
	if err != nil {
		// A prize draw is not a secret; a clock-drawn number is
		// nearly as fair, and a failed entropy read must not cost
		// the buyer their one spin of the day.
		return int(time.Now().UnixNano() % 100)
	}
	return int(n.Int64())
}

// SpinWheel turns the wheel once for a buyer: the prize it lands
// on, delivered before it is handed back -- a code prize is a code
// that already exists and can be spent at checkout, a volume prize
// is already on the customer's plan.
//
// One spin a day, fenced the way the trial's once is fenced: the
// claim is the wallet's conditional update, so two spins racing
// cannot both be the day's one. A spin that breaks while its prize
// is being made gives the claim back -- the buyer has not had their
// turn -- and the claim is kept only by a spin that completes,
// prize or no prize: landing on nothing is still the day's turn.
func (s *Shop) SpinWheel(ctx context.Context, operatorID uint, tgID int64) (*Prize, error) {
	cfg, err := s.ConfigFor(ctx, operatorID)
	if err != nil {
		return nil, err
	}
	if !cfg.WheelEnabled {
		return nil, fmt.Errorf("%w: this shop's wheel is switched off", fault.ErrInvalid)
	}
	w, err := s.WalletFor(ctx, operatorID, tgID)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	claim := s.db.WithContext(ctx).Model(&model.ShopWallet{}).
		Where("id = ? AND (last_spin_at IS NULL OR last_spin_at <= ?)",
			w.ID, now.Add(-24*time.Hour)).
		UpdateColumn("last_spin_at", now)
	if claim.Error != nil {
		return nil, fmt.Errorf("shop: claim the spin: %w", claim.Error)
	}
	if claim.RowsAffected == 0 {
		return nil, ErrWheelTooSoon
	}
	unclaim := func() {
		_ = s.db.WithContext(ctx).Model(&model.ShopWallet{}).
			Where("id = ?", w.ID).
			UpdateColumn("last_spin_at", w.LastSpinAt).Error
	}

	prize, err := s.drawPrize(ctx, cfg, operatorID, tgID, now)
	if err != nil {
		unclaim()
		return nil, err
	}
	s.log.Info("shop wheel spun",
		"operator", operatorID, "buyer", tgID, "prize", prize.Kind)
	return prize, nil
}

// drawPrize works out what a spin landed on and delivers it. The
// bands are the wheel's weights, out of the roll's hundred: most
// spins win nothing, codes grow rarer as they grow larger, and
// volume is the wheel's jackpot end.
func (s *Shop) drawPrize(ctx context.Context, cfg *Config, operatorID uint, tgID int64, now time.Time) (*Prize, error) {
	roll := wheelRoll()
	switch {
	case roll < 40:
		return &Prize{Kind: PrizeNone}, nil
	case roll < 60:
		return s.codePrize(ctx, operatorID, 5, now)
	case roll < 75:
		return s.codePrize(ctx, operatorID, 10, now)
	case roll < 83:
		return s.codePrize(ctx, operatorID, 15, now)
	case roll < 88:
		return s.codePrize(ctx, operatorID, 20, now)
	case roll < 95:
		return s.volumePrize(ctx, operatorID, tgID, 1<<30, now)
	default:
		return s.volumePrize(ctx, operatorID, tgID, 3<<30, now)
	}
}

// codePrize makes a spin's discount code: a percent off, spendable
// once, alive for a week. The code is the shop's ordinary kind, so
// checkout prices it and delivery records its use exactly as it
// does a code the operator typed by hand.
func (s *Shop) codePrize(ctx context.Context, operatorID uint, percent int, now time.Time) (*Prize, error) {
	expires := now.Add(7 * 24 * time.Hour)
	for attempt := 0; attempt < 5; attempt++ {
		code := &Code{
			OperatorID: operatorID,
			Code:       "LUCKY-" + strings.ToUpper(randomNamePart(6)),
			Kind:       CodePercent,
			Value:      int64(percent),
			MaxUses:    1,
			ExpiresAt:  &expires,
			Enabled:    true,
		}
		if err := s.SaveCode(ctx, code); err != nil {
			// A drawn text this operator already has is redrawn;
			// anything else is a real failure of the spin.
			if strings.Contains(err.Error(), "already has that code") {
				continue
			}
			return nil, err
		}
		return &Prize{Kind: PrizeCode, Percent: percent, Code: code.Code}, nil
	}
	return nil, fmt.Errorf("%w: the wheel could not draw a fresh code", fault.ErrInvalid)
}

// volumePrize adds a spin's volume to the buyer's own plan: their
// newest customer still standing under this operator grows by the
// prize. A buyer with no such customer -- or only an unlimited one,
// which volume cannot grow -- is given a small code instead: a
// prize that lands nowhere is worse than a smaller one that lands.
func (s *Shop) volumePrize(ctx context.Context, operatorID uint, tgID int64, bytes uint64, now time.Time) (*Prize, error) {
	opCtx, err := s.OperatorCtx(ctx, operatorID)
	if err != nil {
		return nil, err
	}
	customers, err := s.clients.ByTelegramID(opCtx, tgID)
	if err != nil {
		return nil, err
	}
	var newest *model.Client
	for i := range customers {
		c := &customers[i]
		if c.Status != model.StatusActive || c.QuotaBytes == 0 {
			continue
		}
		if newest == nil || c.ID > newest.ID {
			newest = c
		}
	}
	if newest == nil {
		return s.codePrize(ctx, operatorID, 5, now)
	}
	grown := newest.QuotaBytes + bytes
	if _, err := s.clients.Adjust(opCtx, service.AdjustInput{
		IDs: []uint{newest.ID}, QuotaBytes: &grown,
	}); err != nil {
		return nil, err
	}
	return &Prize{Kind: PrizeVolume, Bytes: bytes}, nil
}
