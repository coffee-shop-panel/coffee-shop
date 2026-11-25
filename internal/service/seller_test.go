package service

import (
	"testing"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// The seller a subscription page names: a reseller's customer is sent to
// the reseller -- under the name they sell with, or their username when
// they never set one -- and nobody else's customer is. The owner's
// customers keep the panel's support address however the owner has
// filled their own fields in, and so do a reseller's whose reseller
// never set a contact.
func TestSellerForNamesOnlyASellersCustomersSeller(t *testing.T) {
	ctx := t.Context()
	db := testDB(t)
	subs := NewSubscriptions(db, nil, nil, quietLog())

	owner := model.Admin{Username: "owner", Role: model.RoleOwner,
		ShopName: "The Panel Shop", SupportContact: "@panel_support"}
	branded := model.Admin{Username: "reza", Role: model.RoleReseller,
		ShopName: "Reza VPN", SupportContact: "@reza_support"}
	unnamed := model.Admin{Username: "sara", Role: model.RoleReseller,
		SupportContact: "https://t.me/sara_help"}
	silent := model.Admin{Username: "quiet", Role: model.RoleReseller,
		ShopName: "Quiet Shop"}
	for _, a := range []*model.Admin{&owner, &branded, &unnamed, &silent} {
		if err := db.Create(a).Error; err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name      string
		ownerID   uint
		wantName  string
		wantReach string
	}{
		{"the owner's own customer", owner.ID, "", ""},
		{"a branded reseller's", branded.ID, "Reza VPN", "@reza_support"},
		{"an unnamed reseller's, by username", unnamed.ID, "sara", "https://t.me/sara_help"},
		{"a reseller with a name but no contact", silent.ID, "", ""},
		{"a customer with no operator", 0, "", ""},
		{"a customer whose operator is gone", 9999, "", ""},
	}
	for _, c := range cases {
		gotName, gotReach := subs.sellerFor(ctx, &model.Client{OwnerID: c.ownerID})
		if gotName != c.wantName || gotReach != c.wantReach {
			t.Errorf("%s: sellerFor = (%q, %q), want (%q, %q)",
				c.name, gotName, gotReach, c.wantName, c.wantReach)
		}
	}
}
