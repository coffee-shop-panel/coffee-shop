package shopbot

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/scope"
	"github.com/abolfazl/w-ui/internal/shop"
)

// The v5 reminders, the wheel and the lottery, walked the way the
// earlier suites walk the shop. The reminder scan is driven
// directly -- scanReminders takes its clock from the caller, which
// is what makes "exactly once, then again after a renewal"
// assertable without waiting for a ticker.

// seedCustomer writes one customer straight into the panel's book
// under an operator, with the standing the caller describes: the
// reminder scan reads customers, it does not care how they came to
// be, and seeding the row is the honest way to stand one at the
// edge of its plan.
func seedCustomer(t *testing.T, fx *shopFixture, ownerID uint, tg int64, mutate func(*model.Client)) *model.Client {
	t.Helper()
	c := &model.Client{
		Name: "Customer", Protocol: model.ProtocolWireGuard,
		OwnerID: ownerID, TelegramID: tg, Status: model.StatusActive,
	}
	if mutate != nil {
		mutate(c)
	}
	if err := fx.db.Create(c).Error; err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	return c
}

// chatTextAll joins everything one chat was sent or had edited
// onto it: a panel screen the draw finishes is an edit, and a test
// that only read fresh messages would miss the result it shows.
func chatTextAll(f *fakeTG, chatID int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var sb strings.Builder
	for _, list := range [][]fakeMsg{f.sent, f.edits} {
		for _, m := range list {
			if m.chatID == chatID {
				sb.WriteString(m.flat)
				sb.WriteString("\n")
			}
		}
	}
	return sb.String()
}

func noticeCount(t *testing.T, fx *shopFixture, kind string) int64 {
	t.Helper()
	var n int64
	if err := fx.db.Model(&model.ShopNotice{}).Where("kind = ?", kind).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

// TestRemindersFireOncePerOccasion is the heart of item 16: a plan
// ending in two days earns exactly one expiry3 across two scans; a
// plan nine-tenths spent earns one volume90; and a renewal -- a new
// end date, a new stamp -- earns a fresh reminder when its own last
// days come.
func TestRemindersFireOncePerOccasion(t *testing.T) {
	fx := newShopFixture(t)
	fake, mgr, ctx := runBot(t, fx)
	if err := scope.Register(fx.db); err != nil {
		t.Fatalf("register scoping: %v", err)
	}

	now := time.Now().UTC()
	const buyer = 111
	expiry := now.Add(48 * time.Hour)
	cust := seedCustomer(t, fx, fx.opID, buyer, func(c *model.Client) {
		c.ExpiresAt = &expiry
		c.QuotaBytes = 100
		c.UsedBytes = 95 // 95% spent
	})

	mgr.scanReminders(ctx, now)
	if n := fake.countTo(buyer, "رو به پایان است"); n != 1 {
		t.Fatalf("the expiring customer got %d expiry reminders, want 1", n)
	}
	if n := fake.countTo(buyer, "رو به اتمام است"); n != 1 {
		t.Fatalf("the nearly-spent customer got %d volume reminders, want 1", n)
	}
	// The reminder carries its way back: the buy button.
	if !fake.saidTo(buyer, `"buy"`) && !fake.saidTo(buyer, "buy") {
		t.Error("the reminder carries no buy button")
	}

	// A second scan at the same moment says nothing again.
	mgr.scanReminders(ctx, now)
	if n := fake.countTo(buyer, "رو به پایان است"); n != 1 {
		t.Errorf("after a second scan the customer holds %d expiry reminders, want still 1", n)
	}
	if n := fake.countTo(buyer, "رو به اتمام است"); n != 1 {
		t.Errorf("after a second scan the customer holds %d volume reminders, want still 1", n)
	}
	if got := noticeCount(t, fx, noticeExpiry3); got != 1 {
		t.Errorf("the notices table holds %d expiry3 rows, want 1", got)
	}

	// The last day earns its own, sterner reminder.
	mgr.scanReminders(ctx, now.Add(36*time.Hour))
	if got := noticeCount(t, fx, noticeExpiry1); got != 1 {
		t.Errorf("the notices table holds %d expiry1 rows, want 1", got)
	}

	// Renewal: the end date moves out, and when the new ending's
	// own last three days come, a fresh expiry3 arrives -- the
	// stamp is the date, and the date is new.
	newExpiry := now.Add(10 * 24 * time.Hour)
	if err := fx.db.Model(&model.Client{}).Where("id = ?", cust.ID).
		Update("expires_at", newExpiry).Error; err != nil {
		t.Fatal(err)
	}
	mgr.scanReminders(ctx, now.Add(8*24*time.Hour))
	if n := fake.countTo(buyer, "رو به پایان است"); n != 3 {
		t.Errorf("the renewed customer holds %d expiry reminders, want 3 (old expiry3, expiry1, fresh expiry3)", n)
	}
	if got := noticeCount(t, fx, noticeExpiry3); got != 2 {
		t.Errorf("the notices table holds %d expiry3 rows, want 2 (one per ending)", got)
	}
}

// TestRemindersStayWithTheirOwnBot: two sellers, two bots, and a
// customer of the second -- the first seller's bot never speaks to
// them, because the scan walks each operator's own scope.
func TestRemindersStayWithTheirOwnBot(t *testing.T) {
	fx := newShopFixture(t)
	ctx := context.Background()

	// A second operator with a bot of their own.
	other := model.Admin{Username: "other-seller", PasswordHash: "unused", Role: model.RoleReseller, Enabled: true}
	if err := fx.db.Create(&other).Error; err != nil {
		t.Fatalf("seed second operator: %v", err)
	}
	if err := fx.sh.SaveConfig(ctx, &shop.Config{
		OperatorID: other.ID, Enabled: true, Token: "OTHERTOKEN", AdminChatID: 888,
	}); err != nil {
		t.Fatalf("save second config: %v", err)
	}

	fake, mgr, mctx := runBot(t, fx)
	if err := scope.Register(fx.db); err != nil {
		t.Fatalf("register scoping: %v", err)
	}
	waitFor(t, "both bots to run", func() bool {
		_, a := mgr.BotInfo(fx.opID)
		_, b := mgr.BotInfo(other.ID)
		return a && b
	})

	now := time.Now().UTC()
	expiry := now.Add(48 * time.Hour)
	const otherBuyer = 555
	seedCustomer(t, fx, other.ID, otherBuyer, func(c *model.Client) {
		c.ExpiresAt = &expiry
	})
	// And one of the first seller's own, to prove their bot scans.
	const ownBuyer = 556
	seedCustomer(t, fx, fx.opID, ownBuyer, func(c *model.Client) {
		c.ExpiresAt = &expiry
	})

	mgr.scanReminders(mctx, now)
	if n := fake.countTo(otherBuyer, "رو به پایان است"); n != 1 {
		t.Errorf("the second seller's customer got %d reminders, want exactly 1 (from their own bot only)", n)
	}
	if n := fake.countTo(ownBuyer, "رو به پایان است"); n != 1 {
		t.Errorf("the first seller's customer got %d reminders, want 1", n)
	}
	if got := noticeCount(t, fx, noticeExpiry3); got != 2 {
		t.Errorf("the notices table holds %d expiry3 rows, want 2 (one per customer)", got)
	}
}

// TestReminderToABlockedChatIsRecorded: a buyer who blocked the bot
// cannot be reminded, ever -- the scan records the attempt as sent
// rather than retrying a dead chat every half hour.
func TestReminderToABlockedChatIsRecorded(t *testing.T) {
	fx := newShopFixture(t)
	fake, mgr, ctx := runBot(t, fx)
	if err := scope.Register(fx.db); err != nil {
		t.Fatalf("register scoping: %v", err)
	}

	now := time.Now().UTC()
	expiry := now.Add(48 * time.Hour)
	const buyer = 777
	fake.blocked[buyer] = true
	seedCustomer(t, fx, fx.opID, buyer, func(c *model.Client) {
		c.ExpiresAt = &expiry
	})

	mgr.scanReminders(ctx, now)
	if n := fake.countTo(buyer, "رو به پایان است"); n != 0 {
		t.Errorf("the blocked chat received %d messages, want none delivered", n)
	}
	if got := noticeCount(t, fx, noticeExpiry3); got != 1 {
		t.Errorf("the blocked reminder left %d notice rows, want 1 (recorded, not retried)", got)
	}
}
