package shop

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// forceRoll stands the wheel on one band for the length of a test.
func forceRoll(t *testing.T, roll int) {
	t.Helper()
	old := wheelRoll
	wheelRoll = func() int { return roll }
	t.Cleanup(func() { wheelRoll = old })
}

func wheelShop(t *testing.T, f *fixture, seller *model.Admin, ifaces []uint) {
	t.Helper()
	sellingShop(t, f, seller, ifaces, func(c *Config) { c.WheelEnabled = true })
}

// A shop with the wheel switched off refuses the spin, and a buyer
// who has spun cannot spin again inside the day -- the refusal is
// its own error, so the bot can answer "come back later" rather
// than "something broke".
func TestSpinWheelRefusals(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	grant(t, f, seller.ID, f.ifaces[0])
	sellingShop(t, f, seller, []uint{f.ifaces[0].ID}, nil)

	if _, err := f.shop.SpinWheel(ctx, seller.ID, 111); err == nil {
		t.Error("a spin was allowed at a shop whose wheel is off")
	}

	wheelShop(t, f, seller, []uint{f.ifaces[0].ID})
	forceRoll(t, 10) // the nothing band
	prize, err := f.shop.SpinWheel(ctx, seller.ID, 111)
	if err != nil {
		t.Fatalf("SpinWheel: %v", err)
	}
	if prize.Kind != PrizeNone {
		t.Errorf("a roll of 10 won %+v, want nothing", prize)
	}
	w, err := f.shop.WalletFor(ctx, seller.ID, 111)
	if err != nil {
		t.Fatal(err)
	}
	if w.LastSpinAt == nil {
		t.Error("a completed spin -- even an empty one -- did not mark the wallet")
	}
	if _, err := f.shop.SpinWheel(ctx, seller.ID, 111); !errors.Is(err, ErrWheelTooSoon) {
		t.Errorf("a second spin within the day: err = %v, want ErrWheelTooSoon", err)
	}

	// Another buyer at the same shop is fenced by their own clock.
	if _, err := f.shop.SpinWheel(ctx, seller.ID, 222); err != nil {
		t.Errorf("another buyer's first spin was refused: %v", err)
	}
}

// A spin landing on a code band mints a real code: single-use, a
// week to live, and priced by the checkout like any other.
func TestSpinWheelCodePrizeIsSpendable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	grant(t, f, seller.ID, f.ifaces[0])
	wheelShop(t, f, seller, []uint{f.ifaces[0].ID})
	forceRoll(t, 50) // the 5% band

	prize, err := f.shop.SpinWheel(ctx, seller.ID, 111)
	if err != nil {
		t.Fatalf("SpinWheel: %v", err)
	}
	if prize.Kind != PrizeCode || prize.Percent != 5 || prize.Code == "" {
		t.Fatalf("the prize is %+v, want a 5%% code", prize)
	}
	code, err := f.shop.CodeFor(ctx, seller.ID, prize.Code)
	if err != nil {
		t.Fatalf("the prize code is not the shop's: %v", err)
	}
	if code.Kind != CodePercent || code.Value != 5 || code.MaxUses != 1 || !code.Enabled {
		t.Errorf("the prize code is %+v, want an enabled single-use 5%% code", code)
	}
	if code.ExpiresAt == nil || time.Until(*code.ExpiresAt) < 6*24*time.Hour {
		t.Errorf("the prize code ends at %v, want about a week of life", code.ExpiresAt)
	}
	final, discount, err := f.shop.PriceWithCode(ctx, seller.ID, 111, prize.Code, 1000)
	if err != nil {
		t.Fatalf("PriceWithCode: %v", err)
	}
	if final != 950 || discount != 50 {
		t.Errorf("the code prices 1000 as %d with %d off, want 950 with 50 off", final, discount)
	}
}

// A spin landing on volume grows the buyer's own plan by the prize;
// a buyer holding no plan is handed a code instead, so the prize
// still lands.
func TestSpinWheelVolumePrize(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	grant(t, f, seller.ID, f.ifaces[0])
	wheelShop(t, f, seller, []uint{f.ifaces[0].ID})

	// The buyer holds a customer from a delivered sale.
	o := paidOrder(seller.ID, 111, KindNew)
	if err := f.shop.CreateOrder(ctx, o); err != nil {
		t.Fatal(err)
	}
	client, err := f.shop.Fulfill(ctx, o.ID)
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	before := client.QuotaBytes

	forceRoll(t, 90) // the +1 GiB band
	prize, err := f.shop.SpinWheel(ctx, seller.ID, 111)
	if err != nil {
		t.Fatalf("SpinWheel: %v", err)
	}
	if prize.Kind != PrizeVolume || prize.Bytes != 1<<30 {
		t.Fatalf("the prize is %+v, want 1 GiB of volume", prize)
	}
	opCtx, err := f.shop.OperatorCtx(ctx, seller.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := f.clients.Get(opCtx, client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.QuotaBytes != before+(1<<30) {
		t.Errorf("the quota is %d, want %d (the plan plus the prize)", after.QuotaBytes, before+(1<<30))
	}

	// A stranger to the shop wins a code instead of volume.
	prize, err = f.shop.SpinWheel(ctx, seller.ID, 999)
	if err != nil {
		t.Fatalf("SpinWheel for a planless buyer: %v", err)
	}
	if prize.Kind != PrizeCode || prize.Percent != 5 || prize.Code == "" {
		t.Errorf("a planless buyer's volume prize is %+v, want a 5%% code", prize)
	}
}

// The lottery pool is the shop's delivered business of the window:
// old sales and undelivered ones are not in it, and a top-up never
// is. The prize is the cheapest plan the shop sells.
func TestLotteryPoolAndPrize(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	now := time.Now().UTC()
	since := now.AddDate(0, 0, -30)

	seq := 0
	seed := func(tg int64, kind, status string, age time.Duration) {
		t.Helper()
		seq++
		row := &model.ShopOrder{
			OperatorID: seller.ID, BuyerTGID: tg, BuyerName: "Buyer",
			Kind: kind, Method: MethodCard, Status: status,
			PublicToken: "tok-" + string(rune('0'+seq)),
			CreatedAt:   now.Add(-age),
		}
		if err := f.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	seed(111, KindNew, StatusFulfilled, 2*24*time.Hour)   // in
	seed(111, KindRenew, StatusFulfilled, 3*24*time.Hour) // same buyer, still one name
	seed(222, KindRenew, StatusFulfilled, 5*24*time.Hour) // in
	seed(333, KindNew, StatusFulfilled, 40*24*time.Hour)  // too old
	seed(444, KindNew, StatusUnpaid, time.Hour)           // never delivered
	seed(555, KindTopup, StatusFulfilled, time.Hour)      // money moving, not a sale
	other := addOperator(t, f.db, "other", model.RoleReseller, 0)
	row := &model.ShopOrder{
		OperatorID: other.ID, BuyerTGID: 666, BuyerName: "Buyer",
		Kind: KindNew, Method: MethodCard, Status: StatusFulfilled,
		PublicToken: "tok-other", CreatedAt: now.Add(-time.Hour),
	}
	if err := f.db.Create(row).Error; err != nil {
		t.Fatal(err)
	}

	pool, err := f.shop.LotteryPool(ctx, seller.ID, since)
	if err != nil {
		t.Fatalf("LotteryPool: %v", err)
	}
	inPool := map[int64]bool{}
	for _, tg := range pool {
		inPool[tg] = true
	}
	if len(pool) != 2 || !inPool[111] || !inPool[222] {
		t.Errorf("the pool is %v, want exactly buyers 111 and 222", pool)
	}

	if _, err := f.shop.CheapestPlanPrice(ctx, seller.ID); err == nil {
		t.Error("a shop with no plans priced a prize")
	}
	for _, p := range []int64{5000, 3000, 9000} {
		if err := f.shop.SavePlan(ctx, &Plan{
			OperatorID: seller.ID, Name: "p", PriceToman: p, Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	price, err := f.shop.CheapestPlanPrice(ctx, seller.ID)
	if err != nil {
		t.Fatalf("CheapestPlanPrice: %v", err)
	}
	if price != 3000 {
		t.Errorf("the prize is %d, want the cheapest plan's 3000", price)
	}
}
