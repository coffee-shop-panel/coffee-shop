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
	opID    uint
	plan    *shop.Plan
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
	return &shopFixture{db: db, sh: sh, clients: clients, opID: admin.ID, plan: &plans[0]}
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
}

type fakeMsg struct {
	chatID int64
	flat   string // text (or caption) plus the keyboard, flattened for searching
}

func newFakeTG(t *testing.T) (*fakeTG, *httptest.Server) {
	f := &fakeTG{queue: make(chan []json.RawMessage, 16), nextID: 1000}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeTG) push(updates ...json.RawMessage) { f.queue <- updates }

func (f *fakeTG) serve(w http.ResponseWriter, r *http.Request) {
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
