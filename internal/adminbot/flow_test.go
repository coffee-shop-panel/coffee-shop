package adminbot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/ipam"
	"github.com/abolfazl/w-ui/internal/scope"
	"github.com/abolfazl/w-ui/internal/service"
	"github.com/abolfazl/w-ui/internal/shop"
)

// The conversation is tested the way it runs, minus the polling: a
// real customer service and a real shop on a real database, a fake
// Telegram on the other end of the wire, and updates handed to the
// bot's own dispatch one at a time. What is asserted is what the
// operator was shown and what the database ended up holding — above
// all that a reseller's bot never reaches past the reseller's fence.

const (
	ownerChat     = 777
	strangerChat  = 555
	testBotToken  = "TESTTOKEN"
	testBotUserID = 4242
)

// flowFixture is a panel with an owner and a reseller, two tunnels
// the reseller is granted (one WireGuard, one adopted 3x-ui inbound),
// and the services the bots run on.
type flowFixture struct {
	db      *gorm.DB
	clients *service.Clients
	shop    *shop.Shop
	mgr     *Manager
	owner   *model.Admin
	seller  *model.Admin
	wg      model.Interface
	xray    model.Interface
}

func newFlowFixture(t *testing.T) *flowFixture {
	t.Helper()
	db := testDB(t)
	if err := scope.Register(db); err != nil {
		t.Fatalf("register scoping: %v", err)
	}
	if err := db.Create(&model.Node{Name: "local", Kind: model.KindLocal}).Error; err != nil {
		t.Fatal(err)
	}
	pools := ipam.NewPools()

	wg := model.Interface{
		Name: "wg0", Protocol: model.ProtocolWireGuard, Enabled: true,
		ListenPort: 51820, Subnet: "10.66.0.0/24", EndpointHost: "vpn.example.com",
		MTU: 1420, NodeID: 1,
	}
	if err := db.Create(&wg).Error; err != nil {
		t.Fatalf("seed wireguard interface: %v", err)
	}
	if _, err := pools.Add(wg.ID, wg.Subnet); err != nil {
		t.Fatalf("pool: %v", err)
	}
	// The V2ray half: an inbound adopted from a 3x-ui node. Three
	// things make it one a customer can be put on — it must say
	// customers may be added (XUI.Customers), carry a kind a secret
	// can be drawn for, and have a pool for its subnet — because
	// delivery checks each in turn; the shop's fixtures found the
	// same three.
	xray := model.Interface{
		Name: "vless-1", Protocol: model.ProtocolXray, Enabled: true,
		ListenPort: 443, Subnet: "10.9.9.0/24", EndpointHost: "xray.example.com",
		NodeID: 1,
		XUI: model.JSON(model.XUIInbound{
			RemoteID: 39, Kind: "vless", Customers: true, Enabled: true,
		}),
	}
	if err := db.Create(&xray).Error; err != nil {
		t.Fatalf("seed xray interface: %v", err)
	}
	if _, err := pools.Add(xray.ID, xray.Subnet); err != nil {
		t.Fatalf("xray pool: %v", err)
	}

	owner := &model.Admin{Username: "owner", PasswordHash: "x", Role: model.RoleOwner, Enabled: true}
	if err := db.Create(owner).Error; err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	seller := &model.Admin{Username: "hussain", PasswordHash: "x", Role: model.RoleReseller, Enabled: true}
	if err := db.Create(seller).Error; err != nil {
		t.Fatalf("seed reseller: %v", err)
	}
	for _, iface := range []model.Interface{wg, xray} {
		if err := db.Create(&model.AdminInterface{AdminID: seller.ID, InterfaceID: iface.ID}).Error; err != nil {
			t.Fatalf("grant interface: %v", err)
		}
	}

	clients := service.NewClients(db, pools, quietLog())
	sh := shop.New(db, clients, quietLog())
	return &flowFixture{
		db: db, clients: clients, shop: sh,
		mgr:   New(db, clients, sh, quietLog()),
		owner: owner, seller: seller, wg: wg, xray: xray,
	}
}

// bot builds one operator's bot on the fixture's manager, wired to
// the fake Telegram, without the polling loop: the tests hand updates
// to its dispatch directly.
func (f *flowFixture) bot(t *testing.T, operatorID, chatID int64) *bot {
	t.Helper()
	b := &bot{
		mgr:    f.mgr,
		api:    newTGClient(testBotToken),
		log:    quietLog(),
		cancel: func() {},
		done:   make(chan struct{}),
	}
	b.setConfig(Config{
		OperatorID:  uint(operatorID),
		Enabled:     true,
		Token:       testBotToken,
		AdminChatID: chatID,
	})
	return b
}

// createFor makes a customer under an operator's own scope, the way
// the bot itself would be served.
func (f *flowFixture) createFor(t *testing.T, operatorID uint, name string, quota uint64, days int) *model.Client {
	t.Helper()
	ctx := context.Background()
	opCtx, err := f.shop.OperatorCtx(ctx, operatorID)
	if err != nil {
		t.Fatal(err)
	}
	in := service.CreateInput{
		Name: name, InterfaceIDs: []uint{f.wg.ID},
		QuotaBytes: quota, DeviceLimit: 1,
	}
	if days > 0 {
		expires := time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)
		in.ExpiresAt = &expires
	}
	c, err := f.clients.Create(opCtx, in)
	if err != nil {
		t.Fatalf("create %q: %v", name, err)
	}
	return c
}

// fakeAdminTG is Telegram, scripted by the test: everything the bot
// says is kept for inspection, flattened the way the shopbot's fake
// flattens it — text plus the keyboard's JSON — so one substring
// search sees a whole screen.
type fakeAdminTG struct {
	mu      sync.Mutex
	sent    []fakeAdminMsg
	edits   []fakeAdminMsg
	answers []fakeAdminAnswer
	// blocked is the chats Telegram refuses to deliver to: an
	// operator who blocked their own bot. A send to one fails the
	// way Telegram fails it, and nothing is recorded as delivered.
	blocked map[int64]bool
}

type fakeAdminMsg struct {
	chatID int64
	flat   string
}

type fakeAdminAnswer struct {
	text  string
	alert bool
}

func newFakeAdminTG(t *testing.T) *fakeAdminTG {
	t.Helper()
	f := &fakeAdminTG{blocked: map[int64]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	old := APIBase
	APIBase = srv.URL
	t.Cleanup(func() { APIBase = old })
	return f
}

func (f *fakeAdminTG) serve(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	var params map[string]any
	_ = json.NewDecoder(r.Body).Decode(&params)

	chatID, _ := params["chat_id"].(float64)
	if method == "sendMessage" && f.blocked[int64(chatID)] {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`)
		return
	}
	flat := ""
	if text, ok := params["text"].(string); ok {
		flat += text
	}
	if markup, ok := params["reply_markup"]; ok {
		raw, _ := json.Marshal(markup)
		flat += " " + string(raw)
	}

	f.mu.Lock()
	switch method {
	case "sendMessage":
		f.sent = append(f.sent, fakeAdminMsg{chatID: int64(chatID), flat: flat})
	case "editMessageText":
		f.edits = append(f.edits, fakeAdminMsg{chatID: int64(chatID), flat: flat})
	case "answerCallbackQuery":
		text, _ := params["text"].(string)
		alert, _ := params["show_alert"].(bool)
		f.answers = append(f.answers, fakeAdminAnswer{text: text, alert: alert})
	}
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if method == "sendMessage" {
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":9001,"chat":{"id":%d}}}`, int64(chatID))
		return
	}
	fmt.Fprint(w, `{"ok":true,"result":true}`)
}

// said reports whether anything the bot sent or edited into a message
// holds sub.
func (f *fakeAdminTG) said(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, list := range [][]fakeAdminMsg{f.sent, f.edits} {
		for _, m := range list {
			if strings.Contains(m.flat, sub) {
				return true
			}
		}
	}
	return false
}

// saidTo is said, for one chat only.
func (f *fakeAdminTG) saidTo(chatID int64, sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, m := range f.sent {
		if m.chatID == chatID && strings.Contains(m.flat, sub) {
			return true
		}
	}
	return false
}

func (f *fakeAdminTG) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func (f *fakeAdminTG) lastAnswer() (fakeAdminAnswer, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.answers) == 0 {
		return fakeAdminAnswer{}, false
	}
	return f.answers[len(f.answers)-1], true
}

func msgUpd(from, chat int64, text string) tgUpdate {
	return tgUpdate{ID: 1, Message: &tgMessage{
		ID:   10,
		From: &tgUser{ID: from, FirstName: "Op"},
		Chat: tgChat{ID: chat},
		Text: text,
	}}
}

func cbUpd(from, chat, msgID int64, data string) tgUpdate {
	return tgUpdate{ID: 2, Callback: &tgCallback{
		ID:   "cb",
		From: &tgUser{ID: from, FirstName: "Op"},
		Message: &tgMessage{
			ID:   msgID,
			From: &tgUser{ID: testBotUserID, FirstName: "AdminBot"},
			Chat: tgChat{ID: chat},
		},
		Data: data,
	}}
}

// A stranger gets the refusal and nothing else: no menu, no screen, no
// state, and — the point of the guard — no customer made or changed
// however they press.
func TestAStrangerGetsOnlyTheRefusal(t *testing.T) {
	fx := newFlowFixture(t)
	fake := newFakeAdminTG(t)
	ctx := context.Background()
	b := fx.bot(t, int64(fx.seller.ID), ownerChat)

	b.handle(ctx, msgUpd(strangerChat, strangerChat, "/start"))
	if !fake.saidTo(strangerChat, txtNotOwner) {
		t.Fatal("the stranger was not refused")
	}
	if fake.said(btnMyClients) {
		t.Fatal("the stranger was shown the menu")
	}
	if n := fake.sentCount(); n != 1 {
		t.Fatalf("the stranger got %d messages, want exactly the refusal", n)
	}

	// A stranger's presses are refused as alerts and go no further:
	// the list does not render, and the wizard does not start.
	b.handle(ctx, cbUpd(strangerChat, strangerChat, 500, "list"))
	ans, ok := fake.lastAnswer()
	if !ok || ans.text != txtNotOwner || !ans.alert {
		t.Fatalf("the stranger's press was answered %+v, want the refusal as an alert", ans)
	}
	b.handle(ctx, cbUpd(strangerChat, strangerChat, 500, "add"))
	b.handle(ctx, msgUpd(strangerChat, strangerChat, "مشتری دزدی"))
	if fake.said("wg0") {
		t.Fatal("the stranger reached the wizard's tunnels")
	}

	var customers int64
	if err := fx.db.Model(&model.Client{}).Count(&customers).Error; err != nil {
		t.Fatal(err)
	}
	if customers != 0 {
		t.Fatalf("a stranger's conversation made %d customers", customers)
	}
}

// The reseller's list is the reseller's book: their own customer is
// there, the owner's is not, and the owner's customer cannot be
// opened through the reseller's bot either.
func TestTheResellersListShowsOnlyTheirOwn(t *testing.T) {
	fx := newFlowFixture(t)
	fake := newFakeAdminTG(t)
	ctx := context.Background()
	own := fx.createFor(t, fx.owner.ID, "مشتری مالک", 10<<30, 30)
	fx.createFor(t, fx.seller.ID, "مشتری نماینده", 10<<30, 30)

	b := fx.bot(t, int64(fx.seller.ID), ownerChat)
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "list"))
	if !fake.said("مشتری نماینده") {
		t.Fatal("the reseller's own customer is not in their list")
	}
	if fake.said("مشتری مالک") {
		t.Fatal("the owner's customer leaked into the reseller's list")
	}

	// The fence is not only the list's: the owner's customer's detail
	// does not open under the reseller's scope.
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, fmt.Sprintf("c:%d", own.ID)))
	if fake.said("مشتری مالک") {
		t.Fatal("the owner's customer's detail opened in the reseller's bot")
	}

	// Search is fenced the same way.
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "search"))
	b.handle(ctx, msgUpd(ownerChat, ownerChat, "نماینده"))
	if !fake.saidTo(ownerChat, "مشتری نماینده") {
		t.Fatal("the search did not find the reseller's customer")
	}
}

// The wizard end to end: a name, the WireGuard section, twenty
// gigabytes, thirty days, one device — and the customer that lands in
// the database is the reseller's, on those terms, with a token to
// hand over.
func TestTheWizardBuildsACustomer(t *testing.T) {
	fx := newFlowFixture(t)
	fake := newFakeAdminTG(t)
	ctx := context.Background()
	b := fx.bot(t, int64(fx.seller.ID), ownerChat)

	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "add"))
	if !fake.said("نام مشتری را بفرستید") {
		t.Fatal("the wizard did not ask for a name")
	}
	b.handle(ctx, msgUpd(ownerChat, ownerChat, "مشتری تستی"))
	if !fake.said("کدام بخش") {
		t.Fatal("the wizard did not offer the sections")
	}
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "wzsec:wireguard"))
	if !fake.said("حجم اشتراک") {
		t.Fatal("the wizard did not ask for a volume")
	}
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "wzq:20"))
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "wzd:30"))
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "wzv:1"))
	if !fake.said("مشتری تستی") || !fake.said("۲۰ گیگابایت") {
		t.Fatal("the summary does not read the form back")
	}
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "wzmake"))

	opCtx, err := fx.shop.OperatorCtx(ctx, fx.seller.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := fx.clients.List(opCtx, service.ListFilter{Search: "تستی"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("the wizard made %d customers, want 1", page.Total)
	}
	c, err := fx.clients.Get(opCtx, page.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if c.OwnerID != fx.seller.ID {
		t.Errorf("the customer belongs to %d, want the reseller %d", c.OwnerID, fx.seller.ID)
	}
	if c.QuotaBytes != 20<<30 {
		t.Errorf("quota is %d bytes, want 20 GB", c.QuotaBytes)
	}
	if c.DeviceLimit != 1 {
		t.Errorf("device limit is %d, want 1", c.DeviceLimit)
	}
	if c.ExpiresAt == nil {
		t.Fatal("the customer has no expiry")
	}
	if until := time.Until(*c.ExpiresAt); until < 29*24*time.Hour || until > 31*24*time.Hour {
		t.Errorf("the expiry is %v away, want about thirty days", until)
	}
	if len(c.Accounts) != 1 || c.Accounts[0].InterfaceID != fx.wg.ID {
		t.Fatalf("the customer sits on %+v, want one account on wg0", c.Accounts)
	}
	if c.SubToken == "" {
		t.Fatal("the customer has no subscription token")
	}
	if !fake.said("ساخته شد") || !fake.said(c.SubToken) {
		t.Fatal("the success message does not carry the delivery")
	}
}

// The detail screen's acts: a reset zeroes usage, a cut takes the
// customer off and a restore brings them back, the presets move the
// expiry and grow the quota, and a confirmed delete removes them.
func TestDetailActionsCutExtendResetDelete(t *testing.T) {
	fx := newFlowFixture(t)
	fake := newFakeAdminTG(t)
	ctx := context.Background()
	c := fx.createFor(t, fx.seller.ID, "مشتری آزمایشی", 10<<30, 10)
	id := fmt.Sprint(c.ID)

	opCtx, err := fx.shop.OperatorCtx(ctx, fx.seller.ID)
	if err != nil {
		t.Fatal(err)
	}
	get := func() *model.Client {
		t.Helper()
		got, err := fx.clients.Get(opCtx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	b := fx.bot(t, int64(fx.seller.ID), ownerChat)
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "c:"+id))
	if !fake.said("مشتری آزمایشی") || !fake.said(txtStatusActive) {
		t.Fatal("the detail did not render the customer")
	}

	// Usage first, so the reset has something to zero.
	if err := fx.db.Model(&model.Client{}).Where("id = ?", c.ID).
		Update("used_bytes", uint64(5<<30)).Error; err != nil {
		t.Fatal(err)
	}
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "rst:"+id))
	if !fake.said("صفر شود") {
		t.Fatal("the reset did not ask first")
	}
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "rsty:"+id))
	if got := get(); got.UsedBytes != 0 {
		t.Fatalf("usage after the reset is %d, want 0", got.UsedBytes)
	}

	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "cut:"+id))
	if got := get(); got.Status != model.StatusDisabled {
		t.Fatalf("status after the cut is %s, want disabled", got.Status)
	}
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "uncut:"+id))
	if got := get(); got.Status != model.StatusActive {
		t.Fatalf("status after the restore is %s, want active", got.Status)
	}

	before := get().ExpiresAt
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "extd:"+id+":30"))
	after := get().ExpiresAt
	if before == nil || after == nil {
		t.Fatal("the expiry vanished across the extension")
	}
	if drift := after.Sub(*before); drift < 29*24*time.Hour || drift > 31*24*time.Hour {
		t.Fatalf("the extension moved the expiry by %v, want thirty days", drift)
	}

	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "quod:"+id+":10"))
	if got := get(); got.QuotaBytes != 20<<30 {
		t.Fatalf("quota after the top-up is %d, want 20 GB", got.QuotaBytes)
	}

	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "del:"+id))
	if !fake.said("کاملاً حذف شود") {
		t.Fatal("the delete did not ask first, naming the customer")
	}
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "dely:"+id))
	if _, err := fx.clients.Get(opCtx, c.ID); err == nil {
		t.Fatal("the customer is still there after the confirmed delete")
	}
}

// The stats screen is the operator's own totals: it renders for an
// operator with a book and for one with none alike.
func TestStatsRenderWithAndWithoutCustomers(t *testing.T) {
	fx := newFlowFixture(t)
	fake := newFakeAdminTG(t)
	ctx := context.Background()

	one := fx.createFor(t, fx.seller.ID, "مشتری یک", 10<<30, 30)
	fx.createFor(t, fx.seller.ID, "مشتری دو", 10<<30, 30)
	opCtx, err := fx.shop.OperatorCtx(ctx, fx.seller.ID)
	if err != nil {
		t.Fatal(err)
	}
	disabled := model.StatusDisabled
	if _, err := fx.clients.Update(opCtx, one.ID, service.UpdateInput{Status: &disabled}); err != nil {
		t.Fatal(err)
	}

	b := fx.bot(t, int64(fx.seller.ID), ownerChat)
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "stats"))
	if !fake.said(txtStatsTitle) {
		t.Fatal("the stats screen did not render")
	}
	if !fake.said(fmt.Sprintf(txtStatsTotal, faNum(2))) {
		t.Fatal("the stats do not count the reseller's two customers")
	}
	if !fake.said(fmt.Sprintf(txtStatsDisabled, faNum(1))) {
		t.Fatal("the stats do not count the disabled customer")
	}

	// An operator with no customers gets the same screen at zero.
	// (Not the owner: the owner's book is the whole panel's, the
	// reseller's two included.)
	fresh := &model.Admin{Username: "fresh", PasswordHash: "x", Role: model.RoleReseller, Enabled: true}
	if err := fx.db.Create(fresh).Error; err != nil {
		t.Fatal(err)
	}
	freshBot := fx.bot(t, int64(fresh.ID), ownerChat)
	freshBot.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "stats"))
	if !fake.said(fmt.Sprintf(txtStatsTotal, faNum(0))) {
		t.Fatal("the empty book's stats did not render at zero")
	}
}

// The wizard's «both» section: one tap gives the customer every
// tunnel of both kinds the operator may give — here the WireGuard
// tunnel and the adopted V2ray inbound alike.
func TestTheWizardBothSectionsGiveEveryTunnel(t *testing.T) {
	fx := newFlowFixture(t)
	fake := newFakeAdminTG(t)
	ctx := context.Background()
	b := fx.bot(t, int64(fx.seller.ID), ownerChat)

	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "add"))
	b.handle(ctx, msgUpd(ownerChat, ownerChat, "مشتری هر دو"))
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "wzsec:both"))
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "wzq:20"))
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "wzd:30"))
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "wzv:1"))
	if !fake.said("هر دو بخش") {
		t.Fatal("the summary does not name the both-sections choice")
	}
	b.handle(ctx, cbUpd(ownerChat, ownerChat, 500, "wzmake"))

	opCtx, err := fx.shop.OperatorCtx(ctx, fx.seller.ID)
	if err != nil {
		t.Fatal(err)
	}
	page, err := fx.clients.List(opCtx, service.ListFilter{Search: "هر دو"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("the wizard made %d customers, want 1", page.Total)
	}
	c, err := fx.clients.Get(opCtx, page.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[uint]bool{}
	for _, a := range c.Accounts {
		got[a.InterfaceID] = true
	}
	if len(c.Accounts) != 2 || !got[fx.wg.ID] || !got[fx.xray.ID] {
		t.Fatalf("the customer sits on %+v, want one account on each of wg0 and the V2ray inbound", c.Accounts)
	}
}
