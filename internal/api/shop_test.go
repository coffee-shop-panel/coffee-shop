package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/ipam"
	"github.com/abolfazl/w-ui/internal/scope"
	"github.com/abolfazl/w-ui/internal/service"
	"github.com/abolfazl/w-ui/internal/shop"
	"github.com/abolfazl/w-ui/internal/shopbot"
)

// The shop handlers over a real store: the rules these tests exist for
// are separations -- one operator's token never read back, one operator's
// orders never confirmed to another -- and a separation is only proved
// against the real database and the real delivery path, not stubs.

type shopFixture struct {
	srv     *Server
	db      *gorm.DB
	store   *shop.Shop
	clients *service.Clients
	ifaces  []model.Interface
	owner   *model.Admin
	seller  *model.Admin
	other   *model.Admin
}

func newShopFixture(t *testing.T, withBot bool) *shopFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	if err := scope.Register(db); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := db.Create(&model.Node{Name: "local", Kind: model.KindLocal}).Error; err != nil {
		t.Fatal(err)
	}
	pools := ipam.NewPools()
	f := &shopFixture{db: db}
	for i, name := range []string{"wg0", "wg1"} {
		iface := model.Interface{
			Name: name, Protocol: model.ProtocolWireGuard, Enabled: true,
			ListenPort: 51820 + i, Subnet: "10.66." + strconv.Itoa(i) + ".0/24",
			EndpointHost: "vpn.example.com", NodeID: 1,
		}
		if err := db.Create(&iface).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := pools.Add(iface.ID, iface.Subnet); err != nil {
			t.Fatal(err)
		}
		f.ifaces = append(f.ifaces, iface)
	}
	f.clients = service.NewClients(db, pools, log)
	f.store = shop.New(db, f.clients, log)
	mk := func(username string, role model.AdminRole, parent uint) *model.Admin {
		a := &model.Admin{Username: username, PasswordHash: "x", Role: role,
			ParentAdminID: parent, Enabled: true}
		if err := db.Create(a).Error; err != nil {
			t.Fatalf("operator %s: %v", username, err)
		}
		return a
	}
	f.owner = mk("owner", model.RoleOwner, 0)
	f.seller = mk("hussain", model.RoleReseller, f.owner.ID)
	f.other = mk("other", model.RoleReseller, f.owner.ID)
	// Both resellers hold both tunnels, as the owner would have assigned
	// them on the operators page: the rows AllowedInterfaces reads,
	// without which a reseller may sell nothing.
	for _, seller := range []*model.Admin{f.seller, f.other} {
		for _, iface := range f.ifaces {
			if err := db.Create(&model.AdminInterface{AdminID: seller.ID, InterfaceID: iface.ID}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	f.srv = &Server{log: log, clients: f.clients, shop: f.store}
	if withBot {
		f.srv.shopBot = shopbot.New(f.store, f.clients, log)
	}
	return f
}

func shopDo(s *Server, h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// The config read says a token is set and never says what it is: the
// value is a BotFather secret, and a response body is a thing that gets
// logged, screenshotted and pasted into support chats.
func TestShopConfigNeverShowsTheToken(t *testing.T) {
	f := newShopFixture(t, false)
	ctx := context.Background()
	if err := f.store.SaveConfig(ctx, &shop.Config{
		OperatorID: f.seller.ID, Enabled: true, Token: "123456:SECRET-TOKEN",
		AdminChatID: 99, CardNumber: "6037", InterfaceIDs: []uint{f.ifaces[0].ID},
	}); err != nil {
		t.Fatal(err)
	}

	rec := shopDo(f.srv, f.srv.handleShopConfig,
		asAdmin(f.seller, http.MethodGet, "/api/shop/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "SECRET-TOKEN") {
		t.Fatalf("the token itself is in the answer: %s", rec.Body)
	}
	var out shopConfigView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.TokenSet || !out.Enabled || out.AdminChatID != 99 || out.CardNumber != "6037" {
		t.Errorf("config reads %+v", out)
	}
	if out.PublicBaseEditable {
		t.Error("a reseller is told they can move the panel's public base")
	}
	if out.BotRunning || out.BotUsername != "" {
		t.Errorf("no bot manager, but the bot reads %q running=%v", out.BotUsername, out.BotRunning)
	}
}

// A save with no token in it -- which is every ordinary save, because the
// page never holds the value -- keeps the token on file; a save with one
// replaces it.
func TestShopConfigSaveKeepsThenReplacesTheToken(t *testing.T) {
	f := newShopFixture(t, false)
	ctx := context.Background()
	if err := f.store.SaveConfig(ctx, &shop.Config{
		OperatorID: f.seller.ID, Enabled: true, Token: "111:old",
	}); err != nil {
		t.Fatal(err)
	}
	save := func(body map[string]any) *httptest.ResponseRecorder {
		return shopDo(f.srv, f.srv.handleShopConfigSave,
			asAdmin(f.seller, http.MethodPut, "/api/shop/config", body))
	}

	rec := save(map[string]any{"enabled": true, "cardNumber": "6037"})
	if rec.Code != http.StatusOK {
		t.Fatalf("save without a token: status %d, body %s", rec.Code, rec.Body)
	}
	cfg, err := f.store.ConfigFor(ctx, f.seller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "111:old" || cfg.CardNumber != "6037" {
		t.Errorf("after a tokenless save the config is %+v", cfg)
	}

	rec = save(map[string]any{"enabled": true, "token": "222:new"})
	if rec.Code != http.StatusOK {
		t.Fatalf("save with a token: status %d, body %s", rec.Code, rec.Body)
	}
	cfg, err = f.store.ConfigFor(ctx, f.seller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "222:new" {
		t.Errorf("after a token save the token is %q, want the new one", cfg.Token)
	}
}

// The shop's teaching words round-trip with the rest of the config:
// what the page saves as the tutorial and the FAQ is what the config
// reads back -- and what the bot will offer -- word for word.
func TestShopConfigTutorialAndFaqRoundTrip(t *testing.T) {
	f := newShopFixture(t, false)

	rec := shopDo(f.srv, f.srv.handleShopConfigSave,
		asAdmin(f.seller, http.MethodPut, "/api/shop/config", map[string]any{
			"enabled":      true,
			"tutorialText": "اول برنامه را نصب کنید، بعد لینک را وارد کنید.",
			"faqText":      "چرا وصل نمی‌شود؟ چون حجم تمام شده است.",
		}))
	if rec.Code != http.StatusOK {
		t.Fatalf("save: status %d, body %s", rec.Code, rec.Body)
	}
	var out shopConfigView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.TutorialText != "اول برنامه را نصب کنید، بعد لینک را وارد کنید." ||
		out.FaqText != "چرا وصل نمی‌شود؟ چون حجم تمام شده است." {
		t.Errorf("the saved config reads tutorial %q, faq %q", out.TutorialText, out.FaqText)
	}

	cfg, err := f.store.ConfigFor(context.Background(), f.seller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TutorialText != out.TutorialText || cfg.FaqText != out.FaqText {
		t.Errorf("the stored config is tutorial %q, faq %q", cfg.TutorialText, cfg.FaqText)
	}

	rec = shopDo(f.srv, f.srv.handleShopConfig,
		asAdmin(f.seller, http.MethodGet, "/api/shop/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("read: status %d, body %s", rec.Code, rec.Body)
	}
	out = shopConfigView{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.TutorialText != cfg.TutorialText || out.FaqText != cfg.FaqText {
		t.Errorf("a fresh read says tutorial %q, faq %q", out.TutorialText, out.FaqText)
	}
}

// The public base is the panel's, not a seller's: a reseller naming a
// different one is refused and nothing of theirs is saved either, while
// the owner may set it and then reads it back.
func TestShopPublicBaseIsTheOwnersAlone(t *testing.T) {
	f := newShopFixture(t, false)

	rec := shopDo(f.srv, f.srv.handleShopConfigSave,
		asAdmin(f.seller, http.MethodPut, "/api/shop/config",
			map[string]any{"enabled": true, "cardNumber": "6037",
				"publicBase": "https://elsewhere.example:12489"}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("reseller moving the public base: status %d, want 403 (body %s)", rec.Code, rec.Body)
	}
	cfg, err := f.store.ConfigFor(context.Background(), f.seller.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CardNumber != "" {
		t.Errorf("the refused save wrote the seller's config anyway: %+v", cfg)
	}

	rec = shopDo(f.srv, f.srv.handleShopConfigSave,
		asAdmin(f.owner, http.MethodPut, "/api/shop/config",
			map[string]any{"enabled": false, "publicBase": "https://panel.example:12489"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("owner setting the public base: status %d, body %s", rec.Code, rec.Body)
	}
	var out shopConfigView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.PublicBase != "https://panel.example:12489" || !out.PublicBaseEditable {
		t.Errorf("the owner's config reads %+v", out)
	}
}

// A plan saved through the page belongs to the caller whatever the body
// claims: the operator id in a request is a thing a caller could put
// there on purpose.
func TestShopPlanSaveForcesTheCaller(t *testing.T) {
	f := newShopFixture(t, false)

	rec := shopDo(f.srv, f.srv.handleShopPlanSave,
		asAdmin(f.seller, http.MethodPost, "/api/shop/plans",
			map[string]any{"operatorId": f.owner.ID, "name": "ماهانه",
				"quotaBytes": 10 << 30, "days": 30, "priceToman": 50000,
				"deviceLimit": 2, "enabled": true}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var out shopPlanView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.OperatorID != f.seller.ID || out.ID == 0 {
		t.Errorf("the saved plan is %+v, want the seller's own new plan", out)
	}
	ownerPlans, err := f.store.PlansFor(context.Background(), f.owner.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ownerPlans) != 0 {
		t.Errorf("the owner gained %d plans from a reseller's save", len(ownerPlans))
	}
}

func shopTestOrder(t *testing.T, f *shopFixture, operatorID uint) *shop.Order {
	t.Helper()
	o := &shop.Order{
		OperatorID: operatorID, BuyerTGID: 4242, BuyerName: "Buyer",
		PlanName: "ماهانه", QuotaBytes: 10 << 30, Days: 30,
		PriceToman: 50000, DeviceLimit: 2,
		Kind: shop.KindNew, Method: shop.MethodCard, Status: shop.StatusPendingReview,
	}
	if err := f.store.CreateOrder(context.Background(), o); err != nil {
		t.Fatalf("order: %v", err)
	}
	return o
}

// Another operator's order is not refused, it is not there: approving a
// sale that is not yours must not even confirm the sale exists.
func TestShopApproveAnotherOperatorsOrderIs404(t *testing.T) {
	f := newShopFixture(t, false)
	order := shopTestOrder(t, f, f.seller.ID)

	req := asAdmin(f.other, http.MethodPost, "/api/shop/orders/approve", nil)
	req.SetPathValue("id", strconv.Itoa(int(order.ID)))
	rec := shopDo(f.srv, f.srv.handleShopOrderApprove, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404 (body %s)", rec.Code, rec.Body)
	}
	got, err := f.store.OrderByID(context.Background(), order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != shop.StatusPendingReview {
		t.Errorf("the order is %q after another operator's approve", got.Status)
	}
}

// Approving your own order delivers it: the customer exists under your
// name afterwards, the order says so, and the answer carries the link.
func TestShopApproveOwnOrderDelivers(t *testing.T) {
	f := newShopFixture(t, false)
	ctx := context.Background()
	if err := f.store.SaveConfig(ctx, &shop.Config{
		OperatorID: f.seller.ID, Enabled: true, Token: "111:x",
		InterfaceIDs: []uint{f.ifaces[0].ID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetPublicBase(ctx, "https://panel.example:12489"); err != nil {
		t.Fatal(err)
	}
	order := shopTestOrder(t, f, f.seller.ID)

	req := asAdmin(f.seller, http.MethodPost, "/api/shop/orders/approve", nil)
	req.SetPathValue("id", strconv.Itoa(int(order.ID)))
	rec := shopDo(f.srv, f.srv.handleShopOrderApprove, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var out struct {
		Order    shopOrderView `json:"order"`
		ClientID uint          `json:"clientId"`
		SubLink  string        `json:"subLink"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body)
	}
	if out.ClientID == 0 || out.Order.Status != shop.StatusFulfilled || out.Order.ClientID != out.ClientID {
		t.Errorf("the answer is %+v", out)
	}
	if !strings.HasPrefix(out.SubLink, "https://panel.example:12489/subscribe/") {
		t.Errorf("subLink %q does not point at the public base", out.SubLink)
	}
	client, err := f.clients.Get(ctx, out.ClientID)
	if err != nil {
		t.Fatalf("the delivered customer: %v", err)
	}
	// One account per device the plan allows, each on the seller's
	// chosen tunnel.
	if client.TelegramID != 4242 || len(client.Accounts) != 2 {
		t.Errorf("the delivered customer is %+v", client)
	}
	for _, a := range client.Accounts {
		if a.InterfaceID != f.ifaces[0].ID {
			t.Errorf("an account sits on tunnel %d, want the seller's %d", a.InterfaceID, f.ifaces[0].ID)
		}
	}
}

// A plan's section travels with the save: the page names the half of
// the shop the plan belongs to, the answer reads it back normalised,
// and a section the shop does not have is a validation refusal.
func TestShopPlanSaveSection(t *testing.T) {
	f := newShopFixture(t, false)

	rec := shopDo(f.srv, f.srv.handleShopPlanSave,
		asAdmin(f.seller, http.MethodPost, "/api/shop/plans",
			map[string]any{"name": "v2", "section": "v2ray", "enabled": true}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var out shopPlanView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Section != shop.SectionV2ray {
		t.Errorf("the saved plan's section is %q, want v2ray", out.Section)
	}

	rec = shopDo(f.srv, f.srv.handleShopPlanSave,
		asAdmin(f.seller, http.MethodPost, "/api/shop/plans",
			map[string]any{"name": "bad", "section": "satellite"}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an unknown section: status %d, want 400 (body %s)", rec.Code, rec.Body)
	}
}

// The buyers list is the caller's own delivered sales and nobody
// else's: one seller's page never names another seller's customers.
func TestShopBuyersListsOnlyTheCallers(t *testing.T) {
	f := newShopFixture(t, false)
	ctx := context.Background()
	if err := f.store.SaveConfig(ctx, &shop.Config{
		OperatorID: f.seller.ID, Enabled: true, Token: "111:x",
		InterfaceIDs: []uint{f.ifaces[0].ID},
	}); err != nil {
		t.Fatal(err)
	}
	order := shopTestOrder(t, f, f.seller.ID)
	order.Status = shop.StatusPaid
	if err := f.store.UpdateOrder(ctx, order); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Fulfill(ctx, order.ID); err != nil {
		t.Fatalf("Fulfill: %v", err)
	}

	buyersOf := func(op *model.Admin) []shopBuyerView {
		rec := shopDo(f.srv, f.srv.handleShopBuyers,
			asAdmin(op, http.MethodGet, "/api/shop/buyers", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("buyers for %s: status %d, body %s", op.Username, rec.Code, rec.Body)
		}
		var out struct {
			Items []shopBuyerView `json:"items"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.Items
	}

	mine := buyersOf(f.seller)
	if len(mine) != 1 {
		t.Fatalf("the seller lists %d buyers, want the one who bought", len(mine))
	}
	b := mine[0]
	if b.TelegramID != 4242 || b.Orders != 1 || b.ClientID == 0 || b.Name == "" {
		t.Errorf("the buyer reads %+v", b)
	}
	if theirs := buyersOf(f.other); len(theirs) != 0 {
		t.Errorf("another operator lists %d of the seller's buyers", len(theirs))
	}
}

// An announcement is validated before any bot is asked to carry it, and
// a panel with no bot manager has none to send by: the text must be
// there and of a size a notice can be, and the manager must exist.
func TestShopAnnounceValidation(t *testing.T) {
	noBot := newShopFixture(t, false)
	rec := shopDo(noBot.srv, noBot.srv.handleShopAnnounce,
		asAdmin(noBot.seller, http.MethodPost, "/api/shop/announce",
			map[string]any{"text": "سلام"}))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("no bot manager: status %d, want 404 (body %s)", rec.Code, rec.Body)
	}

	f := newShopFixture(t, true)
	for name, body := range map[string]map[string]any{
		"empty":    {"text": ""},
		"blank":    {"text": "   "},
		"overlong": {"text": strings.Repeat("x", 2001)},
	} {
		rec := shopDo(f.srv, f.srv.handleShopAnnounce,
			asAdmin(f.seller, http.MethodPost, "/api/shop/announce", body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s text: status %d, want 400 (body %s)", name, rec.Code, rec.Body)
		}
	}
}

// A receipt kept in the panel is served to its own order's operator
// with its bytes and type; to another operator the order is not there,
// and an order with nothing kept has nothing to serve.
func TestShopOrderReceiptServing(t *testing.T) {
	f := newShopFixture(t, false)
	ctx := context.Background()
	order := shopTestOrder(t, f, f.seller.ID)
	picture := []byte("\xff\xd8fake-jpeg")
	if err := f.store.SaveReceipt(ctx, order.ID, "image/jpeg", picture); err != nil {
		t.Fatal(err)
	}
	plain := shopTestOrder(t, f, f.seller.ID)

	get := func(op *model.Admin, id uint) *httptest.ResponseRecorder {
		req := asAdmin(op, http.MethodGet, "/api/shop/orders/receipt", nil)
		req.SetPathValue("id", strconv.Itoa(int(id)))
		return shopDo(f.srv, f.srv.handleShopOrderReceipt, req)
	}

	rec := get(f.seller, order.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("the owner's receipt: status %d, body %s", rec.Code, rec.Body)
	}
	if rec.Body.String() != string(picture) {
		t.Errorf("the served bytes are %q, want the receipt that was kept", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("content type %q, want image/jpeg", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Errorf("cache control %q, want private, no-store", cc)
	}

	if rec := get(f.other, order.ID); rec.Code != http.StatusNotFound {
		t.Errorf("another operator's receipt: status %d, want 404", rec.Code)
	}
	if rec := get(f.seller, plain.ID); rec.Code != http.StatusNotFound {
		t.Errorf("an order with no receipt kept: status %d, want 404", rec.Code)
	}

	// The orders list says which orders have a picture to open.
	rec = shopDo(f.srv, f.srv.handleShopOrders,
		asAdmin(f.seller, http.MethodGet, "/api/shop/orders", nil))
	var list struct {
		Items []shopOrderView `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	marked := map[uint]bool{}
	for _, o := range list.Items {
		marked[o.ID] = o.HasReceipt
	}
	if !marked[order.ID] || marked[plain.ID] {
		t.Errorf("hasReceipt marks %v, want only order %d marked", marked, order.ID)
	}
}

// The gateway's way back: an order nobody has is a plain 404, and a
// buyer the gateway turned away gets the failure page, in Persian, that
// tells them what to do if money left their account anyway.
func TestShopCallbackUnknownAndFailed(t *testing.T) {
	f := newShopFixture(t, true)

	req := httptest.NewRequest(http.MethodGet, "/shop/pay/nope?Status=OK&Authority=A1", nil)
	req.SetPathValue("token", "nope")
	rec := shopDo(f.srv, f.srv.handleShopCallback, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown token: status %d, want 404", rec.Code)
	}

	order := shopTestOrder(t, f, f.seller.ID)
	req = httptest.NewRequest(http.MethodGet,
		"/shop/pay/"+order.PublicToken+"?Status=NOK&Authority=A1", nil)
	req.SetPathValue("token", order.PublicToken)
	rec = shopDo(f.srv, f.srv.handleShopCallback, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("failed payment: status %d, want the failure page", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "پرداخت تأیید نشد") {
		t.Errorf("the failure page does not say so: %s", rec.Body)
	}
}
