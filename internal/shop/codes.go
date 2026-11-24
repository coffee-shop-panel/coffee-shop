package shop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/fault"
)

// The shop's codes: the discounts a buyer names at checkout, and the
// gift codes they redeem into their wallet.
//
// A code is spent in two acts that must not be confused. Pricing asks
// what a code would do to a price and records nothing -- a buyer who
// prices a plan and never pays has used nothing. The use is written
// down only when the money has moved: at delivery for a checkout code
// (RecordCodeUse, called by Fulfill), and at the redemption itself for
// a gift. In both places the uses table's unique index is the guard:
// one use per buyer per code is a fact of the schema, so recording the
// same use twice is refused by the database and read as "already
// recorded", never as a second spend.

// Code is one of an operator's discount or gift codes, mirroring its
// row the way Plan mirrors a plan.
type Code struct {
	ID         uint
	OperatorID uint
	Code       string
	Kind       string
	Value      int64
	MaxUses    int
	UsedCount  int
	ExpiresAt  *time.Time
	Enabled    bool
}

func codeFromRow(r *model.ShopCode) *Code {
	return &Code{
		ID: r.ID, OperatorID: r.OperatorID, Code: r.Code, Kind: r.Kind,
		Value: r.Value, MaxUses: r.MaxUses, UsedCount: r.UsedCount,
		ExpiresAt: r.ExpiresAt, Enabled: r.Enabled,
	}
}

func (c *Code) row() *model.ShopCode {
	return &model.ShopCode{
		ID: c.ID, OperatorID: c.OperatorID, Code: c.Code, Kind: c.Kind,
		Value: c.Value, MaxUses: c.MaxUses, UsedCount: c.UsedCount,
		ExpiresAt: c.ExpiresAt, Enabled: c.Enabled,
	}
}

// normalizeCode is a code as it is stored and looked up: trimmed, and
// in the one case, so a code the operator wrote as "welcome10" is the
// code a buyer spends by typing WELCOME10 into their phone.
func normalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// CodeFor reads one of an operator's codes by its text. A code the
// operator does not have -- never made, or another seller's -- is
// ErrNotFound: from a buyer's side the two are the same sentence, and
// from the operator's page the code simply is not theirs to read.
func (s *Shop) CodeFor(ctx context.Context, operatorID uint, code string) (*Code, error) {
	var row model.ShopCode
	err := s.db.WithContext(ctx).
		Where("operator_id = ? AND code = ?", operatorID, normalizeCode(code)).
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%w: code %q", fault.ErrNotFound, normalizeCode(code))
	}
	if err != nil {
		return nil, fmt.Errorf("shop: read code: %w", err)
	}
	return codeFromRow(&row), nil
}

// CodesFor lists an operator's codes, oldest first: the order they
// were made in is the order the operator thinks of them in, and
// nothing here is offered to a buyer for the list to need a selling
// order.
func (s *Shop) CodesFor(ctx context.Context, operatorID uint) ([]Code, error) {
	var rows []model.ShopCode
	if err := s.db.WithContext(ctx).
		Where("operator_id = ?", operatorID).Order("id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("shop: read codes: %w", err)
	}
	out := make([]Code, 0, len(rows))
	for i := range rows {
		out = append(out, *codeFromRow(&rows[i]))
	}
	return out, nil
}

// SaveCode writes a code: a new one when its ID is zero, the operator's
// own existing one otherwise -- with the same cross-operator refusal
// as SavePlan, and the same reasoning: an id is the one thing a caller
// could get wrong, or be made to send, across the line between
// sellers.
func (s *Shop) SaveCode(ctx context.Context, c *Code) error {
	if c.OperatorID == 0 {
		return fault.Field("operatorId", "a code belongs to an operator")
	}
	c.Code = normalizeCode(c.Code)
	if c.Code == "" {
		return fault.Field("code", "a code's text is required")
	}
	switch c.Kind {
	case CodePercent:
		if c.Value < 1 || c.Value > 100 {
			return fault.Field("value", "a percent code takes between 1 and 100 percent")
		}
	case CodeFixed, CodeGift:
		if c.Value <= 0 {
			return fault.Field("value", "the code's amount must be positive")
		}
	default:
		return fault.Field("kind", "a code is percent, fixed or gift")
	}
	if c.MaxUses < 0 {
		return fault.Field("maxUses", "a use ceiling cannot be negative")
	}
	db := s.db.WithContext(ctx)
	// One text per operator: the schema's unique index would refuse a
	// second, and the refusal is made here, in words, rather than
	// surfaced as a database error.
	var clash model.ShopCode
	err := db.Where("operator_id = ? AND code = ? AND id <> ?",
		c.OperatorID, c.Code, c.ID).First(&clash).Error
	if err == nil {
		return fault.Field("code", "this operator already has that code")
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("shop: check the code: %w", err)
	}
	if c.ID == 0 {
		row := c.row()
		if err := db.Create(row).Error; err != nil {
			return fmt.Errorf("shop: save code: %w", err)
		}
		c.ID = row.ID
		c.UsedCount = row.UsedCount
		return nil
	}
	var existing model.ShopCode
	if err := db.First(&existing, c.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: code %d", fault.ErrNotFound, c.ID)
		}
		return fmt.Errorf("shop: read code: %w", err)
	}
	if existing.OperatorID != c.OperatorID {
		return fault.Field("operatorId", "that code belongs to another operator")
	}
	row := c.row()
	row.CreatedAt = existing.CreatedAt
	// The count of uses is the uses' own record, not a field a save
	// may rewrite: an edit that reset it would hand spent uses back.
	row.UsedCount = existing.UsedCount
	if err := db.Save(row).Error; err != nil {
		return fmt.Errorf("shop: save code: %w", err)
	}
	c.UsedCount = existing.UsedCount
	return nil
}

// DeleteCode removes one of the operator's codes, as DeletePlan does a
// plan: orders and uses already written keep what they recorded, so
// deleting a code a sale is resting on does not rewrite the sale.
func (s *Shop) DeleteCode(ctx context.Context, operatorID, codeID uint) error {
	res := s.db.WithContext(ctx).
		Where("id = ? AND operator_id = ?", codeID, operatorID).
		Delete(&model.ShopCode{})
	if res.Error != nil {
		return fmt.Errorf("shop: delete code: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: code %d", fault.ErrNotFound, codeID)
	}
	return nil
}

// liveCode reads a code for spending and refuses every way it cannot
// be spent, in words a buyer can be shown: switched off, past its
// date, its ceiling of uses reached, or already spent by this buyer.
// What it does not refuse is the code's kind -- what each kind may be
// spent on is the caller's sentence to finish.
func (s *Shop) liveCode(ctx context.Context, operatorID uint, tgID int64, code string) (*Code, error) {
	c, err := s.CodeFor(ctx, operatorID, code)
	if err != nil {
		return nil, err
	}
	if !c.Enabled {
		return nil, fault.Field("code", "that code is not in use")
	}
	if c.ExpiresAt != nil && !time.Now().UTC().Before(*c.ExpiresAt) {
		return nil, fault.Field("code", "that code has expired")
	}
	if c.MaxUses > 0 && c.UsedCount >= c.MaxUses {
		return nil, fault.Field("code", "that code has been used up")
	}
	var used int64
	if err := s.db.WithContext(ctx).Model(&model.ShopCodeUse{}).
		Where("code_id = ? AND buyer_tg_id = ?", c.ID, tgID).
		Count(&used).Error; err != nil {
		return nil, fmt.Errorf("shop: check the code's uses: %w", err)
	}
	if used > 0 {
		return nil, fault.Field("code", "you have already used that code")
	}
	return c, nil
}

// PriceWithCode works out what a checkout code does to a price: the
// price after it, and what it took off. A percent code takes its
// percent of the price; a fixed code takes its amount, and never more
// than the price -- a price cannot go below nothing. A gift code is
// refused: it is not a discount, and pricing a plan with one would
// promise money off that the code never offered.
//
// Nothing is recorded: pricing is a question, and a buyer who asks it
// and never pays has spent nothing.
func (s *Shop) PriceWithCode(ctx context.Context, operatorID uint, tgID int64, code string, price int64) (int64, int64, error) {
	if price < 0 {
		return 0, 0, fault.Field("price", "a price cannot be negative")
	}
	c, err := s.liveCode(ctx, operatorID, tgID, code)
	if err != nil {
		return 0, 0, err
	}
	var discount int64
	switch c.Kind {
	case CodePercent:
		discount = price * c.Value / 100
	case CodeFixed:
		discount = c.Value
		if discount > price {
			discount = price
		}
	default:
		return 0, 0, fault.Field("code", "a gift code is redeemed for wallet money, not used at checkout")
	}
	final := price - discount
	if final < 0 {
		final = 0
	}
	return final, discount, nil
}

// RedeemGift spends a gift code into the redeemer's wallet: the
// code's amount lands as balance, the use is written down, and the
// amount credited is handed back so the caller can say what happened.
// The use is written first and the money second: a redemption the
// index refuses (this buyer, this code, once already) credits
// nothing, and a credit that fails has its use unwritten, so a buyer
// is never left having spent a code on money that never arrived.
func (s *Shop) RedeemGift(ctx context.Context, operatorID uint, tgID int64, code string) (int64, error) {
	c, err := s.liveCode(ctx, operatorID, tgID, code)
	if err != nil {
		return 0, err
	}
	if c.Kind != CodeGift {
		return 0, fault.Field("code", "that code is a checkout discount, not a gift")
	}
	if err := s.recordUse(ctx, c.ID, tgID, 0); err != nil {
		return 0, err
	}
	if err := s.CreditWallet(ctx, operatorID, tgID, c.Value); err != nil {
		_ = s.db.WithContext(ctx).
			Where("code_id = ? AND buyer_tg_id = ?", c.ID, tgID).
			Delete(&model.ShopCodeUse{}).Error
		_ = s.db.WithContext(ctx).Model(&model.ShopCode{}).
			Where("id = ? AND used_count > 0", c.ID).
			UpdateColumn("used_count", gorm.Expr("used_count - 1")).Error
		return 0, err
	}
	return c.Value, nil
}

// RecordCodeUse writes down a checkout code's use by an order's buyer
// and counts it on the code. It is the fulfill path's call, made when
// the order carrying the code is delivered. Recording is idempotent
// by the uses table's own index: a use already recorded -- a delivery
// retried, a button tapped twice -- is the answer, not an error, and
// the count is only ever moved by a recording that was new.
func (s *Shop) RecordCodeUse(ctx context.Context, codeID uint, tgID int64, orderID uint) error {
	return s.recordUse(ctx, codeID, tgID, orderID)
}

func (s *Shop) recordUse(ctx context.Context, codeID uint, tgID int64, orderID uint) error {
	row := &model.ShopCodeUse{CodeID: codeID, BuyerTGID: tgID, OrderID: orderID}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		if isUniqueViolation(err) {
			return nil
		}
		return fmt.Errorf("shop: record the code's use: %w", err)
	}
	if err := s.db.WithContext(ctx).Model(&model.ShopCode{}).
		Where("id = ?", codeID).
		UpdateColumn("used_count", gorm.Expr("used_count + 1")).Error; err != nil {
		return fmt.Errorf("shop: count the code's use: %w", err)
	}
	return nil
}

// isUniqueViolation reads a database refusal as the one it is: the
// words differ between the panel's two databases (SQLite's "UNIQUE
// constraint failed", PostgreSQL's "duplicate key value"), and both
// mean the row the caller tried to write already stands.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "duplicate key value")
}
