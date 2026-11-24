package shop

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// sellingShop saves a bot configuration for the seller over the given
// tunnels, with the caller's adjustments applied first.
func sellingShop(t *testing.T, f *fixture, seller *model.Admin, ifaces []uint, mutate func(*Config)) *Config {
	t.Helper()
	cfg := &Config{
		OperatorID: seller.ID, Enabled: true, Token: "1:x",
		InterfaceIDs: ifaces,
	}
	if mutate != nil {
		mutate(cfg)
	}
	if err := f.shop.SaveConfig(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func balanceOf(t *testing.T, f *fixture, op uint, tg int64) int64 {
	t.Helper()
	w, err := f.shop.WalletFor(context.Background(), op, tg)
	if err != nil {
		t.Fatal(err)
	}
	return w.BalanceToman
}

func clientsOf(t *testing.T, f *fixture, tg int64) []model.Client {
	t.Helper()
	var rows []model.Client
	if err := f.db.Where("telegram_id = ?", tg).Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestWalletCreditDebitAndRefusals(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)

	w, err := f.shop.WalletFor(ctx, seller.ID, 111)
	if err != nil {
		t.Fatalf("WalletFor: %v", err)
	}
	if w.BalanceToman != 0 || w.ReferrerTGID != 0 || w.TrialUsedAt != nil {
		t.Errorf("a fresh wallet is %+v, want empty and unbound", w)
	}
	// Another operator's shop is another wallet for the same buyer.
	other := addOperator(t, f.db, "other", model.RoleReseller, 0)
	if w2, err := f.shop.WalletFor(ctx, other.ID, 111); err != nil || w2.ID == w.ID {
		t.Errorf("the same buyer at another shop got wallet %+v, %v; want their own", w2, err)
	}

	if err := f.shop.CreditWallet(ctx, seller.ID, 111, 500); err != nil {
		t.Fatalf("CreditWallet: %v", err)
	}
	if got := balanceOf(t, f, seller.ID, 111); got != 500 {
		t.Errorf("balance is %d, want 500", got)
	}
	if err := f.shop.DebitWallet(ctx, seller.ID, 111, 200); err != nil {
		t.Fatalf("DebitWallet: %v", err)
	}
	if got := balanceOf(t, f, seller.ID, 111); got != 300 {
		t.Errorf("balance is %d, want 300", got)
	}
	// A debit past the balance is refused and moves nothing.
	if err := f.shop.DebitWallet(ctx, seller.ID, 111, 301); err == nil {
		t.Error("a debit of 301 against 300 was allowed")
	}
	if got := balanceOf(t, f, seller.ID, 111); got != 300 {
		t.Errorf("balance after the refused debit is %d, want 300 unmoved", got)
	}
	// Neither direction moves zero or less.
	for _, amount := range []int64{0, -50} {
		if err := f.shop.CreditWallet(ctx, seller.ID, 111, amount); err == nil {
			t.Errorf("a credit of %d was allowed", amount)
		}
		if err := f.shop.DebitWallet(ctx, seller.ID, 111, amount); err == nil {
			t.Errorf("a debit of %d was allowed", amount)
		}
	}
}

func TestBindReferralRules(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	referrerOf := func(tg int64) int64 {
		w, err := f.shop.WalletFor(ctx, seller.ID, tg)
		if err != nil {
			t.Fatal(err)
		}
		return w.ReferrerTGID
	}

	// A buyer cannot refer themselves, and a stranger to the shop --
	// no wallet, no delivered order -- refers nobody. Both are quiet.
	if err := f.shop.BindReferral(ctx, seller.ID, 111, 111); err != nil {
		t.Fatalf("BindReferral self: %v", err)
	}
	if err := f.shop.BindReferral(ctx, seller.ID, 111, 999); err != nil {
		t.Fatalf("BindReferral stranger: %v", err)
	}
	if got := referrerOf(111); got != 0 {
		t.Errorf("referrer is %d, want nobody bound yet", got)
	}

	// A participant binds: 222 holds a wallet at this shop.
	if _, err := f.shop.WalletFor(ctx, seller.ID, 222); err != nil {
		t.Fatal(err)
	}
	if err := f.shop.BindReferral(ctx, seller.ID, 111, 222); err != nil {
		t.Fatalf("BindReferral: %v", err)
	}
	if got := referrerOf(111); got != 222 {
		t.Errorf("referrer is %d, want 222", got)
	}
	// And once bound, no second name overwrites the first.
	if _, err := f.shop.WalletFor(ctx, seller.ID, 333); err != nil {
		t.Fatal(err)
	}
	if err := f.shop.BindReferral(ctx, seller.ID, 111, 333); err != nil {
		t.Fatalf("BindReferral again: %v", err)
	}
	if got := referrerOf(111); got != 222 {
		t.Errorf("referrer after a second binding is %d, want 222 still", got)
	}

	// A delivered order also makes a participant: 444 has never held
	// a wallet here, but the shop has delivered to them.
	row := &model.ShopOrder{
		OperatorID: seller.ID, BuyerTGID: 444, BuyerName: "Old Buyer",
		Kind: KindNew, Method: MethodCard, Status: StatusFulfilled,
		PublicToken: "tok-for-444",
	}
	if err := f.db.Create(row).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.shop.BindReferral(ctx, seller.ID, 555, 444); err != nil {
		t.Fatalf("BindReferral by order: %v", err)
	}
	if got := referrerOf(555); got != 444 {
		t.Errorf("referrer is %d, want 444 (bound by their delivered order)", got)
	}
}

func TestConfigGrowthFieldsRoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)

	want := &Config{
		OperatorID: seller.ID, Enabled: true, Token: "1:x",
		TrialEnabled: true, TrialBytes: 1 << 30, TrialDays: 7,
		TrialSection: SectionV2ray, NameFormat: "{name}-{n}",
		CashbackPercent: 15, WheelEnabled: true,
	}
	if err := f.shop.SaveConfig(ctx, want); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, err := f.shop.ConfigFor(ctx, seller.ID)
	if err != nil {
		t.Fatalf("ConfigFor: %v", err)
	}
	if !got.TrialEnabled || got.TrialBytes != 1<<30 || got.TrialDays != 7 ||
		got.TrialSection != SectionV2ray || got.NameFormat != "{name}-{n}" ||
		got.CashbackPercent != 15 || !got.WheelEnabled {
		t.Errorf("growth fields read back as %+v, want %+v", got, want)
	}
}

func TestFormatClientName(t *testing.T) {
	plain := &Config{}
	if got := formatClientName(plain, "Ali", 42, 1); got != "Ali" {
		t.Errorf("no format: got %q, want the buyer's name", got)
	}
	if got := formatClientName(nil, "", 42, 1); got != "TG-42" {
		t.Errorf("no name and no format: got %q, want TG-42", got)
	}
	numbered := &Config{NameFormat: "{name} #{n}"}
	if got := formatClientName(numbered, "Ali", 42, 3); got != "Ali #3" {
		t.Errorf("{name} and {n}: got %q, want %q", got, "Ali #3")
	}
	byID := &Config{NameFormat: "user-{id}"}
	if got := formatClientName(byID, "Ali", 42, 1); got != "user-42" {
		t.Errorf("{id}: got %q, want user-42", got)
	}
	random := &Config{NameFormat: "{rand}"}
	got := formatClientName(random, "Ali", 42, 1)
	if len(got) != 4 {
		t.Errorf("{rand}: got %q, want four characters", got)
	}
	for _, r := range got {
		if !strings.ContainsRune(nameRandAlphabet, r) {
			t.Errorf("{rand}: %q holds %q, outside the alphabet", got, r)
		}
	}
	// A name past the panel's ceiling is cut to it, not refused later
	// by the create the name was made for.
	long := formatClientName(plain, strings.Repeat("a", 300), 42, 1)
	if n := len([]rune(long)); n != maxNameRunes {
		t.Errorf("a 300-rune name was capped to %d runes, want %d", n, maxNameRunes)
	}
}

// The panel's name format reaches the customer a sale creates: the
// counter in it is where the customer stands in the seller's book.
func TestFulfillNamesFromFormat(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	grant(t, f, seller.ID, f.ifaces[0])
	sellingShop(t, f, seller, []uint{f.ifaces[0].ID}, func(c *Config) {
		c.NameFormat = "{name}-{n}"
	})

	first := paidOrder(seller.ID, 111, KindNew)
	if err := f.shop.CreateOrder(ctx, first); err != nil {
		t.Fatal(err)
	}
	client, err := f.shop.Fulfill(ctx, first.ID)
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if client.Name != "Buyer-1" {
		t.Errorf("the first customer is named %q, want Buyer-1", client.Name)
	}

	second := paidOrder(seller.ID, 222, KindNew)
	second.BuyerName = "Sara"
	if err := f.shop.CreateOrder(ctx, second); err != nil {
		t.Fatal(err)
	}
	client, err = f.shop.Fulfill(ctx, second.ID)
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if client.Name != "Sara-2" {
		t.Errorf("the second customer is named %q, want Sara-2", client.Name)
	}
}

// A top-up delivers money, not a customer -- and a double delivery
// credits it once.
func TestTopupFulfillCreditsOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)

	o := &Order{
		OperatorID: seller.ID, BuyerTGID: 111, BuyerName: "Buyer",
		Kind: KindTopup, Method: MethodWallet, PriceToman: 250,
		Status: StatusPaid,
	}
	if err := f.shop.CreateOrder(ctx, o); err != nil {
		t.Fatal(err)
	}
	client, err := f.shop.Fulfill(ctx, o.ID)
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if client != nil {
		t.Errorf("a top-up handed back customer %+v, want none", client)
	}
	if got := balanceOf(t, f, seller.ID, 111); got != 250 {
		t.Errorf("balance is %d, want the top-up's 250", got)
	}
	after, err := f.shop.OrderByID(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusFulfilled || after.ClientID != 0 {
		t.Errorf("order is %s with client %d, want fulfilled with none", after.Status, after.ClientID)
	}
	if rows := clientsOf(t, f, 111); len(rows) != 0 {
		t.Errorf("a top-up made %d customers, want none", len(rows))
	}

	if _, err := f.shop.Fulfill(ctx, o.ID); err != nil {
		t.Fatalf("Fulfill again: %v", err)
	}
	if got := balanceOf(t, f, seller.ID, 111); got != 250 {
		t.Errorf("balance after a second delivery is %d, want 250 still", got)
	}
}

// Cashback: a referred buyer's delivered sale pays the shop's percent
// of what was actually paid into the referrer's wallet -- once, however
// many times the delivery is repeated, and never for a top-up.
func TestCashbackLandsOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	grant(t, f, seller.ID, f.ifaces[0])
	sellingShop(t, f, seller, []uint{f.ifaces[0].ID}, func(c *Config) {
		c.CashbackPercent = 10
	})
	if _, err := f.shop.WalletFor(ctx, seller.ID, 222); err != nil {
		t.Fatal(err)
	}
	if err := f.shop.BindReferral(ctx, seller.ID, 111, 222); err != nil {
		t.Fatal(err)
	}

	// 90 list, 10 taken off by a code: 80 paid, a tenth of it is 8.
	o := paidOrder(seller.ID, 111, KindNew)
	o.DiscountToman = 10
	if err := f.shop.CreateOrder(ctx, o); err != nil {
		t.Fatal(err)
	}
	if _, err := f.shop.Fulfill(ctx, o.ID); err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if got := balanceOf(t, f, seller.ID, 222); got != 8 {
		t.Errorf("the referrer holds %d, want 8 (10%% of the 80 paid)", got)
	}
	if _, err := f.shop.Fulfill(ctx, o.ID); err != nil {
		t.Fatalf("Fulfill again: %v", err)
	}
	if got := balanceOf(t, f, seller.ID, 222); got != 8 {
		t.Errorf("the referrer holds %d after a second delivery, want 8 still", got)
	}

	// A top-up from the same buyer is money moving, not a sale.
	top := &Order{
		OperatorID: seller.ID, BuyerTGID: 111, BuyerName: "Buyer",
		Kind: KindTopup, Method: MethodCard, PriceToman: 100,
		Status: StatusPaid,
	}
	if err := f.shop.CreateOrder(ctx, top); err != nil {
		t.Fatal(err)
	}
	if _, err := f.shop.Fulfill(ctx, top.ID); err != nil {
		t.Fatalf("Fulfill top-up: %v", err)
	}
	if got := balanceOf(t, f, seller.ID, 222); got != 8 {
		t.Errorf("the referrer holds %d after the buyer's top-up, want 8 still", got)
	}
	if got := balanceOf(t, f, seller.ID, 111); got != 100 {
		t.Errorf("the buyer holds %d, want their top-up's 100", got)
	}
}

// The trial: a shop that offers one gives it once -- and a shop that
// does not, or a buyer who has had theirs, is refused.
func TestClaimTrialOnceAndRules(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	grant(t, f, seller.ID, f.ifaces[0], f.ifaces[1])
	sellingShop(t, f, seller, []uint{f.ifaces[0].ID}, func(c *Config) {
		c.TrialEnabled = true
		c.TrialBytes = 1 << 30
		c.TrialDays = 7
	})

	client, err := f.shop.ClaimTrial(ctx, seller.ID, 555, "Trial User")
	if err != nil {
		t.Fatalf("ClaimTrial: %v", err)
	}
	if client.OwnerID != seller.ID || client.TelegramID != 555 {
		t.Errorf("the trial customer is owner %d, telegram %d; want the seller's, 555",
			client.OwnerID, client.TelegramID)
	}
	if client.Name != "Trial User" {
		t.Errorf("the trial customer is named %q, want the buyer's name", client.Name)
	}
	if client.QuotaBytes != 1<<30 {
		t.Errorf("the trial quota is %d, want the configured %d", client.QuotaBytes, 1<<30)
	}
	// The trial asks for no device limit of its own (0), as a plan
	// sold with none does; the customers table turns that into its
	// own default of one device on insert, for trials and sales
	// alike.
	if client.DeviceLimit != 1 {
		t.Errorf("the trial device limit is %d, want the table's default of 1", client.DeviceLimit)
	}
	if client.ExpiresAt == nil {
		t.Fatal("a 7-day trial was given with no end date")
	}
	if until := time.Until(*client.ExpiresAt); until < 6*24*time.Hour || until > 8*24*time.Hour {
		t.Errorf("the trial ends in %v, want about 7 days", until)
	}
	w, err := f.shop.WalletFor(ctx, seller.ID, 555)
	if err != nil {
		t.Fatal(err)
	}
	if w.TrialUsedAt == nil {
		t.Error("the wallet does not record the claimed trial")
	}

	// Once is once.
	if _, err := f.shop.ClaimTrial(ctx, seller.ID, 555, "Trial User"); err == nil {
		t.Error("a second trial for the same buyer was given")
	}
	if rows := clientsOf(t, f, 555); len(rows) != 1 {
		t.Errorf("the buyer holds %d customers, want the one trial", len(rows))
	}

	// A shop with the trial switched off gives none, and the refusal
	// spends nobody's one claim.
	sellingShop(t, f, seller, []uint{f.ifaces[0].ID}, func(c *Config) {
		c.TrialEnabled = false
	})
	if _, err := f.shop.ClaimTrial(ctx, seller.ID, 556, "Late"); err == nil {
		t.Error("a trial was given by a shop that offers none")
	}
	sellingShop(t, f, seller, []uint{f.ifaces[0].ID}, func(c *Config) {
		c.TrialEnabled = true
		c.TrialBytes = 1 << 30
		c.TrialDays = 7
	})
	if _, err := f.shop.ClaimTrial(ctx, seller.ID, 556, "Late"); err != nil {
		t.Errorf("the refused claim spent the buyer's trial: %v", err)
	}
}
