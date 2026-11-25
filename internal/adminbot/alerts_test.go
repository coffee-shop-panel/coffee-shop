package adminbot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// The alerts, walked the way the buyer reminders are walked in the
// shop's suite: the scan is driven directly -- scanAlerts and
// scanCustomerAlerts take their clock from the caller, which is
// what makes "exactly once, then again after a renewal" assertable
// without waiting for a ticker. What is asserted is what the
// operator was told and what the notices table ended up holding --
// above all that one operator is never told about another's
// customers.

// seedAlertCustomer writes one customer straight into the panel's
// book under an operator, with the standing the caller describes:
// the alert scan reads customers, it does not care how they came to
// be, and seeding the row is the honest way to stand one at the
// edge of its plan -- the service's create cannot make an expired
// one, and the scan's bands are read off the row as it stands.
func seedAlertCustomer(t *testing.T, fx *flowFixture, ownerID uint, name string, mutate func(*model.Client)) *model.Client {
	t.Helper()
	c := &model.Client{
		Name: name, Protocol: model.ProtocolWireGuard,
		OwnerID: ownerID, Status: model.StatusActive,
	}
	if mutate != nil {
		mutate(c)
	}
	if err := fx.db.Create(c).Error; err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	return c
}

// countSentTo counts what one chat was sent that holds sub.
func countSentTo(f *fakeAdminTG, chatID int64, sub string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.sent {
		if m.chatID == chatID && strings.Contains(m.flat, sub) {
			n++
		}
	}
	return n
}

func alertNoticeCount(t *testing.T, fx *flowFixture, kind string) int64 {
	t.Helper()
	var n int64
	if err := fx.db.Model(&model.ShopNotice{}).Where("kind = ?", kind).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// TestAlertsFireOncePerOccasion is the heart of the alerts: a plan
// ending in two days earns exactly one op_expiry3 across two scans;
// a plan nine-tenths spent earns one op_volume90; and a renewal --
// a new end date, a new stamp -- earns a fresh alert when its own
// last days come. The operator is told the customer's name and how
// long is left.
func TestAlertsFireOncePerOccasion(t *testing.T) {
	fx := newFlowFixture(t)
	fake := newFakeAdminTG(t)
	ctx := context.Background()
	b := fx.bot(t, int64(fx.seller.ID), ownerChat)

	now := time.Now().UTC()
	expiry := now.Add(48 * time.Hour)
	cust := seedAlertCustomer(t, fx, fx.seller.ID, "kamran", func(c *model.Client) {
		c.ExpiresAt = &expiry
		c.QuotaBytes = 100
		c.UsedBytes = 95 // 95% spent
	})

	b.scanCustomerAlerts(ctx, now)
	if n := countSentTo(fake, ownerChat, "رو به پایان است"); n != 1 {
		t.Fatalf("the operator got %d expiry alerts, want 1", n)
	}
	if !fake.saidTo(ownerChat, "kamran") {
		t.Error("the alert does not name the customer")
	}
	if !fake.saidTo(ownerChat, "۲ روز مانده") {
		t.Error("the alert does not say how long is left")
	}
	if n := countSentTo(fake, ownerChat, "به سقف حجمش نزدیک شده است"); n != 1 {
		t.Fatalf("the operator got %d volume alerts, want 1", n)
	}
	if !fake.saidTo(ownerChat, "۹۵٪") {
		t.Error("the volume alert does not carry the spent figure")
	}

	// A second scan at the same moment says nothing again.
	b.scanCustomerAlerts(ctx, now)
	if n := countSentTo(fake, ownerChat, "رو به پایان است"); n != 1 {
		t.Errorf("after a second scan the operator holds %d expiry alerts, want still 1", n)
	}
	if got := alertNoticeCount(t, fx, alertExpiry3); got != 1 {
		t.Errorf("the notices table holds %d op_expiry3 rows, want 1", got)
	}
	if got := alertNoticeCount(t, fx, alertVolume90); got != 1 {
		t.Errorf("the notices table holds %d op_volume90 rows, want 1", got)
	}

	// The buyer reminders' kinds stay untouched: the same ending
	// told to the seller is not the reminder told to the buyer.
	if got := alertNoticeCount(t, fx, "expiry3"); got != 0 {
		t.Errorf("the notices table holds %d buyer-reminder rows, want 0", got)
	}

	// The last day earns its own, sterner alert.
	b.scanCustomerAlerts(ctx, now.Add(36*time.Hour))
	if got := alertNoticeCount(t, fx, alertExpiry1); got != 1 {
		t.Errorf("the notices table holds %d op_expiry1 rows, want 1", got)
	}

	// Renewal: the end date moves out, and when the new ending's
	// own last three days come, a fresh op_expiry3 arrives -- the
	// stamp is the date, and the date is new.
	newExpiry := now.Add(10 * 24 * time.Hour)
	if err := fx.db.Model(&model.Client{}).Where("id = ?", cust.ID).
		Update("expires_at", newExpiry).Error; err != nil {
		t.Fatal(err)
	}
	b.scanCustomerAlerts(ctx, now.Add(8*24*time.Hour))
	if n := countSentTo(fake, ownerChat, "رو به پایان است"); n != 3 {
		t.Errorf("the renewed customer earned %d expiry alerts in total, want 3 (old expiry3, expiry1, fresh expiry3)", n)
	}
	if got := alertNoticeCount(t, fx, alertExpiry3); got != 2 {
		t.Errorf("the notices table holds %d op_expiry3 rows, want 2 (one per ending)", got)
	}
}

// TestAlertSelectionPicksExactlyTheDue: the bands, one customer per
// edge. Five days out is silence; two days is expiry3; twelve hours
// is expiry1 alone, not both; an ended plan is named only when its
// standing says it ended; nine-tenths spent is volume, half is not.
func TestAlertSelectionPicksExactlyTheDue(t *testing.T) {
	fx := newFlowFixture(t)
	fake := newFakeAdminTG(t)
	ctx := context.Background()
	b := fx.bot(t, int64(fx.seller.ID), ownerChat)

	now := time.Now().UTC()
	at := func(d time.Duration) *time.Time { t := now.Add(d); return &t }

	seedAlertCustomer(t, fx, fx.seller.ID, "far-away", func(c *model.Client) {
		c.ExpiresAt = at(5 * 24 * time.Hour)
	})
	seedAlertCustomer(t, fx, fx.seller.ID, "two-days", func(c *model.Client) {
		c.ExpiresAt = at(48 * time.Hour)
	})
	seedAlertCustomer(t, fx, fx.seller.ID, "last-hours", func(c *model.Client) {
		c.ExpiresAt = at(12 * time.Hour)
	})
	seedAlertCustomer(t, fx, fx.seller.ID, "ended", func(c *model.Client) {
		c.ExpiresAt = at(-24 * time.Hour)
		c.Status = model.StatusExpired
	})
	// A past date on a customer still standing active is the
	// reconciler's business, not an alert.
	seedAlertCustomer(t, fx, fx.seller.ID, "past-but-active", func(c *model.Client) {
		c.ExpiresAt = at(-24 * time.Hour)
	})
	seedAlertCustomer(t, fx, fx.seller.ID, "nearly-spent", func(c *model.Client) {
		c.QuotaBytes = 100
		c.UsedBytes = 95
	})
	seedAlertCustomer(t, fx, fx.seller.ID, "half-spent", func(c *model.Client) {
		c.QuotaBytes = 100
		c.UsedBytes = 50
	})
	seedAlertCustomer(t, fx, fx.seller.ID, "no-limits", nil)

	b.scanCustomerAlerts(ctx, now)

	if n := fake.sentCount(); n != 4 {
		t.Fatalf("the operator was sent %d alerts, want 4 (expiry3, expiry1, expired, volume)", n)
	}
	for _, name := range []string{"two-days", "last-hours", "ended", "nearly-spent"} {
		if !fake.saidTo(ownerChat, name) {
			t.Errorf("no alert names %q", name)
		}
	}
	for _, name := range []string{"far-away", "past-but-active", "half-spent", "no-limits"} {
		if fake.saidTo(ownerChat, name) {
			t.Errorf("an alert names %q, who is due none", name)
		}
	}
	for kind, want := range map[string]int64{
		alertExpiry3: 1, alertExpiry1: 1, alertExpired: 1, alertVolume90: 1,
	} {
		if got := alertNoticeCount(t, fx, kind); got != want {
			t.Errorf("the notices table holds %d %s rows, want %d", got, kind, want)
		}
	}
}

// TestAlertsStayWithTheirOwnOperator: two resellers, two bots, and
// the manager's scan over both -- each operator is told about their
// own customers only, in their own chat, and the other operator's
// names never cross.
func TestAlertsStayWithTheirOwnOperator(t *testing.T) {
	fx := newFlowFixture(t)
	fake := newFakeAdminTG(t)
	ctx := context.Background()

	sara := &model.Admin{Username: "sara", PasswordHash: "x", Role: model.RoleReseller, Enabled: true}
	if err := fx.db.Create(sara).Error; err != nil {
		t.Fatalf("seed second reseller: %v", err)
	}

	const saraChat = 888
	fx.mgr.bots[fx.seller.ID] = fx.bot(t, int64(fx.seller.ID), ownerChat)
	fx.mgr.bots[sara.ID] = fx.bot(t, int64(sara.ID), saraChat)

	now := time.Now().UTC()
	expiry := now.Add(48 * time.Hour)
	seedAlertCustomer(t, fx, fx.seller.ID, "hussains-own", func(c *model.Client) {
		c.ExpiresAt = &expiry
	})
	seedAlertCustomer(t, fx, sara.ID, "saras-own", func(c *model.Client) {
		c.ExpiresAt = &expiry
	})

	fx.mgr.scanAlerts(ctx, now)

	if n := countSentTo(fake, ownerChat, "رو به پایان است"); n != 1 {
		t.Errorf("hussain got %d expiry alerts, want 1", n)
	}
	if n := countSentTo(fake, saraChat, "رو به پایان است"); n != 1 {
		t.Errorf("sara got %d expiry alerts, want 1", n)
	}
	if fake.saidTo(ownerChat, "saras-own") {
		t.Error("hussain was told about sara's customer")
	}
	if fake.saidTo(saraChat, "hussains-own") {
		t.Error("sara was told about hussain's customer")
	}
	if got := alertNoticeCount(t, fx, alertExpiry3); got != 2 {
		t.Errorf("the notices table holds %d op_expiry3 rows, want 2 (one per customer)", got)
	}
}

// TestAlertsSkipOperatorsWithoutABot: a reseller whose customers are
// due but who has no management bot running is simply not alerted --
// no messages, no notice rows -- and a bot whose operator never
// named their chat has nobody to tell.
func TestAlertsSkipOperatorsWithoutABot(t *testing.T) {
	fx := newFlowFixture(t)
	fake := newFakeAdminTG(t)
	ctx := context.Background()

	now := time.Now().UTC()
	expiry := now.Add(48 * time.Hour)
	seedAlertCustomer(t, fx, fx.seller.ID, "due-but-botless", func(c *model.Client) {
		c.ExpiresAt = &expiry
	})

	// No bot in the fleet at all.
	fx.mgr.scanAlerts(ctx, now)
	if n := fake.sentCount(); n != 0 {
		t.Fatalf("a botless operator was sent %d alerts, want none", n)
	}

	// A bot whose chat was never named.
	chatless := fx.bot(t, int64(fx.seller.ID), 0)
	chatless.scanCustomerAlerts(ctx, now)
	if n := fake.sentCount(); n != 0 {
		t.Fatalf("a chatless bot sent %d alerts, want none", n)
	}
	if got := alertNoticeCount(t, fx, alertExpiry3); got != 0 {
		t.Errorf("the notices table holds %d op_expiry3 rows, want 0", got)
	}
}

// TestAlertToABlockedChatIsRecorded: an operator who blocked their
// own bot cannot be alerted, ever -- the scan records the attempt
// as sent rather than retrying a dead chat every half hour.
func TestAlertToABlockedChatIsRecorded(t *testing.T) {
	fx := newFlowFixture(t)
	fake := newFakeAdminTG(t)
	ctx := context.Background()
	b := fx.bot(t, int64(fx.seller.ID), ownerChat)

	now := time.Now().UTC()
	expiry := now.Add(48 * time.Hour)
	fake.blocked[ownerChat] = true
	seedAlertCustomer(t, fx, fx.seller.ID, "unreachable", func(c *model.Client) {
		c.ExpiresAt = &expiry
	})

	b.scanCustomerAlerts(ctx, now)
	if n := countSentTo(fake, ownerChat, "رو به پایان است"); n != 0 {
		t.Errorf("the blocked chat received %d alerts, want none delivered", n)
	}
	if got := alertNoticeCount(t, fx, alertExpiry3); got != 1 {
		t.Errorf("the blocked alert left %d notice rows, want 1 (recorded, not retried)", got)
	}
}
