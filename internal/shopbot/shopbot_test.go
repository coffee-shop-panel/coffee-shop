package shopbot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/ipam"
	"github.com/abolfazl/w-ui/internal/service"
	"github.com/abolfazl/w-ui/internal/shop"
)

// The bot is tested the way it runs: a real shop on a real database, a
// fake Telegram on the other end of the wire. The conversation is driven
// update by update, and what is asserted is what the buyer, the seller
// and the database each end up holding.

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// shopFixture is one operator with one tunnel, one plan and a bot
// configured for card payment, ready to sell.
type shopFixture struct {
	db      *gorm.DB
	sh      *shop.Shop
	clients *service.Clients
	pools   *ipam.Pools
	opID    uint
	plan    *shop.Plan
	// v2plan is set only by the two-section fixture: the plan that
	// sells the shop's V2ray half.
	v2plan *shop.Plan
}

func newShopFixture(t *testing.T) *shopFixture {
	t.Helper()
	ctx := context.Background()
	db := testDB(t)

	admin := model.Admin{Username: "seller", PasswordHash: "unused", Role: model.RoleReseller, Enabled: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	iface := model.Interface{Name: "wg0", Protocol: model.ProtocolWireGuard, Enabled: true,
		ListenPort: 51820, Subnet: "10.66.0.0/24", EndpointHost: "vpn.example.com", MTU: 1420, NodeID: 1}
	if err := db.Create(&iface).Error; err != nil {
		t.Fatalf("seed interface: %v", err)
	}
	// The owner has assigned the tunnel to the seller: the row the
	// panel's own AllowedInterfaces reads, without which a reseller
	// may sell nothing.
	if err := db.Create(&model.AdminInterface{AdminID: admin.ID, InterfaceID: iface.ID}).Error; err != nil {
		t.Fatalf("grant interface: %v", err)
	}
	pools := ipam.NewPools()
	if _, err := pools.Add(iface.ID, iface.Subnet); err != nil {
		t.Fatalf("pool: %v", err)
	}
	clients := service.NewClients(db, pools, quietLog())
	sh := shop.New(db, clients, quietLog())

	if err := sh.SaveConfig(ctx, &shop.Config{
		OperatorID: admin.ID, Enabled: true, Token: "TESTTOKEN",
		AdminChatID: 999, CardNumber: "6037991234567890", CardHolder: "Ali Ahmadi",
		InterfaceIDs: []uint{iface.ID},
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	if err := sh.SavePlan(ctx, &shop.Plan{
		OperatorID: admin.ID, Name: "یک ماهه", QuotaBytes: 50 << 30, Days: 30,
		PriceToman: 120000, DeviceLimit: 2, Enabled: true,
	}); err != nil {
		t.Fatalf("save plan: %v", err)
	}
	plans, err := sh.PlansFor(ctx, admin.ID, true)
	if err != nil || len(plans) != 1 {
		t.Fatalf("plans: %v (%d)", err, len(plans))
	}
	return &shopFixture{db: db, sh: sh, clients: clients, pools: pools, opID: admin.ID, plan: &plans[0]}
}

// newTwoSectionFixture grows the one-section shop a second half: a
// 3x-ui inbound on the seller's tunnel list and a plan that sells it,
// so both sections are sellable and a buyer is asked which one they
// want before they see a plan.
func newTwoSectionFixture(t *testing.T) *shopFixture {
	t.Helper()
	ctx := context.Background()
	fx := newShopFixture(t)

	if err := fx.db.Create(&model.Node{Name: "local", Kind: model.KindLocal}).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
	// The V2ray half: an inbound adopted from a 3x-ui node. Three
	// things make it one a customer can be put on, and the fixture
	// needs all three, because delivery checks each in turn: the
	// inbound must say customers may be added to it (XUI.Customers --
	// a plain ProtocolXray row is refused, which is how the panel
	// keeps people off a 3x-ui WireGuard inbound), it must carry a
	// kind a secret can be drawn for, and its subnet needs a pool --
	// every account, Xray ones included, is given an address.
	xray := model.Interface{
		Name: "vless-1", Protocol: model.ProtocolXray, Enabled: true,
		ListenPort: 443, Subnet: "10.9.9.0/24", EndpointHost: "xray.example.com",
		NodeID: 1,
		XUI: model.JSON(model.XUIInbound{
			RemoteID: 39, Kind: "vless", Customers: true, Enabled: true,
		}),
	}
	if err := fx.db.Create(&xray).Error; err != nil {
		t.Fatalf("seed xray interface: %v", err)
	}
	if err := fx.db.Create(&model.AdminInterface{AdminID: fx.opID, InterfaceID: xray.ID}).Error; err != nil {
		t.Fatalf("grant xray interface: %v", err)
	}
	if _, err := fx.pools.Add(xray.ID, xray.Subnet); err != nil {
		t.Fatalf("xray pool: %v", err)
	}
	cfg, err := fx.sh.ConfigFor(ctx, fx.opID)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	cfg.InterfaceIDs = append(cfg.InterfaceIDs, xray.ID)
	if err := fx.sh.SaveConfig(ctx, cfg); err != nil {
		t.Fatalf("extend config: %v", err)
	}
	if err := fx.sh.SavePlan(ctx, &shop.Plan{
		OperatorID: fx.opID, Name: "وی‌توری ماهانه", Section: shop.SectionV2ray,
		QuotaBytes: 50 << 30, Days: 30, PriceToman: 150000, DeviceLimit: 2, Enabled: true,
	}); err != nil {
		t.Fatalf("save v2ray plan: %v", err)
	}
	plans, err := fx.sh.PlansFor(ctx, fx.opID, true)
	if err != nil {
		t.Fatalf("plans: %v", err)
	}
	for i := range plans {
		if shop.NormalizeSection(plans[i].Section) == shop.SectionV2ray {
			fx.v2plan = &plans[i]
		}
	}
	if fx.v2plan == nil {
		t.Fatal("the v2ray plan was not saved")
	}
	return fx
}

// fakeTG is Telegram, scripted by the test: updates are pushed in
// batches, and everything the bot says is kept for inspection.
type fakeTG struct {
	mu       sync.Mutex
	sent     []fakeMsg
	edits    []fakeMsg
	copied   []fakeMsg
	answered int
	queue    chan []json.RawMessage
	nextID   int64
	// fileBytes is the receipt photo the file endpoints hand back,
	// at filePath: getFile names it, the download serves it.
	fileBytes []byte
	filePath  string
}

type fakeMsg struct {
	chatID int64
	flat   string // text (or caption) plus the keyboard, flattened for searching
}

func newFakeTG(t *testing.T) (*fakeTG, *httptest.Server) {
	f := &fakeTG{
		queue: make(chan []json.RawMessage, 16), nextID: 1000,
		fileBytes: []byte("the-receipt-photo-bytes"), filePath: "photos/file_1.jpg",
	}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeTG) push(updates ...json.RawMessage) { f.queue <- updates }

func (f *fakeTG) serve(w http.ResponseWriter, r *http.Request) {
	// A file download is not a method call: its path names the file,
	// and what comes back is the file itself.
	if strings.Contains(r.URL.Path, "/file/bot") {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(f.fileBytes)
		return
	}
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	var params map[string]any
	_ = json.NewDecoder(r.Body).Decode(&params)

	chatID, _ := params["chat_id"].(float64)
	flat := ""
	if text, ok := params["text"].(string); ok {
		flat += text
	}
	if caption, ok := params["caption"].(string); ok {
		flat += caption
	}
	if markup, ok := params["reply_markup"]; ok {
		raw, _ := json.Marshal(markup)
		flat += " " + string(raw)
	}

	f.mu.Lock()
	f.nextID++
	msgID := f.nextID
	switch method {
	case "sendMessage":
		f.sent = append(f.sent, fakeMsg{chatID: int64(chatID), flat: flat})
	case "editMessageText":
		f.edits = append(f.edits, fakeMsg{chatID: int64(chatID), flat: flat})
	case "copyMessage":
		f.copied = append(f.copied, fakeMsg{chatID: int64(chatID), flat: flat})
	case "answerCallbackQuery":
		f.answered++
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	switch method {
	case "getMe":
		fmt.Fprint(w, `{"ok":true,"result":{"id":4242,"first_name":"Shop","username":"shop_bot"}}`)
	case "getFile":
		fmt.Fprintf(w, `{"ok":true,"result":{"file_id":"large-file","file_path":%q}}`, f.filePath)
	case "getUpdates":
		select {
		case batch := <-f.queue:
			parts := make([]string, len(batch))
			for i, u := range batch {
				parts[i] = string(u)
			}
			fmt.Fprintf(w, `{"ok":true,"result":[%s]}`, strings.Join(parts, ","))
		case <-time.After(25 * time.Millisecond):
			fmt.Fprint(w, `{"ok":true,"result":[]}`)
		}
	case "sendMessage", "copyMessage":
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"chat":{"id":%d}}}`, msgID, int64(chatID))
	default:
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	}
}

func (f *fakeTG) anySaid(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, list := range [][]fakeMsg{f.sent, f.edits} {
		for _, m := range list {
			if strings.Contains(m.flat, sub) {
				return true
			}
		}
	}
	return false
}

func (f *fakeTG) saidTo(chatID int64, sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.sent {
		if m.chatID == chatID && strings.Contains(m.flat, sub) {
			return true
		}
	}
	return false
}

// editContaining returns the flattened text of an edited message
// holding sub, for assertions about one screen -- the plan list of one
// section -- rather than about the whole conversation.
func (f *fakeTG) editContaining(sub string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.edits {
		if strings.Contains(m.flat, sub) {
			return m.flat, true
		}
	}
	return "", false
}

func (f *fakeTG) copiedTo(chatID int64, sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.copied {
		if m.chatID == chatID && strings.Contains(m.flat, sub) {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func tgUserObj(id int64, name string) map[string]any {
	return map[string]any{"id": id, "first_name": name}
}

func updText(id, chat, from int64, name, text string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"update_id": id,
		"message": map[string]any{
			"message_id": id * 10,
			"from":       tgUserObj(from, name),
			"chat":       map[string]any{"id": chat},
			"text":       text,
		},
	})
	return raw
}

func updPhoto(id, chat, from int64, name string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"update_id": id,
		"message": map[string]any{
			"message_id": id * 10,
			"from":       tgUserObj(from, name),
			"chat":       map[string]any{"id": chat},
			"photo": []any{
				map[string]any{"file_id": "small-file", "width": 90, "height": 90},
				map[string]any{"file_id": "large-file", "width": 800, "height": 800},
			},
		},
	})
	return raw
}

func updCallback(id, chat, from int64, name string, msgID int64, data string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{
		"update_id": id,
		"callback_query": map[string]any{
			"id":   fmt.Sprint("cb", id),
			"from": tgUserObj(from, name),
			"message": map[string]any{
				"message_id": msgID,
				"from":       tgUserObj(4242, "Shop"),
				"chat":       map[string]any{"id": chat},
				"text":       "menu",
			},
			"data": data,
		},
	})
	return raw
}

// TestTheCardSaleEndToEnd walks a whole sale: the buyer starts, picks
// the plan, pays by card, sends a receipt; the seller approves; the
// buyer holds a subscription and the database holds the customer.
func TestTheCardSaleEndToEnd(t *testing.T) {
	fx := newShopFixture(t)
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

	const buyer, seller = 111, 999
	fake.push(updText(1, buyer, buyer, "Buyer", "/start"))
	waitFor(t, "the welcome", func() bool { return fake.saidTo(buyer, "خوش آمدید") })

	fake.push(updCallback(2, buyer, buyer, "Buyer", 5001, "buy"))
	waitFor(t, "the plan list", func() bool { return fake.anySaid("یک ماهه") })

	fake.push(updCallback(3, buyer, buyer, "Buyer", 5001, fmt.Sprintf("plan:%d", fx.plan.ID)))
	waitFor(t, "the payment choices", func() bool { return fake.anySaid(btnPayCard) })

	fake.push(updCallback(4, buyer, buyer, "Buyer", 5001, fmt.Sprintf("card:%d", fx.plan.ID)))
	waitFor(t, "the card instructions", func() bool { return fake.anySaid("6037991234567890") })

	orders, err := fx.sh.OrdersFor(ctx, fx.opID, "", 10)
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders after choosing card: %v (%d)", err, len(orders))
	}
	order := orders[0]
	if order.Status != "awaiting_receipt" || order.Method != "card" || order.BuyerTGID != buyer {
		t.Fatalf("the order is %+v", order)
	}

	fake.push(updPhoto(5, buyer, buyer, "Buyer"))
	waitFor(t, "the receipt to reach the seller with its buttons", func() bool {
		return fake.copiedTo(seller, fmt.Sprintf("ok:%d", order.ID))
	})
	waitFor(t, "the order to await review", func() bool {
		o, err := fx.sh.OrderByID(ctx, order.ID)
		return err == nil && o.Status == "pending_review" && o.ReceiptFileID == "large-file"
	})
	// The same photo is the panel's copy too, fetched back off
	// Telegram and kept against the order.
	waitFor(t, "the receipt to be kept for the panel", func() bool {
		ct, data, err := fx.sh.ReceiptFor(ctx, order.ID)
		return err == nil && ct == "image/jpeg" && string(data) == string(fake.fileBytes)
	})

	fake.push(updCallback(6, seller, seller, "Seller", 6001, fmt.Sprintf("ok:%d", order.ID)))
	waitFor(t, "the order to be fulfilled", func() bool {
		o, err := fx.sh.OrderByID(ctx, order.ID)
		return err == nil && o.Status == "fulfilled" && o.ClientID != 0
	})

	var client model.Client
	if err := fx.db.Where("telegram_id = ?", buyer).First(&client).Error; err != nil {
		t.Fatalf("the buyer's customer: %v", err)
	}
	if client.OwnerID != fx.opID {
		t.Errorf("the customer belongs to %d, want the seller %d", client.OwnerID, fx.opID)
	}
	if client.SubToken == "" {
		t.Fatal("the customer has no subscription token to deliver")
	}
	waitFor(t, "the delivery to reach the buyer", func() bool {
		return fake.saidTo(buyer, client.SubToken)
	})
}

// TestASellerRejectionReachesTheBuyer covers the other button: the sale
// dies, the order says so, and no customer is made.
func TestASellerRejectionReachesTheBuyer(t *testing.T) {
	fx := newShopFixture(t)
	fake, srv := newFakeTG(t)

	oldBase := APIBase
	APIBase = srv.URL
	t.Cleanup(func() { APIBase = oldBase })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr := New(fx.sh, fx.clients, quietLog())
	mgr.Start(ctx)
	waitFor(t, "the bot to run", func() bool {
		_, running := mgr.BotInfo(fx.opID)
		return running
	})

	const buyer, seller = 222, 999
	fake.push(updText(1, buyer, buyer, "Buyer", "/start"))
	waitFor(t, "the welcome", func() bool { return fake.saidTo(buyer, "خوش آمدید") })
	fake.push(updCallback(2, buyer, buyer, "Buyer", 5001, "buy"))
	waitFor(t, "the plan list", func() bool { return fake.anySaid("یک ماهه") })
	fake.push(updCallback(3, buyer, buyer, "Buyer", 5001, fmt.Sprintf("card:%d", fx.plan.ID)))
	waitFor(t, "the card instructions", func() bool { return fake.anySaid("6037991234567890") })
	fake.push(updPhoto(4, buyer, buyer, "Buyer"))

	var orderID uint
	waitFor(t, "the order to await review", func() bool {
		orders, err := fx.sh.OrdersFor(ctx, fx.opID, "pending_review", 10)
		if err == nil && len(orders) == 1 {
			orderID = orders[0].ID
			return true
		}
		return false
	})

	fake.push(updCallback(5, seller, seller, "Seller", 6001, fmt.Sprintf("no:%d", orderID)))
	waitFor(t, "the order to be rejected", func() bool {
		o, err := fx.sh.OrderByID(ctx, orderID)
		return err == nil && o.Status == "rejected"
	})
	waitFor(t, "the buyer to be told", func() bool { return fake.saidTo(buyer, "تأیید نشد") })

	var count int64
	if err := fx.db.Model(&model.Client{}).Where("telegram_id = ?", buyer).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("a rejected sale made %d customers", count)
	}
}

// TestTheTwoSectionSale walks a V2ray purchase in a shop that sells
// both sections: the buyer is asked which section they want, sees only
// that section's plans, and is delivered the link bundle their apps
// import -- while the receipt still lands in the panel.
func TestTheTwoSectionSale(t *testing.T) {
	fx := newTwoSectionFixture(t)
	fake, srv := newFakeTG(t)

	oldBase := APIBase
	APIBase = srv.URL
	t.Cleanup(func() { APIBase = oldBase })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	// A public base, so deliveries are links rather than tokens and
	// the V2ray bundle link can be told from the plain one.
	if err := fx.sh.SetPublicBase(ctx, "https://panel.example.com"); err != nil {
		t.Fatalf("set public base: %v", err)
	}
	mgr := New(fx.sh, fx.clients, quietLog())
	mgr.Start(ctx)
	waitFor(t, "the bot to run", func() bool {
		_, running := mgr.BotInfo(fx.opID)
		return running
	})

	const buyer, seller = 333, 999
	fake.push(updText(1, buyer, buyer, "Buyer", "/start"))
	waitFor(t, "the welcome", func() bool { return fake.saidTo(buyer, "خوش آمدید") })

	// Both sections are sellable, so buying asks which one first.
	fake.push(updCallback(2, buyer, buyer, "Buyer", 5001, "buy"))
	waitFor(t, "the section question", func() bool {
		return fake.anySaid(txtChooseSection) && fake.anySaid("sec:wireguard") && fake.anySaid("sec:v2ray")
	})

	// The V2ray list names its section and holds its plans alone.
	fake.push(updCallback(3, buyer, buyer, "Buyer", 5001, "sec:v2ray"))
	var list string
	waitFor(t, "the v2ray plan list", func() bool {
		flat, ok := fake.editContaining(fx.v2plan.Name)
		list = flat
		return ok
	})
	if strings.Contains(list, fx.plan.Name) || strings.Contains(list, fmt.Sprintf("plan:%d", fx.plan.ID)) {
		t.Errorf("the v2ray plan list also offers the wireguard plan: %s", list)
	}
	if !strings.Contains(list, btnSectionV2ray) {
		t.Errorf("the v2ray plan list does not name its section: %s", list)
	}

	fake.push(updCallback(4, buyer, buyer, "Buyer", 5001, fmt.Sprintf("plan:%d", fx.v2plan.ID)))
	waitFor(t, "the payment choices", func() bool { return fake.anySaid(btnPayCard) })
	fake.push(updCallback(5, buyer, buyer, "Buyer", 5001, fmt.Sprintf("card:%d", fx.v2plan.ID)))
	waitFor(t, "the card instructions", func() bool { return fake.anySaid("6037991234567890") })

	orders, err := fx.sh.OrdersFor(ctx, fx.opID, "", 10)
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders after choosing card: %v (%d)", err, len(orders))
	}
	order := orders[0]
	if order.Section != shop.SectionV2ray {
		t.Fatalf("the order's section is %q, want %q", order.Section, shop.SectionV2ray)
	}

	fake.push(updPhoto(6, buyer, buyer, "Buyer"))
	waitFor(t, "the receipt to reach the seller with its buttons", func() bool {
		return fake.copiedTo(seller, fmt.Sprintf("ok:%d", order.ID))
	})
	waitFor(t, "the receipt to be kept for the panel", func() bool {
		ct, data, err := fx.sh.ReceiptFor(ctx, order.ID)
		return err == nil && ct == "image/jpeg" && string(data) == string(fake.fileBytes)
	})

	fake.push(updCallback(7, seller, seller, "Seller", 6001, fmt.Sprintf("ok:%d", order.ID)))
	waitFor(t, "the order to be fulfilled", func() bool {
		o, err := fx.sh.OrderByID(ctx, order.ID)
		return err == nil && o.Status == "fulfilled" && o.ClientID != 0
	})

	// The customer holds the V2ray half and no other.
	var client model.Client
	if err := fx.db.Preload("Accounts").Where("telegram_id = ?", buyer).First(&client).Error; err != nil {
		t.Fatalf("the buyer's customer: %v", err)
	}
	wg, xray, err := fx.sh.ClientSections(ctx, &client)
	if err != nil || wg || !xray {
		t.Errorf("the customer's sections are wireguard=%v v2ray=%v (%v), want v2ray only", wg, xray, err)
	}

	// And the delivery carries the bundle link a V2ray app imports.
	waitFor(t, "the delivery with the bundle link", func() bool {
		return fake.saidTo(buyer, "https://panel.example.com/subscribe/"+client.SubToken+"?format=base64")
	})
}

// TestSendAnnouncement covers the panel's voice through the bot: a
// broadcast reaches the operator's buyers, a single message reaches
// its one buyer, and the two refusals -- no bot running, no buyers
// yet -- say why, in the operator's own words.
func TestSendAnnouncement(t *testing.T) {
	fx := newShopFixture(t)
	fake, srv := newFakeTG(t)

	oldBase := APIBase
	APIBase = srv.URL
	t.Cleanup(func() { APIBase = oldBase })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// A second operator whose bot will run but has sold nothing.
	quiet := model.Admin{Username: "quiet", PasswordHash: "unused", Role: model.RoleReseller, Enabled: true}
	if err := fx.db.Create(&quiet).Error; err != nil {
		t.Fatalf("seed second operator: %v", err)
	}
	if err := fx.sh.SaveConfig(ctx, &shop.Config{
		OperatorID: quiet.ID, Enabled: true, Token: "QUIETTOKEN", AdminChatID: 888,
	}); err != nil {
		t.Fatalf("save second config: %v", err)
	}

	mgr := New(fx.sh, fx.clients, quietLog())
	mgr.Start(ctx)
	waitFor(t, "both bots to run", func() bool {
		_, a := mgr.BotInfo(fx.opID)
		_, b := mgr.BotInfo(quiet.ID)
		return a && b
	})

	if _, err := mgr.SendAnnouncement(ctx, 987654, 0, "سلام"); err == nil ||
		err.Error() != "بات فروش روشن نیست؛ اول از صفحه «بات فروش» پنل بات را روشن کنید" {
		t.Errorf("announcement with no bot running: err = %v", err)
	}
	if _, err := mgr.SendAnnouncement(ctx, quiet.ID, 0, "سلام"); err == nil ||
		err.Error() != "هنوز هیچ مشتری‌ای از بات خرید نکرده است" {
		t.Errorf("announcement with no buyers: err = %v", err)
	}

	// One delivered sale, made directly in the shop: buyer 777 now
	// holds a subscription from the fixture's operator.
	const buyer = 777
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
	if _, err := fx.sh.Fulfill(ctx, o.ID); err != nil {
		t.Fatalf("fulfill: %v", err)
	}

	sent, err := mgr.SendAnnouncement(ctx, fx.opID, 0, "فروش ویژهٔ آخر هفته")
	if err != nil || sent != 1 {
		t.Fatalf("broadcast: sent = %d, err = %v", sent, err)
	}
	if !fake.saidTo(buyer, "📣 پیام فروشنده") || !fake.saidTo(buyer, "فروش ویژهٔ آخر هفته") {
		t.Error("the broadcast did not reach the buyer")
	}

	sent, err = mgr.SendAnnouncement(ctx, fx.opID, buyer, "اشتراکت رو تمدید کن")
	if err != nil || sent != 1 {
		t.Fatalf("single announcement: sent = %d, err = %v", sent, err)
	}
	if !fake.saidTo(buyer, "اشتراکت رو تمدید کن") {
		t.Error("the single announcement did not reach the buyer")
	}
}
