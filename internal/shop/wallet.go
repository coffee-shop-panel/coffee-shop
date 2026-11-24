package shop

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/fault"
	"github.com/abolfazl/w-ui/internal/service"
)

// The buyer's wallet at one shop, and the doors that hang off it: who
// referred them, and the one free trial they may claim.
//
// Money here is a shop's promise, not a bank's: the balance is what
// this operator owes this buyer, spendable only in this shop. Every
// movement is a single atomic column update against the row, so two
// movements landing together -- a cashback and a redemption racing --
// cannot read the same balance and write back two different answers.

// walletRow reads a buyer's wallet without making one: the callers
// that only ask (has this buyer a referrer? have they had their
// trial?) should not conjure a row for a stranger by asking.
func (s *Shop) walletRow(ctx context.Context, operatorID uint, tgID int64) (*model.ShopWallet, bool, error) {
	var w model.ShopWallet
	err := s.db.WithContext(ctx).
		Where("operator_id = ? AND buyer_tg_id = ?", operatorID, tgID).
		First(&w).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("shop: read wallet: %w", err)
	}
	return &w, true, nil
}

// WalletFor reads a buyer's wallet, opening one -- empty, unreferred,
// trial unclaimed -- when they have none yet. A wallet is opened by
// looking at it because everything a wallet records starts empty, and
// a caller who had to create before reading would only ever write
// this same row first.
func (s *Shop) WalletFor(ctx context.Context, operatorID uint, tgID int64) (*model.ShopWallet, error) {
	w, ok, err := s.walletRow(ctx, operatorID, tgID)
	if err != nil {
		return nil, err
	}
	if ok {
		return w, nil
	}
	w = &model.ShopWallet{OperatorID: operatorID, BuyerTGID: tgID}
	if err := s.db.WithContext(ctx).Create(w).Error; err != nil {
		// Two openings at once: the unique index refuses the second,
		// and the row the first one wrote is the answer for both.
		again, ok, rerr := s.walletRow(ctx, operatorID, tgID)
		if rerr == nil && ok {
			return again, nil
		}
		return nil, fmt.Errorf("shop: open wallet: %w", err)
	}
	return w, nil
}

// CreditWallet puts amount Toman in a buyer's wallet: a top-up
// delivered, a gift code redeemed, a referrer's cashback. The row is
// opened first if there is none -- money owed to a buyer with no
// wallet yet is still owed.
func (s *Shop) CreditWallet(ctx context.Context, operatorID uint, tgID int64, amount int64) error {
	if amount <= 0 {
		return fault.Field("amount", "a credit must be a positive amount")
	}
	if _, err := s.WalletFor(ctx, operatorID, tgID); err != nil {
		return err
	}
	res := s.db.WithContext(ctx).Model(&model.ShopWallet{}).
		Where("operator_id = ? AND buyer_tg_id = ?", operatorID, tgID).
		UpdateColumn("balance_toman", gorm.Expr("balance_toman + ?", amount))
	if res.Error != nil {
		return fmt.Errorf("shop: credit wallet: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: wallet of buyer %d at operator %d", fault.ErrNotFound, tgID, operatorID)
	}
	return nil
}

// DebitWallet takes amount Toman out of a buyer's wallet, refusing --
// and moving nothing -- when the balance does not cover it. The
// refusal is in the update itself (the WHERE carries the balance), so
// the check and the taking are one act and cannot be raced apart.
func (s *Shop) DebitWallet(ctx context.Context, operatorID uint, tgID int64, amount int64) error {
	if amount <= 0 {
		return fault.Field("amount", "a debit must be a positive amount")
	}
	if _, err := s.WalletFor(ctx, operatorID, tgID); err != nil {
		return err
	}
	res := s.db.WithContext(ctx).Model(&model.ShopWallet{}).
		Where("operator_id = ? AND buyer_tg_id = ? AND balance_toman >= ?",
			operatorID, tgID, amount).
		UpdateColumn("balance_toman", gorm.Expr("balance_toman - ?", amount))
	if res.Error != nil {
		return fmt.Errorf("shop: debit wallet: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fault.Field("amount", "the wallet does not hold that much")
	}
	return nil
}

// BindReferral records who brought a buyer to the shop, the first
// time anyone says: a wallet already bound keeps its referrer, a
// buyer cannot refer themselves, and a referrer who is not themselves
// a participant in this shop -- no wallet here, no delivered order
// here -- binds nobody. Every refusal is a quiet no-op rather than an
// error: a referral link is something a buyer arrives holding, and
// their arrival must never fail because the link was stale, forged or
// their own.
func (s *Shop) BindReferral(ctx context.Context, operatorID uint, tgID int64, referrerTGID int64) error {
	if referrerTGID == 0 || referrerTGID == tgID {
		return nil
	}
	if _, ok, err := s.walletRow(ctx, operatorID, referrerTGID); err != nil {
		return err
	} else if !ok {
		var orders int64
		if err := s.db.WithContext(ctx).Model(&model.ShopOrder{}).
			Where("operator_id = ? AND buyer_tg_id = ? AND status = ?",
				operatorID, referrerTGID, StatusFulfilled).
			Count(&orders).Error; err != nil {
			return fmt.Errorf("shop: check the referrer: %w", err)
		}
		if orders == 0 {
			return nil
		}
	}
	w, err := s.WalletFor(ctx, operatorID, tgID)
	if err != nil {
		return err
	}
	// Only a wallet with nobody named yet takes a name: the condition
	// is in the update, so two first arrivals racing cannot overwrite
	// each other's referrer with the second one's.
	res := s.db.WithContext(ctx).Model(&model.ShopWallet{}).
		Where("id = ? AND referrer_tg_id = 0", w.ID).
		UpdateColumn("referrer_tg_id", referrerTGID)
	if res.Error != nil {
		return fmt.Errorf("shop: bind referral: %w", res.Error)
	}
	return nil
}

// ReferralCount is how many buyers this shop's records say a buyer
// brought with them: the wallets at this operator naming them as
// their referrer. It is the number the invitation screen shows --
// who arrived because of this buyer, whether or not they have bought
// yet -- so it counts bindings, not sales.
func (s *Shop) ReferralCount(ctx context.Context, operatorID uint, tgID int64) (int, error) {
	var n int64
	if err := s.db.WithContext(ctx).Model(&model.ShopWallet{}).
		Where("operator_id = ? AND referrer_tg_id = ?", operatorID, tgID).
		Count(&n).Error; err != nil {
		return 0, fmt.Errorf("shop: count a buyer's referrals: %w", err)
	}
	return int(n), nil
}

// maxNameRunes is the customer name's ceiling, the panel's own:
// service.Create refuses a longer name (its column is sized the same),
// and a formatted name that ran past it would fail the sale it was
// made for.
const maxNameRunes = 128

// capClientName trims a made name to what a customer may be called:
// no control characters (a name is carried into logs, exports and
// Telegram messages, all of which read lines), no longer than the
// panel's own ceiling.
func capClientName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxNameRunes {
		name = string([]rune(name)[:maxNameRunes])
		name = strings.TrimSpace(name)
	}
	return name
}

const nameRandAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// randomNamePart draws n letters and digits for a {rand} placeholder:
// a name made of them tells two customers of one buyer apart without
// saying anything about either.
func randomNamePart(n int) string {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		// A name is not a secret; a clock-drawn part tells customers
		// apart nearly as well, and a failed entropy read must not
		// fail the sale the name is for.
		seed := time.Now().UnixNano()
		for i := range raw {
			raw[i] = byte(seed >> (8 * (i % 8)))
		}
	}
	out := make([]byte, n)
	for i, b := range raw {
		out[i] = nameRandAlphabet[int(b)%len(nameRandAlphabet)]
	}
	return string(out)
}

// formatClientName makes a customer's name for a sale: the operator's
// format when they have set one, the buyer's own name when they have
// not -- which is what every sale did before formats existed.
//
// The placeholders are the buyer's Telegram display name ({name}),
// their Telegram id ({id}), a short random part ({rand}), and where
// the customer stands in the operator's book ({n}: the operator's
// customer count, plus one, worked out by the caller, who knows the
// moment the count is true of). A buyer with no display name is their
// TG-<id>, as an unnamed sale's customer has always been called.
func formatClientName(cfg *Config, buyerName string, tgID int64, counter int) string {
	name := strings.TrimSpace(buyerName)
	if name == "" {
		name = fmt.Sprintf("TG-%d", tgID)
	}
	format := ""
	if cfg != nil {
		format = strings.TrimSpace(cfg.NameFormat)
	}
	if format == "" {
		return capClientName(name)
	}
	out := strings.NewReplacer(
		"{name}", name,
		"{id}", strconv.FormatInt(tgID, 10),
		"{rand}", randomNamePart(4),
		"{n}", strconv.Itoa(counter),
	).Replace(format)
	if strings.TrimSpace(out) == "" {
		out = name
	}
	return capClientName(out)
}

// clientCounter is where an operator's next customer stands in their
// book: how many customers the operator's scope already holds, plus
// one. Read through the operator's own context, so a reseller's count
// is of their own customers and nobody else's.
func (s *Shop) clientCounter(ctx context.Context, opCtx context.Context) int {
	page, err := s.clients.List(opCtx, service.ListFilter{})
	if err != nil || page == nil {
		return 1
	}
	return int(page.Total) + 1
}

// payCashback lands a delivered sale's cashback in the wallet of the
// buyer who referred its purchaser: the shop's configured percent of
// the order's price.
//
// The convention, stated once and kept everywhere: an order's
// PriceToman is the final total payable for the whole order -- net of
// any discount, all units included -- so it is taken exactly as it
// stands. DiscountToman rides on the order as information (what the
// code took off) and must NOT be subtracted again here, and Quantity
// must NOT multiply again: either one would pay cashback on money
// that was never the sale's price.
//
// It is called by Fulfill's settle, in the one call that turned the
// order fulfilled, which is what makes it land once. A top-up earns
// nothing (it is money moving, not a sale), a trial never reaches
// here, and a buyer nobody referred earns nobody anything. Failures
// are the caller's to log: the sale has already been delivered, and
// cashback is bookkeeping behind it.
func (s *Shop) payCashback(ctx context.Context, cfg *Config, order *Order) {
	if cfg == nil || cfg.CashbackPercent <= 0 || order.PriceToman <= 0 {
		return
	}
	if order.Kind != KindNew && order.Kind != KindRenew {
		return
	}
	switch order.Method {
	case MethodCard, MethodZarinpal, MethodWallet:
	default:
		return
	}
	w, ok, err := s.walletRow(ctx, order.OperatorID, order.BuyerTGID)
	if err != nil {
		s.log.Warn("shop: read the buyer's wallet for cashback",
			"order", order.ID, "error", err)
		return
	}
	if !ok || w.ReferrerTGID == 0 {
		return
	}
	// The price as it stands (see the convention above): no second
	// discount, no second quantity.
	amount := order.PriceToman * int64(cfg.CashbackPercent) / 100
	if amount <= 0 {
		return
	}
	if err := s.CreditWallet(ctx, order.OperatorID, w.ReferrerTGID, amount); err != nil {
		s.log.Warn("shop: pay cashback", "order", order.ID, "error", err)
	}
}

// ClaimTrial gives a buyer this shop's free trial: a small customer of
// theirs, on the trial section's tunnels, for the trial's allowance
// and length -- once per buyer per shop, ever.
//
// The once is claimed before the customer is made (the wallet's
// conditional update is the claim), and given back if making the
// customer fails: a buyer whose trial delivery broke has not had a
// trial, and one whose succeeded cannot have a second whatever order
// the two acts are retried in.
func (s *Shop) ClaimTrial(ctx context.Context, operatorID uint, tgID int64, buyerName string) (*model.Client, error) {
	cfg, err := s.ConfigFor(ctx, operatorID)
	if err != nil {
		return nil, err
	}
	if !cfg.TrialEnabled {
		return nil, fmt.Errorf("%w: this shop offers no trial", fault.ErrInvalid)
	}
	w, err := s.WalletFor(ctx, operatorID, tgID)
	if err != nil {
		return nil, err
	}
	if w.TrialUsedAt != nil {
		return nil, fmt.Errorf("%w: this buyer has already had this shop's trial", fault.ErrInvalid)
	}

	opCtx, err := s.OperatorCtx(ctx, operatorID)
	if err != nil {
		return nil, err
	}
	sectionIDs, err := s.SectionInterfaces(ctx, cfg, cfg.TrialSection)
	if err != nil {
		return nil, err
	}
	if len(sectionIDs) == 0 {
		return nil, fmt.Errorf("%w: the shop has no %s tunnel to give a trial on",
			fault.ErrInvalid, NormalizeSection(cfg.TrialSection))
	}

	now := time.Now().UTC()
	claim := s.db.WithContext(ctx).Model(&model.ShopWallet{}).
		Where("id = ? AND trial_used_at IS NULL", w.ID).
		UpdateColumn("trial_used_at", now)
	if claim.Error != nil {
		return nil, fmt.Errorf("shop: claim the trial: %w", claim.Error)
	}
	if claim.RowsAffected == 0 {
		return nil, fmt.Errorf("%w: this buyer has already had this shop's trial", fault.ErrInvalid)
	}
	unclaim := func() {
		_ = s.db.WithContext(ctx).Model(&model.ShopWallet{}).
			Where("id = ?", w.ID).
			UpdateColumn("trial_used_at", nil).Error
	}

	in := service.CreateInput{
		Name:         formatClientName(cfg, buyerName, tgID, s.clientCounter(ctx, opCtx)),
		InterfaceIDs: sectionIDs,
		QuotaBytes:   cfg.TrialBytes,
		DeviceLimit:  0,
		TelegramID:   tgID,
	}
	if cfg.TrialDays > 0 {
		expires := now.Add(time.Duration(cfg.TrialDays) * 24 * time.Hour)
		in.ExpiresAt = &expires
	}
	client, err := s.clients.Create(opCtx, in)
	if err != nil {
		unclaim()
		return nil, err
	}
	s.log.Info("shop trial claimed",
		"operator", operatorID, "buyer", tgID, "client", client.ID)
	return client, nil
}
