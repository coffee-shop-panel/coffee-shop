package shopbot

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/shop"
)

// The v5 buyer features, walked the way the earlier suites walk the
// shop: a real shop on a real database, a fake Telegram on the other
// end of the wire, and assertions on what the buyer, the seller and
// the database each end up holding.

// runBot boots the fixture's bot against a fresh fake Telegram and
// waits until it answers to its username.
func runBot(t *testing.T, fx *shopFixture) (*fakeTG, *Manager, context.Context) {
	t.Helper()
	fake, srv := newFakeTG(t)
	oldBase := APIBase
	APIBase = srv.URL
	t.Cleanup(func() { APIBase = oldBase })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := New(fx.sh, fx.clients, quietLog())
	mgr.Start(ctx)
	waitFor(t, "the bot to be running", func() bool {
		_, running := mgr.BotInfo(fx.opID)
		return running
	})
	return fake, mgr, ctx
}

// chatText joins everything the bot sent one chat, for assertions
// about a whole conversation -- a bulk delivery's three links live
// in one message among several.
func (f *fakeTG) chatText(chatID int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var sb strings.Builder
	for _, m := range f.sent {
		if m.chatID == chatID {
			sb.WriteString(m.flat)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func balanceOf(t *testing.T, fx *shopFixture, tgID int64) int64 {
	t.Helper()
	w, err := fx.sh.WalletFor(context.Background(), fx.opID, tgID)
	if err != nil {
		t.Fatalf("read the wallet: %v", err)
	}
	return w.BalanceToman
}

func customersOf(t *testing.T, fx *shopFixture, tgID int64) []model.Client {
	t.Helper()
	var rows []model.Client
	if err := fx.db.Where("telegram_id = ?", tgID).Order("id").Find(&rows).Error; err != nil {
		t.Fatalf("read the customers: %v", err)
	}
	return rows
}

// buyByCard walks a buyer from /start to the card instructions for a
// quantity of the fixture's plan, and returns the order standing
// there waiting for its receipt.
func buyByCard(t *testing.T, fake *fakeTG, fx *shopFixture, buyer int64, qty int) shop.Order {
	t.Helper()
	ctx := context.Background()
	fake.push(updText(1, buyer, buyer, "Buyer", "/start"))
	waitFor(t, "the welcome", func() bool { return fake.saidTo(buyer, "خوش آمدید") })
	fake.push(updCallback(2, buyer, buyer, "Buyer", 5001, "buy"))
	waitFor(t, "the plan list", func() bool { return fake.anySaid("یک ماهه") })
	fake.push(updCallback(3, buyer, buyer, "Buyer", 5001, fmt.Sprintf("plan:%d", fx.plan.ID)))
	waitFor(t, "the quantity choices", func() bool {
		return fake.anySaid(fmt.Sprintf("qty:%d:%d", fx.plan.ID, qty))
	})
	fake.push(updCallback(4, buyer, buyer, "Buyer", 5001, fmt.Sprintf("qty:%d:%d", fx.plan.ID, qty)))
	waitFor(t, "the payment choices", func() bool { return fake.anySaid(btnPayCard) })
	fake.push(updCallback(5, buyer, buyer, "Buyer", 5001, fmt.Sprintf("card:%d", fx.plan.ID)))
	waitFor(t, "the card instructions", func() bool { return fake.anySaid("6037991234567890") })
	orders, err := fx.sh.OrdersFor(ctx, fx.opID, "", 10)
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders after choosing card: %v (%d)", err, len(orders))
	}
	return orders[0]
}

// approve walks the receipt road: the buyer sends the photo, the
// seller taps approve on the copy that lands in their chat.
func approve(t *testing.T, fake *fakeTG, fx *shopFixture, buyer, seller int64, orderID uint) {
	t.Helper()
	fake.push(updPhoto(6, buyer, buyer, "Buyer"))
	waitFor(t, "the receipt to reach the seller", func() bool {
		return fake.copiedTo(seller, fmt.Sprintf("ok:%d", orderID))
	})
	fake.push(updCallback(7, seller, seller, "Seller", 6001, fmt.Sprintf("ok:%d", orderID)))
	waitFor(t, "the order to be fulfilled", func() bool {
		o, err := fx.sh.OrderByID(context.Background(), orderID)
		return err == nil && o.Status == "fulfilled"
	})
}

// TestQuantityPurchaseDeliversEveryUnit buys three of the plan by
// card: the total is three prices, delivery makes three customers,
// and the one delivery message carries all three links.
func TestQuantityPurchaseDeliversEveryUnit(t *testing.T) {
	fx := newShopFixture(t)
	fake, _, ctx := runBot(t, fx)
	if err := fx.sh.SetPublicBase(ctx, "https://panel.example.com"); err != nil {
		t.Fatalf("set public base: %v", err)
	}

	const buyer, seller = 111, 999
	order := buyByCard(t, fake, fx, buyer, 3)
	if order.Quantity != 3 {
		t.Fatalf("the order buys %d, want 3", order.Quantity)
	}
	if order.PriceToman != 3*fx.plan.PriceToman {
		t.Errorf("the order's price is %d, want three plans' %d", order.PriceToman, 3*fx.plan.PriceToman)
	}
	approve(t, fake, fx, buyer, seller, order.ID)

	rows := customersOf(t, fx, buyer)
	if len(rows) != 3 {
		t.Fatalf("the buyer holds %d customers, want the order's 3 units", len(rows))
	}
	waitFor(t, "the bulk delivery with every link", func() bool {
		text := fake.chatText(buyer)
		for _, c := range rows {
			if !strings.Contains(text, "https://panel.example.com/subscribe/"+c.SubToken) {
				return false
			}
		}
		return strings.Contains(text, "اشتراک‌های شما آماده است")
	})
}

// TestDiscountCodeAtCheckout prices a code against the basket,
// stamps it on the order, and charges the lowered price: the card
// instructions name the final amount, and delivery records the use.
func TestDiscountCodeAtCheckout(t *testing.T) {
	fx := newShopFixture(t)
	fake, _, ctx := runBot(t, fx)
	if err := fx.sh.SaveCode(ctx, &shop.Code{
		OperatorID: fx.opID, Code: "PCT10", Kind: shop.CodePercent, Value: 10, Enabled: true,
	}); err != nil {
		t.Fatalf("save code: %v", err)
	}

	const buyer, seller = 111, 999
	fake.push(updText(1, buyer, buyer, "Buyer", "/start"))
	waitFor(t, "the welcome", func() bool { return fake.saidTo(buyer, "خوش آمدید") })
	fake.push(updCallback(2, buyer, buyer, "Buyer", 5001, "buy"))
	waitFor(t, "the plan list", func() bool { return fake.anySaid("یک ماهه") })
	fake.push(updCallback(3, buyer, buyer, "Buyer", 5001, fmt.Sprintf("plan:%d", fx.plan.ID)))
	fake.push(updCallback(4, buyer, buyer, "Buyer", 5001, fmt.Sprintf("qty:%d:1", fx.plan.ID)))
	waitFor(t, "the payment choices", func() bool { return fake.anySaid(btnPayCard) })

	fake.push(updCallback(5, buyer, buyer, "Buyer", 5001, fmt.Sprintf("code:%d", fx.plan.ID)))
	waitFor(t, "the code prompt", func() bool { return fake.anySaid("کد تخفیف را بنویسید") })
	fake.push(updText(6, buyer, buyer, "Buyer", "PCT10"))
	waitFor(t, "the discounted payment screen", func() bool {
		return fake.saidTo(buyer, "مبلغ نهایی") && fake.saidTo(buyer, "۱۰۸٬۰۰۰ تومان")
	})

	fake.push(updCallback(7, buyer, buyer, "Buyer", 5001, fmt.Sprintf("card:%d", fx.plan.ID)))
	waitFor(t, "the card instructions at the final price", func() bool {
		return fake.anySaid("6037991234567890") && fake.anySaid("۱۰۸٬۰۰۰ تومان")
	})
	orders, err := fx.sh.OrdersFor(ctx, fx.opID, "", 10)
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders: %v (%d)", err, len(orders))
	}
	order := orders[0]
	if order.Code != "PCT10" || order.DiscountToman != 12000 || order.PriceToman != 108000 {
		t.Fatalf("the order is code %q, discount %d, price %d; want PCT10, 12000 off, 108000",
			order.Code, order.DiscountToman, order.PriceToman)
	}

	approve(t, fake, fx, buyer, seller, order.ID)
	code, err := fx.sh.CodeFor(ctx, fx.opID, "PCT10")
	if err != nil || code.UsedCount != 1 {
		t.Errorf("the code's uses = %+v, %v; want exactly 1 recorded at delivery", code, err)
	}
}

// TestWalletPaymentDeliversWithoutAReceipt pays out of a stocked
// wallet: no card, no receipt, no seller decision -- the debit is
// the payment and the subscription arrives behind it.
func TestWalletPaymentDeliversWithoutAReceipt(t *testing.T) {
	fx := newShopFixture(t)
	fake, _, ctx := runBot(t, fx)
	const buyer = 111
	if err := fx.sh.CreditWallet(ctx, fx.opID, buyer, 200000); err != nil {
		t.Fatalf("stock the wallet: %v", err)
	}

	fake.push(updText(1, buyer, buyer, "Buyer", "/start"))
	waitFor(t, "the welcome", func() bool { return fake.saidTo(buyer, "خوش آمدید") })
	fake.push(updCallback(2, buyer, buyer, "Buyer", 5001, "buy"))
	waitFor(t, "the plan list", func() bool { return fake.anySaid("یک ماهه") })
	fake.push(updCallback(3, buyer, buyer, "Buyer", 5001, fmt.Sprintf("plan:%d", fx.plan.ID)))
	fake.push(updCallback(4, buyer, buyer, "Buyer", 5001, fmt.Sprintf("qty:%d:1", fx.plan.ID)))
	waitFor(t, "the wallet button on the payment screen", func() bool {
		return fake.anySaid(fmt.Sprintf("wal:%d", fx.plan.ID)) && fake.anySaid("پرداخت از کیف پول")
	})

	fake.push(updCallback(5, buyer, buyer, "Buyer", 5001, fmt.Sprintf("wal:%d", fx.plan.ID)))
	waitFor(t, "the delivery", func() bool { return fake.saidTo(buyer, "اشتراک شما آماده است") })

	orders, err := fx.sh.OrdersFor(ctx, fx.opID, "", 10)
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders: %v (%d)", err, len(orders))
	}
	if orders[0].Method != "wallet" || orders[0].Status != "fulfilled" {
		t.Errorf("the order is %s by %s, want fulfilled by wallet", orders[0].Status, orders[0].Method)
	}
	if got := balanceOf(t, fx, buyer); got != 200000-fx.plan.PriceToman {
		t.Errorf("the wallet holds %d, want the price taken out: %d", got, 200000-fx.plan.PriceToman)
	}
	if rows := customersOf(t, fx, buyer); len(rows) != 1 {
		t.Errorf("the buyer holds %d customers, want 1", len(rows))
	}
}

// TestWalletButtonNeedsTheBalance: an empty wallet is not offered
// as a way of paying -- the button exists only when the balance
// covers what is owed.
func TestWalletButtonNeedsTheBalance(t *testing.T) {
	fx := newShopFixture(t)
	fake, _, _ := runBot(t, fx)
	const buyer = 111

	fake.push(updText(1, buyer, buyer, "Buyer", "/start"))
	waitFor(t, "the welcome", func() bool { return fake.saidTo(buyer, "خوش آمدید") })
	fake.push(updCallback(2, buyer, buyer, "Buyer", 5001, "buy"))
	waitFor(t, "the plan list", func() bool { return fake.anySaid("یک ماهه") })
	fake.push(updCallback(3, buyer, buyer, "Buyer", 5001, fmt.Sprintf("plan:%d", fx.plan.ID)))
	fake.push(updCallback(4, buyer, buyer, "Buyer", 5001, fmt.Sprintf("qty:%d:1", fx.plan.ID)))
	var screen string
	waitFor(t, "the payment screen", func() bool {
		flat, ok := fake.editContaining(txtChoosePay)
		screen = flat
		return ok
	})
	if strings.Contains(screen, "پرداخت از کیف پول") || strings.Contains(screen, fmt.Sprintf("wal:%d", fx.plan.ID)) {
		t.Errorf("an empty wallet was offered as a way of paying: %s", screen)
	}
}

// TestTopupByCardCreditsTheWallet walks a top-up down the card road
// a plan takes: amount, card, receipt, the seller's approval -- and
// the delivery is money in the wallet, not a customer.
func TestTopupByCardCreditsTheWallet(t *testing.T) {
	fx := newShopFixture(t)
	fake, _, ctx := runBot(t, fx)
	const buyer, seller = 111, 999

	fake.push(updText(1, buyer, buyer, "Buyer", "/start"))
	waitFor(t, "the welcome", func() bool { return fake.saidTo(buyer, "خوش آمدید") })
	fake.push(updCallback(2, buyer, buyer, "Buyer", 5001, "wallet"))
	waitFor(t, "the wallet screen", func() bool { return fake.anySaid("کیف پول شما") })
	fake.push(updCallback(3, buyer, buyer, "Buyer", 5001, "topup"))
	waitFor(t, "the amounts", func() bool { return fake.anySaid("top:100000") })
	fake.push(updCallback(4, buyer, buyer, "Buyer", 5001, "top:100000"))
	waitFor(t, "the top-up payment choices", func() bool { return fake.anySaid("topcard:100000") })
	fake.push(updCallback(5, buyer, buyer, "Buyer", 5001, "topcard:100000"))
	waitFor(t, "the card instructions", func() bool {
		return fake.anySaid("6037991234567890") && fake.anySaid("۱۰۰٬۰۰۰ تومان")
	})

	orders, err := fx.sh.OrdersFor(ctx, fx.opID, "", 10)
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders: %v (%d)", err, len(orders))
	}
	order := orders[0]
	if order.Kind != shop.KindTopup || order.PriceToman != 100000 {
		t.Fatalf("the order is a %s of %d, want a top-up of 100000", order.Kind, order.PriceToman)
	}

	approve(t, fake, fx, buyer, seller, order.ID)
	if got := balanceOf(t, fx, buyer); got != 100000 {
		t.Errorf("the wallet holds %d, want the top-up's 100000", got)
	}
	waitFor(t, "the buyer to hear the wallet was charged", func() bool {
		return fake.saidTo(buyer, "کیف پول شما شارژ شد")
	})
	if rows := customersOf(t, fx, buyer); len(rows) != 0 {
		t.Errorf("a top-up made %d customers, want none", len(rows))
	}
}

// TestGiftCodeRedeemsIntoTheWallet: a gift code typed at the wallet
// lands its amount as balance -- once; the second typing of the same
// code is refused and moves nothing.
func TestGiftCodeRedeemsIntoTheWallet(t *testing.T) {
	fx := newShopFixture(t)
	fake, _, ctx := runBot(t, fx)
	if err := fx.sh.SaveCode(ctx, &shop.Code{
		OperatorID: fx.opID, Code: "GIFT50", Kind: shop.CodeGift, Value: 50000, Enabled: true,
	}); err != nil {
		t.Fatalf("save code: %v", err)
	}
	const buyer = 111

	fake.push(updText(1, buyer, buyer, "Buyer", "/start"))
	waitFor(t, "the welcome", func() bool { return fake.saidTo(buyer, "خوش آمدید") })
	fake.push(updCallback(2, buyer, buyer, "Buyer", 5001, "wallet"))
	fake.push(updCallback(3, buyer, buyer, "Buyer", 5001, "gift"))
	waitFor(t, "the gift prompt", func() bool { return fake.anySaid("کد هدیه را بنویسید") })
	fake.push(updText(4, buyer, buyer, "Buyer", "GIFT50"))
	waitFor(t, "the redemption news", func() bool { return fake.saidTo(buyer, "به کیف پولتان اضافه شد") })
	if got := balanceOf(t, fx, buyer); got != 50000 {
		t.Fatalf("the wallet holds %d, want the gift's 50000", got)
	}

	fake.push(updCallback(5, buyer, buyer, "Buyer", 5001, "wallet"))
	fake.push(updCallback(6, buyer, buyer, "Buyer", 5001, "gift"))
	fake.push(updText(7, buyer, buyer, "Buyer", "GIFT50"))
	waitFor(t, "the second redemption refused", func() bool { return fake.saidTo(buyer, "معتبر نیست") })
	if got := balanceOf(t, fx, buyer); got != 50000 {
		t.Errorf("the wallet holds %d after a refused redemption, want 50000 still", got)
	}
}

// TestReferralBindsAndCounts: a buyer arriving on another's
// invitation link is bound to them at /start, their own link names
// them, and the inviter's screen counts the arrival.
func TestReferralBindsAndCounts(t *testing.T) {
	fx := newShopFixture(t)
	fake, _, ctx := runBot(t, fx)
	const inviter, buyer = 111, 222
	// The inviter is a participant in this shop -- the door the
	// binding rules ask for -- by holding a wallet here.
	if _, err := fx.sh.WalletFor(ctx, fx.opID, inviter); err != nil {
		t.Fatalf("open the inviter's wallet: %v", err)
	}

	fake.push(updText(1, buyer, buyer, "Buyer", "/start ref_111"))
	waitFor(t, "the welcome", func() bool { return fake.saidTo(buyer, "خوش آمدید") })
	waitFor(t, "the binding to be written", func() bool {
		w, err := fx.sh.WalletFor(ctx, fx.opID, buyer)
		return err == nil && w.ReferrerTGID == inviter
	})

	fake.push(updCallback(2, buyer, buyer, "Buyer", 5001, "wallet"))
	fake.push(updCallback(3, buyer, buyer, "Buyer", 5001, "refer"))
	waitFor(t, "the buyer's own invitation link", func() bool {
		return fake.anySaid("https://t.me/shop_bot?start=ref_222")
	})

	fake.push(updText(4, inviter, inviter, "Inviter", "/start"))
	waitFor(t, "the inviter's welcome", func() bool { return fake.saidTo(inviter, "خوش آمدید") })
	fake.push(updCallback(5, inviter, inviter, "Inviter", 5001, "wallet"))
	fake.push(updCallback(6, inviter, inviter, "Inviter", 5001, "refer"))
	waitFor(t, "the inviter to count one arrival", func() bool {
		return fake.anySaid("۱ نفر")
	})
}

// TestTrialDeliversOnceAndRefusesTwice: a shop offering a trial
// shows the button, the tap delivers a real customer with its link,
// and the second tap is refused in words -- one trial per buyer.
func TestTrialDeliversOnceAndRefusesTwice(t *testing.T) {
	fx := newShopFixture(t)
	ctx := context.Background()
	cfg, err := fx.sh.ConfigFor(ctx, fx.opID)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	cfg.TrialEnabled = true
	cfg.TrialBytes = 1 << 30
	cfg.TrialDays = 7
	if err := fx.sh.SaveConfig(ctx, cfg); err != nil {
		t.Fatalf("offer the trial: %v", err)
	}
	fake, _, _ := runBot(t, fx)
	const buyer = 111

	fake.push(updText(1, buyer, buyer, "Buyer", "/start"))
	waitFor(t, "the menu with the trial button", func() bool { return fake.saidTo(buyer, btnTrial) })
	fake.push(updCallback(2, buyer, buyer, "Buyer", 5001, "trial"))
	waitFor(t, "the trial customer to exist", func() bool {
		return len(customersOf(t, fx, buyer)) == 1
	})
	rows := customersOf(t, fx, buyer)
	waitFor(t, "the trial delivery to reach the buyer", func() bool {
		return fake.saidTo(buyer, rows[0].SubToken)
	})

	fake.push(updCallback(3, buyer, buyer, "Buyer", 5001, "trial"))
	waitFor(t, "the second trial refused in words", func() bool {
		return fake.saidTo(buyer, "قبلاً اکانت تست")
	})
	if rows := customersOf(t, fx, buyer); len(rows) != 1 {
		t.Errorf("the buyer holds %d customers, want the one trial", len(rows))
	}
}

// TestFaqButtonsFollowTheTexts: the teaching buttons exist only in
// a menu whose shop has written the words, and tapping one sends
// the seller's own text.
func TestFaqButtonsFollowTheTexts(t *testing.T) {
	fx := newShopFixture(t)
	fake, mgr, ctx := runBot(t, fx)
	const buyer = 111

	fake.push(updText(1, buyer, buyer, "Buyer", "/start"))
	waitFor(t, "the welcome", func() bool { return fake.saidTo(buyer, "خوش آمدید") })
	if fake.saidTo(buyer, btnTutorial) || fake.saidTo(buyer, btnFaq) {
		t.Fatal("a shop with no texts written offers teaching buttons")
	}

	cfg, err := fx.sh.ConfigFor(ctx, fx.opID)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	cfg.TutorialText = "متن آموزش اتصال اینجاست"
	cfg.FaqText = "پاسخ پرسش‌های پرتکرار اینجاست"
	if err := fx.sh.SaveConfig(ctx, cfg); err != nil {
		t.Fatalf("write the texts: %v", err)
	}
	mgr.Reload(fx.opID)

	fake.push(updText(2, buyer, buyer, "Buyer", "/start"))
	waitFor(t, "the menu with both buttons", func() bool {
		return fake.saidTo(buyer, btnTutorial) && fake.saidTo(buyer, btnFaq)
	})
	fake.push(updCallback(3, buyer, buyer, "Buyer", 5001, "tutorial"))
	waitFor(t, "the tutorial text", func() bool { return fake.saidTo(buyer, "متن آموزش اتصال اینجاست") })
	fake.push(updCallback(4, buyer, buyer, "Buyer", 5001, "faq"))
	waitFor(t, "the faq text", func() bool { return fake.saidTo(buyer, "پاسخ پرسش‌های پرتکرار اینجاست") })
}

// TestSellerBuyerDetailShowsTheWallet: the seller looking at one of
// their buyers sees what that buyer holds at the shop, beside the
// plan they have used.
func TestSellerBuyerDetailShowsTheWallet(t *testing.T) {
	fx := newShopFixture(t)
	fake, _, ctx := runBot(t, fx)
	const buyer, seller = 111, 999

	o := &shop.Order{
		OperatorID: fx.opID, BuyerTGID: buyer, BuyerName: "Buyer",
		PlanID: fx.plan.ID, PlanName: fx.plan.Name, Section: shop.SectionWireGuard,
		QuotaBytes: fx.plan.QuotaBytes, Days: fx.plan.Days,
		PriceToman: fx.plan.PriceToman, DeviceLimit: fx.plan.DeviceLimit,
		Kind: shop.KindNew, Method: shop.MethodCard, Status: shop.StatusPaid,
	}
	if err := fx.sh.CreateOrder(ctx, o); err != nil {
		t.Fatalf("create order: %v", err)
	}
	client, err := fx.sh.Fulfill(ctx, o.ID)
	if err != nil {
		t.Fatalf("fulfill: %v", err)
	}
	if err := fx.sh.CreditWallet(ctx, fx.opID, buyer, 5000); err != nil {
		t.Fatalf("stock the buyer's wallet: %v", err)
	}

	fake.push(updText(1, seller, seller, "Seller", "/start"))
	waitFor(t, "the seller panel", func() bool { return fake.anySaid("sp:buyers") })
	fake.push(updCallback(2, seller, seller, "Seller", 5002, "sp:buyers"))
	waitFor(t, "the buyers list", func() bool { return fake.anySaid("مشتری‌های شما") })
	fake.push(updCallback(3, seller, seller, "Seller", 5002, fmt.Sprintf("sp:buy:%d", client.ID)))
	waitFor(t, "the buyer detail with the wallet line", func() bool {
		return fake.anySaid("موجودی کیف پول") && fake.anySaid(formatPrice(5000))
	})
}
