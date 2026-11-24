package shopbot

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// The shop's storefront button: the buyer's menu carries a «فروشگاه»
// web_app button when the panel has a public address for the page to
// live at, and does not offer a door to nowhere when it has not.

// With a public base set, the welcome's menu opens this shop's page.
func TestTheMenuCarriesTheShopAppButtonWithAPublicBase(t *testing.T) {
	fx := newShopFixture(t)
	ctx := context.Background()
	if err := fx.sh.SetPublicBase(ctx, "https://shop.example.com:12489"); err != nil {
		t.Fatalf("set the public base: %v", err)
	}
	fake, _, _ := runBot(t, fx)

	const buyer = 111
	fake.push(updText(1, buyer, buyer, "Buyer", "/start"))
	want := fmt.Sprintf(`"web_app":{"url":"https://shop.example.com:12489/shop/app?op=%d"}`, fx.opID)
	waitFor(t, "the shop button in the menu", func() bool { return fake.anySaid(want) })
	waitFor(t, "the shop button's label", func() bool { return fake.saidTo(buyer, btnShopApp) })
}

// Without one, no message the bot sends names a web app at all.
func TestTheMenuHasNoShopAppButtonWithoutAPublicBase(t *testing.T) {
	fx := newShopFixture(t)
	fake, _, _ := runBot(t, fx)

	const buyer = 111
	fake.push(updText(1, buyer, buyer, "Buyer", "/start"))
	waitFor(t, "the welcome", func() bool { return fake.saidTo(buyer, "خوش آمدید") })
	if strings.Contains(fake.chatText(buyer), "web_app") {
		t.Error("the menu offered a web app with no public base set")
	}
}
