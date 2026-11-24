package shop

import (
	"context"
	"testing"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
)

func saveCode(t *testing.T, f *fixture, c *Code) *Code {
	t.Helper()
	if err := f.shop.SaveCode(context.Background(), c); err != nil {
		t.Fatalf("SaveCode %+v: %v", c, err)
	}
	return c
}

func TestSaveCodeValidationAndScoping(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := addOperator(t, f.db, "a-seller", model.RoleReseller, 0)
	b := addOperator(t, f.db, "b-seller", model.RoleReseller, 0)

	// The value must mean what the kind says: a percent between 1 and
	// 100, a positive amount for the money kinds, a real kind at all.
	bad := []*Code{
		{OperatorID: a.ID, Code: "P0", Kind: CodePercent, Value: 0, Enabled: true},
		{OperatorID: a.ID, Code: "P101", Kind: CodePercent, Value: 101, Enabled: true},
		{OperatorID: a.ID, Code: "F0", Kind: CodeFixed, Value: 0, Enabled: true},
		{OperatorID: a.ID, Code: "G0", Kind: CodeGift, Value: -5, Enabled: true},
		{OperatorID: a.ID, Code: "XX", Kind: "bogus", Value: 5, Enabled: true},
		{OperatorID: a.ID, Code: "  ", Kind: CodeFixed, Value: 5, Enabled: true},
	}
	for _, c := range bad {
		if err := f.shop.SaveCode(ctx, c); err == nil {
			t.Errorf("SaveCode %+v was allowed, want a refusal", c)
		}
	}

	// Written untidily, stored in the one spelling, found by any.
	mine := saveCode(t, f, &Code{
		OperatorID: a.ID, Code: " sale ", Kind: CodePercent, Value: 10, Enabled: true,
	})
	if mine.Code != "SALE" {
		t.Errorf("the code was stored as %q, want SALE", mine.Code)
	}
	if got, err := f.shop.CodeFor(ctx, a.ID, "sale"); err != nil || got.ID != mine.ID {
		t.Errorf("CodeFor by a buyer's spelling = %+v, %v; want the code", got, err)
	}
	// The same text twice for one operator is refused; for another
	// operator it is their own code.
	if err := f.shop.SaveCode(ctx, &Code{
		OperatorID: a.ID, Code: "SALE", Kind: CodeFixed, Value: 5, Enabled: true,
	}); err == nil {
		t.Error("a second SALE for the same operator was allowed")
	}
	theirs := saveCode(t, f, &Code{
		OperatorID: b.ID, Code: "SALE", Kind: CodePercent, Value: 20, Enabled: true,
	})

	// One operator's code is not the other's to edit or delete.
	if err := f.shop.SaveCode(ctx, &Code{
		ID: mine.ID, OperatorID: b.ID, Code: "SALE", Kind: CodePercent, Value: 99, Enabled: true,
	}); err == nil {
		t.Error("operator B edited operator A's code")
	}
	if err := f.shop.DeleteCode(ctx, b.ID, mine.ID); err == nil {
		t.Error("operator B deleted operator A's code")
	}
	if got, err := f.shop.CodeFor(ctx, a.ID, "SALE"); err != nil || got.Value != 10 {
		t.Errorf("A's code after B's attempts = %+v, %v; want untouched at 10", got, err)
	}

	codesA, err := f.shop.CodesFor(ctx, a.ID)
	if err != nil || len(codesA) != 1 || codesA[0].ID != mine.ID {
		t.Errorf("A's codes = %+v, %v; want just theirs", codesA, err)
	}
	codesB, err := f.shop.CodesFor(ctx, b.ID)
	if err != nil || len(codesB) != 1 || codesB[0].ID != theirs.ID {
		t.Errorf("B's codes = %+v, %v; want just theirs", codesB, err)
	}
	if err := f.shop.DeleteCode(ctx, a.ID, mine.ID); err != nil {
		t.Fatalf("DeleteCode: %v", err)
	}
	if _, err := f.shop.CodeFor(ctx, a.ID, "SALE"); err == nil {
		t.Error("a deleted code still reads back")
	}
}

func TestPriceWithCode(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := addOperator(t, f.db, "a-seller", model.RoleReseller, 0)

	pct := saveCode(t, f, &Code{OperatorID: a.ID, Code: "PCT10", Kind: CodePercent, Value: 10, Enabled: true})
	saveCode(t, f, &Code{OperatorID: a.ID, Code: "FIX30", Kind: CodeFixed, Value: 30, Enabled: true})
	saveCode(t, f, &Code{OperatorID: a.ID, Code: "GIFT50", Kind: CodeGift, Value: 50, Enabled: true})
	saveCode(t, f, &Code{OperatorID: a.ID, Code: "OFF", Kind: CodePercent, Value: 10, Enabled: false})
	past := time.Now().UTC().Add(-time.Hour)
	saveCode(t, f, &Code{OperatorID: a.ID, Code: "OLD", Kind: CodePercent, Value: 10, Enabled: true, ExpiresAt: &past})
	one := saveCode(t, f, &Code{OperatorID: a.ID, Code: "ONE", Kind: CodePercent, Value: 10, Enabled: true, MaxUses: 1})

	if final, off, err := f.shop.PriceWithCode(ctx, a.ID, 111, "pct10", 90); err != nil || final != 81 || off != 9 {
		t.Errorf("10%% off 90 = (%d, %d), %v; want (81, 9)", final, off, err)
	}
	if final, off, err := f.shop.PriceWithCode(ctx, a.ID, 111, "FIX30", 90); err != nil || final != 60 || off != 30 {
		t.Errorf("30 off 90 = (%d, %d), %v; want (60, 30)", final, off, err)
	}
	// A fixed code never takes more than the price.
	if final, off, err := f.shop.PriceWithCode(ctx, a.ID, 111, "FIX30", 20); err != nil || final != 0 || off != 20 {
		t.Errorf("30 off 20 = (%d, %d), %v; want (0, 20)", final, off, err)
	}
	// Pricing records nothing: the same buyer may price again.
	if _, _, err := f.shop.PriceWithCode(ctx, a.ID, 111, "PCT10", 90); err != nil {
		t.Errorf("pricing twice was refused: %v", err)
	}
	if got, err := f.shop.CodeFor(ctx, a.ID, "PCT10"); err != nil || got.UsedCount != 0 {
		t.Errorf("after pricing, the code's uses = %+v, %v; want none recorded", got, err)
	}

	// The refusals: a gift at checkout, a switched-off code, an
	// expired one, an unknown one.
	for _, code := range []string{"GIFT50", "OFF", "OLD", "NOPE"} {
		if _, _, err := f.shop.PriceWithCode(ctx, a.ID, 111, code, 90); err == nil {
			t.Errorf("pricing with %s was allowed, want a refusal", code)
		}
	}

	// One use in all: once it is spent, nobody prices with it -- and
	// the buyer who spent it cannot price with any code twice either.
	if err := f.shop.RecordCodeUse(ctx, one.ID, 999, 0); err != nil {
		t.Fatalf("RecordCodeUse: %v", err)
	}
	if _, _, err := f.shop.PriceWithCode(ctx, a.ID, 111, "ONE", 90); err == nil {
		t.Error("a used-up code still priced")
	}
	if err := f.shop.RecordCodeUse(ctx, pct.ID, 111, 77); err != nil {
		t.Fatalf("RecordCodeUse: %v", err)
	}
	if _, _, err := f.shop.PriceWithCode(ctx, a.ID, 111, "PCT10", 90); err == nil {
		t.Error("a buyer priced with a code they had already used")
	}
	if _, _, err := f.shop.PriceWithCode(ctx, a.ID, 222, "PCT10", 90); err != nil {
		t.Errorf("another buyer could not price with the code: %v", err)
	}
	// Recording the same use again is the answer, not a second count.
	if err := f.shop.RecordCodeUse(ctx, pct.ID, 111, 77); err != nil {
		t.Fatalf("RecordCodeUse again: %v", err)
	}
	if got, err := f.shop.CodeFor(ctx, a.ID, "PCT10"); err != nil || got.UsedCount != 1 {
		t.Errorf("the code's uses = %+v, %v; want exactly 1", got, err)
	}
}

func TestRedeemGiftCreditsOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := addOperator(t, f.db, "a-seller", model.RoleReseller, 0)
	gift := saveCode(t, f, &Code{OperatorID: a.ID, Code: "GIFT50", Kind: CodeGift, Value: 50, Enabled: true})
	saveCode(t, f, &Code{OperatorID: a.ID, Code: "PCT10", Kind: CodePercent, Value: 10, Enabled: true})

	credited, err := f.shop.RedeemGift(ctx, a.ID, 111, "gift50")
	if err != nil {
		t.Fatalf("RedeemGift: %v", err)
	}
	if credited != 50 {
		t.Errorf("the redemption credited %d, want the code's 50", credited)
	}
	if got := balanceOf(t, f, a.ID, 111); got != 50 {
		t.Errorf("the wallet holds %d, want 50", got)
	}
	var use model.ShopCodeUse
	if err := f.db.Where("code_id = ? AND buyer_tg_id = ?", gift.ID, 111).First(&use).Error; err != nil {
		t.Fatalf("the redemption left no use: %v", err)
	}
	if use.OrderID != 0 {
		t.Errorf("the gift's use names order %d, want none", use.OrderID)
	}
	if got, err := f.shop.CodeFor(ctx, a.ID, "GIFT50"); err != nil || got.UsedCount != 1 {
		t.Errorf("the code's uses = %+v, %v; want 1", got, err)
	}

	// Once per buyer: a second redemption is refused and credits
	// nothing more.
	if _, err := f.shop.RedeemGift(ctx, a.ID, 111, "GIFT50"); err == nil {
		t.Error("a second redemption of the same gift was allowed")
	}
	if got := balanceOf(t, f, a.ID, 111); got != 50 {
		t.Errorf("the wallet holds %d after the refused redemption, want 50", got)
	}
	// A discount code is not a gift, however it is presented.
	if _, err := f.shop.RedeemGift(ctx, a.ID, 222, "PCT10"); err == nil {
		t.Error("a percent code was redeemed as a gift")
	}
	// Another buyer may still redeem theirs.
	if _, err := f.shop.RedeemGift(ctx, a.ID, 222, "GIFT50"); err != nil {
		t.Errorf("another buyer's redemption was refused: %v", err)
	}
}

// A code named at checkout is written down as used when its order is
// delivered -- once, across a double delivery.
func TestFulfillRecordsCodeUseOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	grant(t, f, seller.ID, f.ifaces[0])
	sellingShop(t, f, seller, []uint{f.ifaces[0].ID}, nil)
	code := saveCode(t, f, &Code{OperatorID: seller.ID, Code: "PCT10", Kind: CodePercent, Value: 10, Enabled: true})

	o := paidOrder(seller.ID, 111, KindNew)
	o.Code = "PCT10"
	o.DiscountToman = 9
	if err := f.shop.CreateOrder(ctx, o); err != nil {
		t.Fatal(err)
	}
	if _, err := f.shop.Fulfill(ctx, o.ID); err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	var use model.ShopCodeUse
	if err := f.db.Where("code_id = ? AND buyer_tg_id = ?", code.ID, 111).First(&use).Error; err != nil {
		t.Fatalf("the delivery left no use: %v", err)
	}
	if use.OrderID != o.ID {
		t.Errorf("the use names order %d, want the delivered %d", use.OrderID, o.ID)
	}
	if _, err := f.shop.Fulfill(ctx, o.ID); err != nil {
		t.Fatalf("Fulfill again: %v", err)
	}
	if got, err := f.shop.CodeFor(ctx, seller.ID, "PCT10"); err != nil || got.UsedCount != 1 {
		t.Errorf("the code's uses = %+v, %v; want exactly 1 after two deliveries", got, err)
	}
	var uses int64
	if err := f.db.Model(&model.ShopCodeUse{}).Where("code_id = ?", code.ID).Count(&uses).Error; err != nil {
		t.Fatal(err)
	}
	if uses != 1 {
		t.Errorf("%d uses are recorded, want 1", uses)
	}
}

// A bulk order delivers every unit: the first as the buyer's customer,
// each extra as another customer of the same terms, numbered after
// the first so the buyer can tell their own services apart.
func TestQuantityFulfillCreatesEveryUnit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	grant(t, f, seller.ID, f.ifaces[0])
	sellingShop(t, f, seller, []uint{f.ifaces[0].ID}, nil)

	o := paidOrder(seller.ID, 111, KindNew)
	o.Quantity = 3
	if err := f.shop.CreateOrder(ctx, o); err != nil {
		t.Fatal(err)
	}
	first, err := f.shop.Fulfill(ctx, o.ID)
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	rows := clientsOf(t, f, 111)
	if len(rows) != 3 {
		t.Fatalf("the buyer holds %d customers, want the order's 3 units", len(rows))
	}
	if rows[0].ID != first.ID {
		t.Errorf("the first customer is %d, want the one Fulfill returned (%d)", rows[0].ID, first.ID)
	}
	wantNames := []string{"Buyer", "Buyer (2)", "Buyer (3)"}
	for i, row := range rows {
		if row.Name != wantNames[i] {
			t.Errorf("unit %d is named %q, want %q", i+1, row.Name, wantNames[i])
		}
		if row.QuotaBytes != 20<<30 {
			t.Errorf("unit %d's quota is %d, want the plan's %d", i+1, row.QuotaBytes, 20<<30)
		}
		if row.OwnerID != seller.ID {
			t.Errorf("unit %d belongs to %d, want the seller %d", i+1, row.OwnerID, seller.ID)
		}
	}
	after, err := f.shop.OrderByID(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusFulfilled || after.ClientID != first.ID {
		t.Errorf("the order is %s with client %d, want fulfilled with the first unit %d",
			after.Status, after.ClientID, first.ID)
	}
	// A repeated delivery adds nothing.
	if _, err := f.shop.Fulfill(ctx, o.ID); err != nil {
		t.Fatalf("Fulfill again: %v", err)
	}
	if rows := clientsOf(t, f, 111); len(rows) != 3 {
		t.Errorf("after a second delivery the buyer holds %d customers, want 3 still", len(rows))
	}
}
