package shop

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/fault"
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
	xray    model.Interface
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
	// The V2ray half of the shop: an inbound adopted from a 3x-ui node.
	// Three things make it one a customer can be put on, and the fixture
	// needs all three, because delivery checks each in turn: the inbound
	// must say customers may be added to it (XUI.Customers -- a plain
	// ProtocolXray row is refused by loadInterfaces, which is how the
	// panel keeps people off a 3x-ui WireGuard inbound), it must carry a
	// kind a secret can be drawn for, and its subnet needs a pool --
	// every account, Xray ones included, is given an address.
	f.xray = model.Interface{
		Name: "vless-1", Protocol: model.ProtocolXray, Enabled: true,
		ListenPort: 443, Subnet: "10.9.9.0/24", EndpointHost: "xray.example.com",
		NodeID: 1,
		XUI: model.JSON(model.XUIInbound{
			RemoteID: 39, Kind: "vless", Customers: true, Enabled: true,
		}),
	}
	if err := db.Create(&f.xray).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := pools.Add(f.xray.ID, f.xray.Subnet); err != nil {
		t.Fatal(err)
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

// A plan's section is validated like its other terms: a section the
// shop does not have is refused, and an empty one -- every plan saved
// before sections existed -- is saved as the WireGuard it always was.
func TestSavePlanSectionRules(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)

	bad := &Plan{OperatorID: seller.ID, Name: "bad", Section: "satellite", PriceToman: 10}
	if err := f.shop.SavePlan(ctx, bad); err == nil {
		t.Fatal("a plan in a section the shop does not have was saved")
	} else if fault.FieldOf(err) != "section" {
		t.Errorf("the refusal names field %q, want section (%v)", fault.FieldOf(err), err)
	}

	legacy := &Plan{OperatorID: seller.ID, Name: "legacy", PriceToman: 10, Enabled: true}
	if err := f.shop.SavePlan(ctx, legacy); err != nil {
		t.Fatalf("SavePlan legacy: %v", err)
	}
	if legacy.Section != SectionWireGuard {
		t.Errorf("an empty section was saved as %q, want wireguard", legacy.Section)
	}
	v2 := &Plan{OperatorID: seller.ID, Name: "v2", Section: SectionV2ray, PriceToman: 10, Enabled: true}
	if err := f.shop.SavePlan(ctx, v2); err != nil {
		t.Fatalf("SavePlan v2ray: %v", err)
	}
	plans, err := f.shop.PlansFor(ctx, seller.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]string{}
	for _, p := range plans {
		byName[p.Name] = p.Section
	}
	if byName["legacy"] != SectionWireGuard || byName["v2"] != SectionV2ray {
		t.Errorf("sections read back %v", byName)
	}
}

// The section split of a seller's tunnel list: each section gets its
// own kind, in the config's order, and a tunnel that is gone from the
// database is skipped rather than breaking the list.
func TestSectionInterfaces(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	cfg := &Config{InterfaceIDs: []uint{f.xray.ID, f.ifaces[1].ID, 99999, f.ifaces[0].ID}}

	wg, err := f.shop.SectionInterfaces(ctx, cfg, SectionWireGuard)
	if err != nil {
		t.Fatalf("SectionInterfaces wireguard: %v", err)
	}
	if len(wg) != 2 || wg[0] != f.ifaces[1].ID || wg[1] != f.ifaces[0].ID {
		t.Errorf("wireguard section = %v, want the two WireGuard tunnels in config order", wg)
	}
	v2, err := f.shop.SectionInterfaces(ctx, cfg, SectionV2ray)
	if err != nil {
		t.Fatalf("SectionInterfaces v2ray: %v", err)
	}
	if len(v2) != 1 || v2[0] != f.xray.ID {
		t.Errorf("v2ray section = %v, want the one inbound", v2)
	}
	// An order written before sections existed carries nothing, and
	// nothing is WireGuard.
	legacy, err := f.shop.SectionInterfaces(ctx, cfg, "")
	if err != nil || len(legacy) != 2 {
		t.Errorf("empty section = %v, %v; want the WireGuard tunnels", legacy, err)
	}
}

// A sale delivers its own section and no other: the buyer chose
// WireGuard or V2ray before they saw a price, and the other half of the
// shop is a different product the seller sells separately.
func TestFulfillDeliversOnlyTheOrdersSection(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	grant(t, f, seller.ID, f.ifaces[0], f.ifaces[1], f.xray)
	if err := f.shop.SaveConfig(ctx, &Config{
		OperatorID: seller.ID, Enabled: true,
		InterfaceIDs: []uint{f.ifaces[0].ID, f.ifaces[1].ID, f.xray.ID},
	}); err != nil {
		t.Fatal(err)
	}

	v2 := paidOrder(seller.ID, 5001, KindNew)
	v2.Section = SectionV2ray
	if err := f.shop.CreateOrder(ctx, v2); err != nil {
		t.Fatal(err)
	}
	v2Client, err := f.shop.Fulfill(ctx, v2.ID)
	if err != nil {
		t.Fatalf("Fulfill v2ray: %v", err)
	}
	if len(v2Client.Accounts) != 2 {
		t.Fatalf("the v2ray customer holds %d accounts, want 2 (one per device)", len(v2Client.Accounts))
	}
	for _, acc := range v2Client.Accounts {
		if acc.InterfaceID != f.xray.ID {
			t.Errorf("a v2ray account sits on tunnel %d, want the inbound %d", acc.InterfaceID, f.xray.ID)
		}
	}
	wg, xray, err := f.shop.ClientSections(ctx, v2Client)
	if err != nil || wg || !xray {
		t.Errorf("ClientSections = %v, %v, %v; want v2ray only", wg, xray, err)
	}

	wgOrder := paidOrder(seller.ID, 5002, KindNew)
	wgOrder.Section = SectionWireGuard
	if err := f.shop.CreateOrder(ctx, wgOrder); err != nil {
		t.Fatal(err)
	}
	wgClient, err := f.shop.Fulfill(ctx, wgOrder.ID)
	if err != nil {
		t.Fatalf("Fulfill wireguard: %v", err)
	}
	if len(wgClient.Accounts) != 4 {
		t.Fatalf("the wireguard customer holds %d accounts, want 4 (2 devices x 2 tunnels)", len(wgClient.Accounts))
	}
	for _, acc := range wgClient.Accounts {
		if acc.InterfaceID == f.xray.ID {
			t.Error("a wireguard account sits on the v2ray inbound")
		}
	}
	wg, xray, err = f.shop.ClientSections(ctx, wgClient)
	if err != nil || !wg || xray {
		t.Errorf("ClientSections = %v, %v, %v; want wireguard only", wg, xray, err)
	}

	// A seller whose shop has no tunnel of the section cannot deliver
	// it: the order stays paid and the reason names the section.
	plain := addOperator(t, f.db, "plain", model.RoleReseller, 0)
	grant(t, f, plain.ID, f.ifaces[0])
	if err := f.shop.SaveConfig(ctx, &Config{
		OperatorID: plain.ID, Enabled: true, InterfaceIDs: []uint{f.ifaces[0].ID},
	}); err != nil {
		t.Fatal(err)
	}
	stranded := paidOrder(plain.ID, 5003, KindNew)
	stranded.Section = SectionV2ray
	if err := f.shop.CreateOrder(ctx, stranded); err != nil {
		t.Fatal(err)
	}
	if _, err := f.shop.Fulfill(ctx, stranded.ID); err == nil {
		t.Fatal("a v2ray sale was delivered by a shop with no inbound")
	} else if !strings.Contains(err.Error(), "v2ray") {
		t.Errorf("the refusal is %q, want it to name the section", err)
	}
	after, _ := f.shop.OrderByID(ctx, stranded.ID)
	if after.Status != StatusPaid {
		t.Errorf("the undeliverable order is %s, want still paid", after.Status)
	}
}

// The second purchase, in the other section, renews the one customer
// and adds the section they lacked: one Telegram account stays one
// customer, holding both halves under the one link.
func TestFulfillRenewalAcrossSectionsAttaches(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	grant(t, f, seller.ID, f.ifaces[0], f.xray)
	if err := f.shop.SaveConfig(ctx, &Config{
		OperatorID: seller.ID, Enabled: true,
		InterfaceIDs: []uint{f.ifaces[0].ID, f.xray.ID},
	}); err != nil {
		t.Fatal(err)
	}

	first := paidOrder(seller.ID, 6001, KindNew) // wireguard, as an empty section reads
	if err := f.shop.CreateOrder(ctx, first); err != nil {
		t.Fatal(err)
	}
	client, err := f.shop.Fulfill(ctx, first.ID)
	if err != nil {
		t.Fatalf("Fulfill wireguard: %v", err)
	}
	if len(client.Accounts) != 2 {
		t.Fatalf("the new customer holds %d accounts, want 2 on the wireguard tunnel", len(client.Accounts))
	}

	second := paidOrder(seller.ID, 6001, KindRenew)
	second.Section = SectionV2ray
	second.QuotaBytes = 10 << 30
	if err := f.shop.CreateOrder(ctx, second); err != nil {
		t.Fatal(err)
	}
	again, err := f.shop.Fulfill(ctx, second.ID)
	if err != nil {
		t.Fatalf("Fulfill v2ray renewal: %v", err)
	}
	if again.ID != client.ID {
		t.Fatalf("the cross-section purchase made customer %d, want the same %d", again.ID, client.ID)
	}
	onWG, onXray := 0, 0
	for _, acc := range again.Accounts {
		switch acc.InterfaceID {
		case f.ifaces[0].ID:
			onWG++
		case f.xray.ID:
			onXray++
		}
	}
	if onWG != 2 || onXray != 2 {
		t.Errorf("accounts: %d wireguard, %d xray; want 2 and 2", onWG, onXray)
	}
	if again.QuotaBytes != 30<<30 {
		t.Errorf("quota is %d, want the first 20 GB plus the renewal's 10 GB", again.QuotaBytes)
	}
	wg, xray, err := f.shop.ClientSections(ctx, again)
	if err != nil || !wg || !xray {
		t.Errorf("ClientSections = %v, %v, %v; want both sections", wg, xray, err)
	}
}

// The buyers list: delivered orders grouped by the person who placed
// them, standing read off their customer, newest business first -- and
// a buyer whose customer was deleted is not a row that opens onto
// nothing.
func TestBuyersForAggregatesAndSkipsDeleted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	grant(t, f, seller.ID, f.ifaces[0])
	if err := f.shop.SaveConfig(ctx, &Config{
		OperatorID: seller.ID, Enabled: true, InterfaceIDs: []uint{f.ifaces[0].ID},
	}); err != nil {
		t.Fatal(err)
	}
	buy := func(tg int64) *Order {
		o := paidOrder(seller.ID, tg, KindNew)
		if err := f.shop.CreateOrder(ctx, o); err != nil {
			t.Fatal(err)
		}
		if _, err := f.shop.Fulfill(ctx, o.ID); err != nil {
			t.Fatalf("Fulfill %d: %v", tg, err)
		}
		return o
	}
	a1 := buy(7001)
	a2 := buy(7001) // the same person again: a renewal, not a second customer
	c1 := buy(7003)
	b1 := buy(7002)

	// The placed-at times are set outright, so the order of the list is
	// the test's and not the clock's.
	base := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for o, at := range map[*Order]time.Time{
		a1: base, a2: base.Add(2 * time.Hour),
		b1: base.Add(3 * time.Hour), c1: base.Add(4 * time.Hour),
	} {
		if err := f.db.Model(&model.ShopOrder{}).Where("id = ?", o.ID).
			Update("created_at", at).Error; err != nil {
			t.Fatal(err)
		}
	}

	// Buyer 7002's customer is deleted after the sale.
	dead, err := f.shop.OrderByID(ctx, b1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Delete(&model.Client{}, dead.ClientID).Error; err != nil {
		t.Fatal(err)
	}

	buyers, err := f.shop.BuyersFor(ctx, seller.ID)
	if err != nil {
		t.Fatalf("BuyersFor: %v", err)
	}
	if len(buyers) != 2 {
		t.Fatalf("buyers = %+v, want the two whose customers remain", buyers)
	}
	if buyers[0].TelegramID != 7003 || buyers[1].TelegramID != 7001 {
		t.Errorf("order is %d then %d, want newest business (7003) first", buyers[0].TelegramID, buyers[1].TelegramID)
	}
	a := buyers[1]
	if a.Orders != 2 {
		t.Errorf("buyer 7001 holds %d orders on the list, want 2", a.Orders)
	}
	if !a.LastOrderAt.Equal(base.Add(2 * time.Hour)) {
		t.Errorf("buyer 7001's last order reads %v, want the later purchase", a.LastOrderAt)
	}
	fresh, err := f.clients.Get(ctx, a.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != fresh.Name || a.Status != string(fresh.Status) || a.QuotaBytes != fresh.QuotaBytes {
		t.Errorf("the row reads %+v, the customer is %+v", a, fresh)
	}

	// Another seller's buyers are not this seller's list.
	other := addOperator(t, f.db, "other", model.RoleReseller, 0)
	none, err := f.shop.BuyersFor(ctx, other.ID)
	if err != nil {
		t.Fatalf("BuyersFor other: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("a seller with no sales lists %d buyers", len(none))
	}
}

// A receipt kept in the panel: it reads back as it was sent, a second
// one replaces the first, an oversize one is refused, and the orders
// say whether they have one.
func TestReceiptRoundTripAndLimits(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	seller := addOperator(t, f.db, "hussain", model.RoleReseller, 0)
	o := paidOrder(seller.ID, 8001, KindNew)
	o.Status = StatusPendingReview
	if err := f.shop.CreateOrder(ctx, o); err != nil {
		t.Fatal(err)
	}

	if _, _, err := f.shop.ReceiptFor(ctx, o.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Errorf("ReceiptFor with none kept: %v, want not found", err)
	}
	got, err := f.shop.OrderByID(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.HasReceipt {
		t.Error("an order with no receipt kept says it has one")
	}

	picture := []byte("\xff\xd8fake-jpeg")
	if err := f.shop.SaveReceipt(ctx, o.ID, "image/jpeg", picture); err != nil {
		t.Fatalf("SaveReceipt: %v", err)
	}
	ct, data, err := f.shop.ReceiptFor(ctx, o.ID)
	if err != nil {
		t.Fatalf("ReceiptFor: %v", err)
	}
	if ct != "image/jpeg" || string(data) != string(picture) {
		t.Errorf("the receipt reads back %q %q", ct, data)
	}
	got, _ = f.shop.OrderByID(ctx, o.ID)
	if !got.HasReceipt {
		t.Error("the order does not say it has the receipt that was kept")
	}
	list, err := f.shop.OrdersFor(ctx, seller.ID, "", 10)
	if err != nil || len(list) != 1 || !list[0].HasReceipt {
		t.Errorf("the list reads %+v, %v; want the order marked", list, err)
	}

	better := []byte("\xff\xd8better-jpeg")
	if err := f.shop.SaveReceipt(ctx, o.ID, "image/jpeg", better); err != nil {
		t.Fatalf("SaveReceipt again: %v", err)
	}
	_, data, err = f.shop.ReceiptFor(ctx, o.ID)
	if err != nil || string(data) != string(better) {
		t.Errorf("after replacing, the receipt is %q, %v", data, err)
	}
	var count int64
	if err := f.db.Model(&model.ShopReceipt{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("%d receipts are kept for one order, want 1", count)
	}

	if err := f.shop.SaveReceipt(ctx, o.ID, "image/jpeg", make([]byte, maxReceiptBytes+1)); err == nil {
		t.Error("a receipt past the size limit was kept")
	} else if !errors.Is(err, fault.ErrInvalid) {
		t.Errorf("the oversize refusal is %v, want a validation error", err)
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
