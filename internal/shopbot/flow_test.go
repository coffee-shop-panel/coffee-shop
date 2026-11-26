package shopbot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/abolfazl/w-ui/internal/shop"
)

// TestPaidButtonRevisitDoesNotDeliverTwice: the buyer's "I paid" button
// pressed again on an order that is already settled -- the payment page
// revisited, the button tapped twice -- must not ask the gateway again
// and must not settle again. The gateway answers an authority it
// already verified with 101, which a re-check reads as paid, so a
// second verification would mark the delivered order paid a second
// time and Fulfill would credit the top-up again. The settled order
// short-circuits to delivery instead, and delivery of a fulfilled
// order changes nothing.
func TestPaidButtonRevisitDoesNotDeliverTwice(t *testing.T) {
	fx := newShopFixture(t)
	ctx := context.Background()

	if err := fx.sh.SaveConfig(ctx, &shop.Config{
		OperatorID: fx.opID, Enabled: true, Token: "TESTTOKEN",
		AdminChatID: 999, ZarinpalMerchant: "merchant-1",
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}

	// The gateway, scripted to answer that this authority was already
	// verified -- code 101, which a re-check reads as paid -- and to
	// count every time it is asked.
	var verifyCalls atomic.Int32
	z := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"code":101,"message":"Verified","ref_id":555001},"errors":[]}`)
	}))
	t.Cleanup(z.Close)
	useZarinpal(t, z)

	fake, srv := newFakeTG(t)
	oldBase := APIBase
	APIBase = srv.URL
	t.Cleanup(func() { APIBase = oldBase })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := New(fx.sh, fx.clients, quietLog())
	mgr.Start(ctx)
	waitFor(t, "the bot to be running under its username", func() bool {
		name, running := mgr.BotInfo(fx.opID)
		return running && name == "shop_bot"
	})

	const buyer = 111
	order := &shop.Order{
		OperatorID: fx.opID, BuyerTGID: buyer, BuyerName: "Buyer",
		PlanName: "شارژ کیف پول", PriceToman: 50000,
		Kind: shop.KindTopup, Method: shop.MethodZarinpal, Status: shop.StatusPaid,
		Authority: "A1",
	}
	if err := fx.sh.CreateOrder(ctx, order); err != nil {
		t.Fatal(err)
	}
	// The first delivery, as the first press would have made it.
	if _, err := fx.sh.Fulfill(ctx, order.ID); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	balance := func() int64 {
		wallet, err := fx.sh.WalletFor(ctx, fx.opID, buyer)
		if err != nil {
			t.Fatal(err)
		}
		return wallet.BalanceToman
	}
	if got := balance(); got != 50000 {
		t.Fatalf("after the first delivery the wallet holds %d, want 50000", got)
	}

	// The "I paid" button again, twice: a double tap, or the buyer
	// returning to the payment message.
	check := fmt.Sprintf("check:%d", order.ID)
	fake.push(updCallback(1, buyer, buyer, "Buyer", 5001, check))
	fake.push(updCallback(2, buyer, buyer, "Buyer", 5001, check))
	waitFor(t, "both presses to be answered with the delivery", func() bool {
		return fake.countTo(buyer, "کیف پول شما شارژ شد") == 2
	})

	if got := balance(); got != 50000 {
		t.Errorf("the wallet holds %d after the repeated presses, want 50000: the top-up was credited twice", got)
	}
	if got := verifyCalls.Load(); got != 0 {
		t.Errorf("the gateway was asked %d times about an order already settled, want 0", got)
	}
	done, err := fx.sh.OrderByID(ctx, order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != shop.StatusFulfilled {
		t.Errorf("the order is %q after the repeated presses, want fulfilled", done.Status)
	}
}
