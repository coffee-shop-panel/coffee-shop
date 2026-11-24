package shop

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/ipam"
	"github.com/abolfazl/w-ui/internal/scope"
	"github.com/abolfazl/w-ui/internal/service"
)

// The fixtures mirror the service package's own: an in-memory database
// with the scope callbacks registered (without them nothing narrows, and
// the separation these tests exist to prove would not be exercised), a
// local node, and two WireGuard tunnels with address pools.

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type fixture struct {
	db      *gorm.DB
	clients *service.Clients
	shop    *Shop
	ifaces  []model.Interface
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := scope.Register(db); err != nil {
		t.Fatalf("register scoping: %v", err)
	}
	if err := db.Create(&model.Node{Name: "local", Kind: model.KindLocal}).Error; err != nil {
		t.Fatal(err)
	}
	pools := ipam.NewPools()
	f := &fixture{db: db}
	for i, name := range []string{"wg0", "wg1"} {
		subnet := "10.9." + string(rune('0'+i)) + ".0/24"
		iface := model.Interface{
			Name: name, Protocol: model.ProtocolWireGuard, Enabled: true,
			ListenPort: 51820 + i, Subnet: subnet, EndpointHost: "vpn.example.com",
			MTU: 1420, NodeID: 1,
		}
		if err := db.Create(&iface).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := pools.Add(iface.ID, subnet); err != nil {
			t.Fatal(err)
		}
		f.ifaces = append(f.ifaces, iface)
	}
	f.clients = service.NewClients(db, pools, quietLog())
	f.shop = New(db, f.clients, quietLog())
	return f
}

func addOperator(t *testing.T, db *gorm.DB, username string, role model.AdminRole, parent uint) *model.Admin {
	t.Helper()
	a := &model.Admin{
		Username: username, PasswordHash: "x", Role: role,
		ParentAdminID: parent, Enabled: true,
	}
	if err := db.Create(a).Error; err != nil {
		t.Fatalf("operator %s: %v", username, err)
	}
	return a
}

// grant records the tunnels the owner has assigned an operator -- the
// AdminInterface rows AllowedInterfaces reads. Without them a reseller
// may sell nothing at all, which is the panel's real rule; a fixture
// that skipped the rows would be testing a seller the owner never gave
// a tunnel to, not the seller these tests mean to build.
func grant(t *testing.T, f *fixture, adminID uint, ifaces ...model.Interface) {
	t.Helper()
	for _, iface := range ifaces {
		if err := f.db.Create(&model.AdminInterface{AdminID: adminID, InterfaceID: iface.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestConfigRoundTrip(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	grant(t, f, seller.ID, f.ifaces[0], f.ifaces[1])

	// No shop yet is a zero config, not an error.
	got, err := f.shop.ConfigFor(ctx, seller.ID)
	if err != nil {
		t.Fatalf("ConfigFor: %v", err)
	}
	if got.Enabled || got.Token != "" {
		t.Errorf("a fresh operator's config is %+v, want zero", got)
	}

	want := &Config{
		OperatorID: seller.ID, Enabled: true, Token: "123:abc",
		AdminChatID: 99, CardNumber: "6037", CardHolder: "Hussain",
		Welcome: "salam", InterfaceIDs: []uint{f.ifaces[0].ID, f.ifaces[1].ID},
	}
	if err := f.shop.SaveConfig(ctx, want); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, err = f.shop.ConfigFor(ctx, seller.ID)
	if err != nil {
		t.Fatalf("ConfigFor: %v", err)
	}
	if got.Token != want.Token || got.AdminChatID != 99 || len(got.InterfaceIDs) != 2 ||
		got.InterfaceIDs[0] != f.ifaces[0].ID || got.CardHolder != "Hussain" {
		t.Errorf("round trip gave %+v, want %+v", got, want)
	}

	// Saving again replaces the one row rather than adding another.
	want.Token = "456:def"
	if err := f.shop.SaveConfig(ctx, want); err != nil {
		t.Fatalf("SaveConfig again: %v", err)
	}
	all, err := f.shop.Configs(ctx)
	if err != nil {
		t.Fatalf("Configs: %v", err)
	}
	if len(all) != 1 || all[0].Token != "456:def" {
		t.Errorf("Configs = %+v, want the one updated row", all)
	}
}

func TestPlanCRUDAndCrossOperatorRefusal(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := addOperator(t, f.db, "a-seller", model.RoleReseller, 0)
	b := addOperator(t, f.db, "b-seller", model.RoleReseller, 0)
	grant(t, f, a.ID, f.ifaces[0])
	grant(t, f, b.ID, f.ifaces[1])

	p2 := &Plan{OperatorID: a.ID, Name: "big", QuotaBytes: 50 << 30, Days: 30, PriceToman: 200, DeviceLimit: 2, Enabled: true, Sort: 2}
	p1 := &Plan{OperatorID: a.ID, Name: "small", QuotaBytes: 10 << 30, Days: 30, PriceToman: 90, DeviceLimit: 1, Enabled: true, Sort: 1}
	off := &Plan{OperatorID: a.ID, Name: "off", QuotaBytes: 1 << 30, Days: 7, PriceToman: 10, Enabled: false, Sort: 3}
	for _, p := range []*Plan{p2, p1, off} {
		if err := f.shop.SavePlan(ctx, p); err != nil {
			t.Fatalf("SavePlan %s: %v", p.Name, err)
		}
	}

	plans, err := f.shop.PlansFor(ctx, a.ID, false)
	if err != nil {
		t.Fatalf("PlansFor: %v", err)
	}
	if len(plans) != 3 || plans[0].Name != "small" || plans[1].Name != "big" {
		t.Errorf("plans in offer order = %+v, want small, big, off", plans)
	}
	enabled, err := f.shop.PlansFor(ctx, a.ID, true)
	if err != nil {
		t.Fatalf("PlansFor enabled: %v", err)
	}
	if len(enabled) != 2 {
		t.Errorf("enabled plans = %d, want 2", len(enabled))
	}

	// Another operator editing or deleting A's plan is refused, and the
	// plan survives both attempts untouched.
	p1.OperatorID = b.ID
	if err := f.shop.SavePlan(ctx, p1); err == nil {
		t.Error("saving another operator's plan was allowed")
	}
	if err := f.shop.DeletePlan(ctx, b.ID, p1.ID); err == nil {
		t.Error("deleting another operator's plan was allowed")
	}
	still, err := f.shop.PlansFor(ctx, a.ID, false)
	if err != nil || len(still) != 3 {
		t.Errorf("after the refusals A holds %d plans (err %v), want 3", len(still), err)
	}

	if err := f.shop.DeletePlan(ctx, a.ID, off.ID); err != nil {
		t.Fatalf("DeletePlan: %v", err)
	}
	plans, _ = f.shop.PlansFor(ctx, a.ID, false)
	if len(plans) != 2 {
		t.Errorf("after delete A holds %d plans, want 2", len(plans))
	}

	if err := f.shop.SavePlan(ctx, &Plan{OperatorID: a.ID, Name: "bad", PriceToman: -1}); err == nil {
		t.Error("a negative price was allowed")
	}
	if err := f.shop.SavePlan(ctx, &Plan{OperatorID: a.ID, Name: "bad", Days: -3}); err == nil {
		t.Error("negative days were allowed")
	}
}

func paidOrder(op uint, tg int64, kind string) *Order {
	return &Order{
		OperatorID: op, BuyerTGID: tg, BuyerName: "Buyer",
		PlanName: "small", QuotaBytes: 20 << 30, Days: 30,
		PriceToman: 90, DeviceLimit: 2,
		Kind: kind, Method: MethodCard, Status: StatusPaid,
	}
}

func TestOrderLifecycleAndStats(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := addOperator(t, f.db, "a-seller", model.RoleReseller, 0)

	o := paidOrder(a.ID, 4242, KindNew)
	if err := f.shop.CreateOrder(ctx, o); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if o.ID == 0 || len(o.PublicToken) != 24 {
		t.Errorf("order id %d, token %q; want an id and a 24-char token", o.ID, o.PublicToken)
	}
	byToken, err := f.shop.OrderByToken(ctx, o.PublicToken)
	if err != nil || byToken.ID != o.ID {
		t.Errorf("OrderByToken = %+v, %v; want order %d", byToken, err, o.ID)
	}

	other := paidOrder(a.ID, 777, KindNew)
	other.Status = StatusUnpaid
	if err := f.shop.CreateOrder(ctx, other); err != nil {
		t.Fatalf("CreateOrder 2: %v", err)
	}

	paid, err := f.shop.OrdersFor(ctx, a.ID, StatusPaid, 10)
	if err != nil || len(paid) != 1 || paid[0].ID != o.ID {
		t.Errorf("paid orders = %+v, %v; want the one", paid, err)
	}
	all, err := f.shop.OrdersFor(ctx, a.ID, "", 10)
	if err != nil || len(all) != 2 || all[0].ID != other.ID {
		t.Errorf("all orders = %+v, %v; want newest first", all, err)
	}

	st, err := f.shop.StatsFor(ctx, a.ID)
	if err != nil {
		t.Fatalf("StatsFor: %v", err)
	}
	if st.Pending != 2 || st.TotalSales != 0 || st.RevenueToman != 0 || st.Buyers != 0 {
		t.Errorf("stats = %+v, want 2 pending and no sales yet", st)
	}
}

func TestFulfillCreatesTheSellersCustomer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	grant(t, f, seller.ID, f.ifaces[0], f.ifaces[1])

	if err := f.shop.SaveConfig(ctx, &Config{
		OperatorID: seller.ID, Enabled: true, Token: "1:x",
		InterfaceIDs: []uint{f.ifaces[0].ID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.shop.SetPublicBase(ctx, "https://panel.example:12489/"); err != nil {
		t.Fatal(err)
	}

	o := paidOrder(seller.ID, 4242, KindNew)
	if err := f.shop.CreateOrder(ctx, o); err != nil {
		t.Fatal(err)
	}
	client, err := f.shop.Fulfill(ctx, o.ID)
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if client.OwnerID != seller.ID {
		t.Errorf("customer's owner is %d, want the seller %d", client.OwnerID, seller.ID)
	}
	if client.TelegramID != 4242 {
		t.Errorf("customer's telegram id is %d, want 4242", client.TelegramID)
	}
	if client.QuotaBytes != 20<<30 {
		t.Errorf("quota is %d, want the plan's %d", client.QuotaBytes, 20<<30)
	}
	if client.DeviceLimit != 2 {
		t.Errorf("device limit is %d, want 2", client.DeviceLimit)
	}
	if client.ExpiresAt == nil {
		t.Fatal("a 30-day plan was delivered with no end date")
	}
	if until := time.Until(*client.ExpiresAt); until < 29*24*time.Hour || until > 31*24*time.Hour {
		t.Errorf("expiry is %v away, want about 30 days", until)
	}
	if len(client.Accounts) != 2 {
		t.Errorf("customer holds %d accounts, want one per device (2) on the bot's tunnel", len(client.Accounts))
	}
	for _, acc := range client.Accounts {
		if acc.InterfaceID != f.ifaces[0].ID {
			t.Errorf("an account sits on tunnel %d, want the config's %d", acc.InterfaceID, f.ifaces[0].ID)
		}
	}

	after, err := f.shop.OrderByID(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != StatusFulfilled || after.ClientID != client.ID {
		t.Errorf("order is %s with client %d, want fulfilled with %d", after.Status, after.ClientID, client.ID)
	}

	base, err := f.shop.PublicBase(ctx)
	if err != nil || base != "https://panel.example:12489" {
		t.Errorf("PublicBase = %q, %v; want the origin without its trailing slash", base, err)
	}
	if link := f.shop.SubLink(ctx, client); link != base+"/subscribe/"+client.SubToken {
		t.Errorf("SubLink = %q, want %q", link, base+"/subscribe/"+client.SubToken)
	}

	st, err := f.shop.StatsFor(ctx, seller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.TotalSales != 1 || st.TodaySales != 1 || st.RevenueToman != 90 || st.Buyers != 1 || st.Pending != 0 {
		t.Errorf("stats = %+v, want one sale of 90 to one buyer", st)
	}
}

// A double tap on approve must not become a double sale.
func TestFulfillIsIdempotent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	grant(t, f, seller.ID, f.ifaces[0], f.ifaces[1])
	if err := f.shop.SaveConfig(ctx, &Config{OperatorID: seller.ID, Enabled: true, InterfaceIDs: []uint{f.ifaces[0].ID}}); err != nil {
		t.Fatal(err)
	}
	o := paidOrder(seller.ID, 4242, KindNew)
	if err := f.shop.CreateOrder(ctx, o); err != nil {
		t.Fatal(err)
	}
	first, err := f.shop.Fulfill(ctx, o.ID)
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	second, err := f.shop.Fulfill(ctx, o.ID)
	if err != nil {
		t.Fatalf("Fulfill again: %v", err)
	}
	if second.ID != first.ID {
		t.Errorf("second delivery made customer %d, want the same %d", second.ID, first.ID)
	}
	if second.QuotaBytes != first.QuotaBytes {
		t.Errorf("quota moved on a re-delivery: %d then %d", first.QuotaBytes, second.QuotaBytes)
	}
	var count int64
	if err := f.db.Model(&model.Client{}).Where("telegram_id = ?", 4242).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("%d customers hold telegram id 4242, want 1", count)
	}
}

// The second purchase renews the customer the first one made -- even
// labelled "new", because what the buyer tapped is not what decides; one
// Telegram account is one customer under one seller.
func TestFulfillRenewsTheSameCustomer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	grant(t, f, seller.ID, f.ifaces[0], f.ifaces[1])
	if err := f.shop.SaveConfig(ctx, &Config{OperatorID: seller.ID, Enabled: true, InterfaceIDs: []uint{f.ifaces[0].ID}}); err != nil {
		t.Fatal(err)
	}
	first := paidOrder(seller.ID, 4242, KindNew)
	if err := f.shop.CreateOrder(ctx, first); err != nil {
		t.Fatal(err)
	}
	client, err := f.shop.Fulfill(ctx, first.ID)
	if err != nil {
		t.Fatalf("Fulfill: %v", err)
	}

	// The plan ran out: date passed, marked expired, some allowance spent.
	past := time.Now().UTC().Add(-time.Hour)
	if err := f.db.Model(&model.Client{}).Where("id = ?", client.ID).Updates(map[string]any{
		"expires_at": past, "status": model.StatusExpired, "used_bytes": 5 << 30,
	}).Error; err != nil {
		t.Fatal(err)
	}

	renewal := paidOrder(seller.ID, 4242, KindNew) // mislabelled on purpose
	renewal.QuotaBytes = 10 << 30
	if err := f.shop.CreateOrder(ctx, renewal); err != nil {
		t.Fatal(err)
	}
	again, err := f.shop.Fulfill(ctx, renewal.ID)
	if err != nil {
		t.Fatalf("Fulfill renewal: %v", err)
	}
	if again.ID != client.ID {
		t.Fatalf("renewal made customer %d, want the same %d", again.ID, client.ID)
	}
	if again.QuotaBytes != 30<<30 {
		t.Errorf("quota is %d, want the old 20 GB plus the renewal's 10 GB", again.QuotaBytes)
	}
	if again.Status != model.StatusActive {
		t.Errorf("status is %s, want active again", again.Status)
	}
	if until := time.Until(*again.ExpiresAt); until < 29*24*time.Hour || until > 31*24*time.Hour {
		t.Errorf("expiry is %v away, want about 30 days from the renewal, not from the old date", until)
	}
}

// Another seller's customer with the same Telegram id is not this seller's
// to renew: the scope decides, not the id alone.
func TestFulfillNeverTouchesAnotherSellersCustomer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := addOperator(t, f.db, "a-seller", model.RoleReseller, 0)
	b := addOperator(t, f.db, "b-seller", model.RoleReseller, 0)
	grant(t, f, a.ID, f.ifaces[0])
	grant(t, f, b.ID, f.ifaces[1])

	if err := f.shop.SaveConfig(ctx, &Config{OperatorID: a.ID, Enabled: true, InterfaceIDs: []uint{f.ifaces[0].ID}}); err != nil {
		t.Fatal(err)
	}
	oa := paidOrder(a.ID, 4242, KindNew)
	if err := f.shop.CreateOrder(ctx, oa); err != nil {
		t.Fatal(err)
	}
	aClient, err := f.shop.Fulfill(ctx, oa.ID)
	if err != nil {
		t.Fatalf("Fulfill A: %v", err)
	}

	if err := f.shop.SaveConfig(ctx, &Config{OperatorID: b.ID, Enabled: true, InterfaceIDs: []uint{f.ifaces[1].ID}}); err != nil {
		t.Fatal(err)
	}
	ob := paidOrder(b.ID, 4242, KindNew)
	if err := f.shop.CreateOrder(ctx, ob); err != nil {
		t.Fatal(err)
	}
	bClient, err := f.shop.Fulfill(ctx, ob.ID)
	if err != nil {
		t.Fatalf("Fulfill B: %v", err)
	}
	if bClient.ID == aClient.ID {
		t.Fatal("B's sale landed on A's customer")
	}
	if bClient.OwnerID != b.ID {
		t.Errorf("B's customer is owned by %d, want %d", bClient.OwnerID, b.ID)
	}
}

func TestRejectAndOperatorCtxRules(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	grant(t, f, seller.ID, f.ifaces[0], f.ifaces[1])
	if err := f.shop.SaveConfig(ctx, &Config{OperatorID: seller.ID, Enabled: true, InterfaceIDs: []uint{f.ifaces[0].ID}}); err != nil {
		t.Fatal(err)
	}

	review := paidOrder(seller.ID, 111, KindNew)
	review.Status = StatusPendingReview
	if err := f.shop.CreateOrder(ctx, review); err != nil {
		t.Fatal(err)
	}
	if err := f.shop.Reject(ctx, review.ID); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	got, _ := f.shop.OrderByID(ctx, review.ID)
	if got.Status != StatusRejected {
		t.Errorf("order is %s, want rejected", got.Status)
	}

	unpaid := paidOrder(seller.ID, 222, KindNew)
	unpaid.Status = StatusUnpaid
	if err := f.shop.CreateOrder(ctx, unpaid); err != nil {
		t.Fatal(err)
	}
	if err := f.shop.Reject(ctx, unpaid.ID); err == nil {
		t.Error("rejecting an unpaid order was allowed")
	}

	done := paidOrder(seller.ID, 333, KindNew)
	if err := f.shop.CreateOrder(ctx, done); err != nil {
		t.Fatal(err)
	}
	if _, err := f.shop.Fulfill(ctx, done.ID); err != nil {
		t.Fatalf("Fulfill: %v", err)
	}
	if err := f.shop.Reject(ctx, done.ID); err == nil {
		t.Error("rejecting a delivered order was allowed")
	}

	if _, err := f.shop.OperatorCtx(ctx, 9999); err == nil {
		t.Error("a context was built for an operator who does not exist")
	}
	off := addOperator(t, f.db, "off-seller", model.RoleReseller, 0)
	if err := f.db.Model(off).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := f.shop.OperatorCtx(ctx, off.ID); err == nil {
		t.Error("a context was built for a switched-off operator")
	}
}
