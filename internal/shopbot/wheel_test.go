package shopbot

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/shop"
)

// The v5 wheel and lottery, walked the way the earlier suites walk
// the shop: the wheel from the buyer's menu, the draw from the
// seller's panel.

// TestTheWheelButtonSpinsOnceADay walks the buyer's side of the
// wheel: the button is on the menu of a shop whose wheel is on,
// one tap spins, and a second tap inside the day is answered with
// when the next turn comes.
func TestTheWheelButtonSpinsOnceADay(t *testing.T) {
	fx := newShopFixture(t)
	ctx := context.Background()
	cfg, err := fx.sh.ConfigFor(ctx, fx.opID)
	if err != nil {
		t.Fatal(err)
	}
	cfg.WheelEnabled = true
	if err := fx.sh.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	fake, _, _ := runBot(t, fx)
	const buyer = 111
	fake.push(updText(1, buyer, buyer, "Buyer", "/start"))
	waitFor(t, "the welcome with the wheel on it", func() bool {
		return fake.saidTo(buyer, "گردونه شانس") && fake.saidTo(buyer, `"wheel"`)
	})

	fake.push(updCallback(2, buyer, buyer, "Buyer", 5001, "wheel"))
	waitFor(t, "the spin's outcome", func() bool {
		w, err := fx.sh.WalletFor(ctx, fx.opID, buyer)
		return err == nil && w.LastSpinAt != nil
	})

	fake.push(updCallback(3, buyer, buyer, "Buyer", 5001, "wheel"))
	waitFor(t, "the second spin refused with its hour", func() bool {
		return fake.anySaid("امروز گردونه را چرخانده‌اید")
	})
}

// TestTheLotteryDraw walks the seller's draw: the pool of the last
// month's buyers, the cheapest plan's price as the prize, exactly
// one winner credited exactly that, and the news in their chat.
func TestTheLotteryDraw(t *testing.T) {
	fx := newShopFixture(t)
	fake, _, _ := runBot(t, fx)
	const seller = 999

	now := time.Now().UTC()
	seedOrder := func(tg int64, status string, age time.Duration) {
		t.Helper()
		row := &model.ShopOrder{
			OperatorID: fx.opID, BuyerTGID: tg, BuyerName: fmt.Sprintf("Buyer %d", tg),
			PlanName: fx.plan.Name, Kind: shop.KindNew, Method: shop.MethodCard,
			Status: status, PublicToken: fmt.Sprintf("lotto-%d-%s", tg, status),
			CreatedAt: now.Add(-age),
		}
		if err := fx.db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	buyers := []int64{111, 222, 333}
	for _, tg := range buyers {
		seedOrder(tg, shop.StatusFulfilled, 2*24*time.Hour)
	}
	seedOrder(444, shop.StatusFulfilled, 40*24*time.Hour) // too old for the pool
	seedOrder(555, shop.StatusUnpaid, time.Hour)          // never a sale

	fake.push(updText(1, seller, seller, "Seller", "/start"))
	waitFor(t, "the seller panel with the lottery on it", func() bool {
		return fake.anySaid("sp:lotto")
	})
	fake.push(updCallback(2, seller, seller, "Seller", 5002, "sp:lotto"))
	waitFor(t, "the draw offer with the pool and the prize", func() bool {
		return fake.anySaid("شرکت‌کننده‌ها") && fake.anySaid("sp:lotdraw")
	})

	fake.push(updCallback(3, seller, seller, "Seller", 5002, "sp:lotdraw"))
	waitFor(t, "the seller told the winner", func() bool {
		return strings.Contains(chatTextAll(fake, seller), "قرعه‌کشی انجام شد")
	})

	// Exactly one pool member holds exactly the cheapest plan's
	// price (the fixture sells one plan), and the winner heard it.
	credited := 0
	winner := int64(0)
	for _, tg := range buyers {
		if got := balanceOf(t, fx, tg); got == fx.plan.PriceToman {
			credited++
			winner = tg
		} else if got != 0 {
			t.Errorf("buyer %d holds %d, want 0 or the prize %d", tg, got, fx.plan.PriceToman)
		}
	}
	if credited != 1 {
		t.Fatalf("%d buyers were credited, want exactly 1", credited)
	}
	if !fake.saidTo(winner, "برنده شدید") {
		t.Error("the winner was not told")
	}
	if got := balanceOf(t, fx, 444); got != 0 {
		t.Errorf("the too-old buyer holds %d, want nothing", got)
	}
	if !strings.Contains(chatTextAll(fake, seller), fmt.Sprintf("<code>%d</code>", winner)) {
		t.Error("the seller's screen does not name the winner")
	}
}

// TestTheLotteryNeedsAPool: a shop whose month holds no delivered
// sale offers no draw.
func TestTheLotteryNeedsAPool(t *testing.T) {
	fx := newShopFixture(t)
	fake, _, _ := runBot(t, fx)
	const seller = 999

	fake.push(updText(1, seller, seller, "Seller", "/start"))
	waitFor(t, "the seller panel", func() bool { return fake.anySaid("sp:lotto") })
	fake.push(updCallback(2, seller, seller, "Seller", 5002, "sp:lotto"))
	waitFor(t, "the empty draw refused in words", func() bool {
		return fake.anySaid("قرعه‌کشی ممکن نیست")
	})
}
