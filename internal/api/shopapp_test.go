package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abolfazl/w-ui/internal/shop"
)

// The mini app over the test shop stack: a real store, a real
// delivery path, and initData signed the way Telegram signs it. What
// is asserted is what the buyer, the wallet and the database each end
// up holding — and that a buyer sees only their own.

const shopAppToken = "123456:APP-TOKEN"

// signShopAppInitData builds an initData payload for a bot token the
// way the Telegram client does: the same check string and HMAC chain
// shop.ValidateInitData verifies.
func signShopAppInitData(token string, tgID int64, firstName string, authDate time.Time) string {
	user, _ := json.Marshal(map[string]any{"id": tgID, "first_name": firstName})
	fields := map[string]string{
		"auth_date": strconv.FormatInt(authDate.Unix(), 10),
		"query_id":  "AAE1",
		"user":      string(user),
	}
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var check strings.Builder
	for i, k := range keys {
		if i > 0 {
			check.WriteByte('\n')
		}
		check.WriteString(k + "=" + fields[k])
	}
	mac := hmac.New(sha256.New, []byte("WebAppData"))
	mac.Write([]byte(token))
	secret := mac.Sum(nil)
	mac = hmac.New(sha256.New, secret)
	mac.Write([]byte(check.String()))
	q := url.Values{}
	for k, v := range fields {
		q.Set(k, v)
	}
	q.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return q.Encode()
}

func buyerInitData(tgID int64) string {
	return signShopAppInitData(shopAppToken, tgID, "Buyer", time.Now())
}

// newShopAppFixture is the shop fixture with the seller's shop set up
// for the app: a token to sign against, a card to pay to, both
// tunnels on the list, a public base for links, and one plan.
func newShopAppFixture(t *testing.T) (*shopFixture, shop.Plan) {
	t.Helper()
	f := newShopFixture(t, false)
	ctx := context.Background()
	if err := f.store.SaveConfig(ctx, &shop.Config{
		OperatorID: f.seller.ID, Enabled: true, Token: shopAppToken,
		AdminChatID: 999, CardNumber: "6037991234567890", CardHolder: "Seller",
		InterfaceIDs: []uint{f.ifaces[0].ID, f.ifaces[1].ID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetPublicBase(ctx, "https://panel.example:12489"); err != nil {
		t.Fatal(err)
	}
	plan := shop.Plan{
		OperatorID: f.seller.ID, Name: "ماهانه", QuotaBytes: 50 << 30,
		Days: 30, PriceToman: 100000, DeviceLimit: 2, Enabled: true,
	}
	if err := f.store.SavePlan(ctx, &plan); err != nil {
		t.Fatal(err)
	}
	return f, plan
}

func shopAppCall(f *shopFixture, op uint, initData, action string, extra map[string]any) *httptest.ResponseRecorder {
	body := map[string]any{"op": op, "initData": initData, "action": action}
	for k, v := range extra {
		body[k] = v
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/shop/app/api", bytes.NewReader(raw))
	return shopDo(f.srv, f.srv.handleShopAppAPI, req)
}

type shopAppStateView struct {
	Name          string `json:"name"`
	Balance       int64  `json:"balance"`
	Subscriptions []struct {
		Name string `json:"name"`
		Link string `json:"link"`
	} `json:"subscriptions"`
	Sections []struct {
		Section string `json:"section"`
		Plans   []struct {
			ID uint `json:"id"`
		} `json:"plans"`
	} `json:"sections"`
	TrialAvailable  bool `json:"trialAvailable"`
	WheelEnabled    bool `json:"wheelEnabled"`
	CardEnabled     bool `json:"cardEnabled"`
	ZarinpalEnabled bool `json:"zarinpalEnabled"`
}

func shopAppState(t *testing.T, f *shopFixture, tgID int64) shopAppStateView {
	t.Helper()
	rec := shopAppCall(f, f.seller.ID, buyerInitData(tgID), "state", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("state: status %d, body %s", rec.Code, rec.Body)
	}
	var out shopAppStateView
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("state: decode: %v", err)
	}
	return out
}

// One buyer's delivered sale is in their state and in nobody else's:
// the signature names the buyer, and the scope is the seller's.
func TestShopAppStateShowsOnlyTheCallersSubscriptions(t *testing.T) {
	f, plan := newShopAppFixture(t)
	ctx := context.Background()
	order := &shop.Order{
		OperatorID: f.seller.ID, BuyerTGID: 4242, BuyerName: "Buyer",
		PlanID: plan.ID, PlanName: plan.Name, QuotaBytes: plan.QuotaBytes,
		Days: plan.Days, PriceToman: plan.PriceToman, DeviceLimit: plan.DeviceLimit,
		Kind: shop.KindNew, Method: shop.MethodCard, Status: shop.StatusPaid,
	}
	if err := f.store.CreateOrder(ctx, order); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Fulfill(ctx, order.ID); err != nil {
		t.Fatal(err)
	}

	mine := shopAppState(t, f, 4242)
	if len(mine.Subscriptions) != 1 {
		t.Fatalf("the buyer sees %d subscriptions, want 1", len(mine.Subscriptions))
	}
	if !strings.Contains(mine.Subscriptions[0].Link, "/subscribe/") {
		t.Errorf("the subscription link is %q", mine.Subscriptions[0].Link)
	}
	if len(mine.Sections) != 1 || len(mine.Sections[0].Plans) != 1 {
		t.Errorf("the shop offers %+v", mine.Sections)
	}
	if !mine.CardEnabled || mine.ZarinpalEnabled {
		t.Errorf("payment doors read card=%v zarinpal=%v", mine.CardEnabled, mine.ZarinpalEnabled)
	}

	stranger := shopAppState(t, f, 777)
	if len(stranger.Subscriptions) != 0 {
		t.Errorf("a stranger sees %d subscriptions", len(stranger.Subscriptions))
	}
}

// A wallet purchase is paid and delivered in the one call: the
// balance falls by the price, and the customer stands in the answer.
func TestShopAppBuyByWalletDebitsAndDelivers(t *testing.T) {
	f, plan := newShopAppFixture(t)
	ctx := context.Background()
	if err := f.store.CreditWallet(ctx, f.seller.ID, 4242, 150000); err != nil {
		t.Fatal(err)
	}
	rec := shopAppCall(f, f.seller.ID, buyerInitData(4242), "buy", map[string]any{
		"planId": plan.ID, "qty": 1, "method": "wallet",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("buy: status %d, body %s", rec.Code, rec.Body)
	}
	var out struct {
		Delivered     bool `json:"delivered"`
		Subscriptions []struct {
			Name string `json:"name"`
		} `json:"subscriptions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Delivered || len(out.Subscriptions) != 1 {
		t.Errorf("the answer is %+v", out)
	}
	w, err := f.store.WalletFor(ctx, f.seller.ID, 4242)
	if err != nil {
		t.Fatal(err)
	}
	if w.BalanceToman != 50000 {
		t.Errorf("the wallet holds %d after the purchase, want 50000", w.BalanceToman)
	}
}

// A card purchase hands back where the money goes and leaves the
// order standing where a receipt can reach it.
func TestShopAppBuyByCardReturnsTheCardAndWaits(t *testing.T) {
	f, plan := newShopAppFixture(t)
	rec := shopAppCall(f, f.seller.ID, buyerInitData(4242), "buy", map[string]any{
		"planId": plan.ID, "qty": 2, "method": "card",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("buy: status %d, body %s", rec.Code, rec.Body)
	}
	var out struct {
		OrderID    uint   `json:"orderID"`
		CardNumber string `json:"cardNumber"`
		CardHolder string `json:"cardHolder"`
		Amount     int64  `json:"amount"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.CardNumber != "6037991234567890" || out.CardHolder != "Seller" || out.Amount != 200000 {
		t.Errorf("the answer is %+v", out)
	}
	order, err := f.store.OrderByID(context.Background(), out.OrderID)
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != shop.StatusAwaitingReceipt || order.Method != shop.MethodCard || order.Quantity != 2 {
		t.Errorf("the order stands as %+v", order)
	}
}

// A top-up by card is the same road with a topup order at the end of
// it, and a wallet top-up is refused: a wallet cannot charge itself.
func TestShopAppTopupByCardAndNotByWallet(t *testing.T) {
	f, _ := newShopAppFixture(t)
	rec := shopAppCall(f, f.seller.ID, buyerInitData(4242), "topup", map[string]any{
		"amount": 80000, "method": "card",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("topup: status %d, body %s", rec.Code, rec.Body)
	}
	var out struct {
		OrderID uint  `json:"orderID"`
		Amount  int64 `json:"amount"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	order, err := f.store.OrderByID(context.Background(), out.OrderID)
	if err != nil {
		t.Fatal(err)
	}
	if order.Kind != shop.KindTopup || order.Status != shop.StatusAwaitingReceipt || order.PriceToman != 80000 {
		t.Errorf("the order stands as %+v", order)
	}

	rec = shopAppCall(f, f.seller.ID, buyerInitData(4242), "topup", map[string]any{
		"amount": 80000, "method": "wallet",
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a wallet top-up answered %d, want 400", rec.Code)
	}
}

// A gift code redeemed in the app lands in the wallet, and the
// answer says what arrived and what the wallet now holds.
func TestShopAppGiftCreditsTheWallet(t *testing.T) {
	f, _ := newShopAppFixture(t)
	ctx := context.Background()
	if err := f.store.SaveCode(ctx, &shop.Code{
		OperatorID: f.seller.ID, Code: "GIFT1", Kind: shop.CodeGift,
		Value: 20000, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	rec := shopAppCall(f, f.seller.ID, buyerInitData(4242), "gift", map[string]any{"code": "GIFT1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("gift: status %d, body %s", rec.Code, rec.Body)
	}
	var out struct {
		Credited int64 `json:"credited"`
		Balance  int64 `json:"balance"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Credited != 20000 || out.Balance != 20000 {
		t.Errorf("the answer is %+v", out)
	}
}

// The trial is given once: the first claim hands back a link, the
// second is refused in words.
func TestShopAppTrialOnlyOnce(t *testing.T) {
	f, _ := newShopAppFixture(t)
	ctx := context.Background()
	cfg, err := f.store.ConfigFor(ctx, f.seller.ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg.TrialEnabled = true
	cfg.TrialBytes = 1 << 30
	cfg.TrialDays = 7
	if err := f.store.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	rec := shopAppCall(f, f.seller.ID, buyerInitData(4242), "trial", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("trial: status %d, body %s", rec.Code, rec.Body)
	}
	var out struct {
		Link string `json:"link"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Link, "/subscribe/") {
		t.Errorf("the trial link is %q", out.Link)
	}

	rec = shopAppCall(f, f.seller.ID, buyerInitData(4242), "trial", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("the second trial answered %d, want 400 (body %s)", rec.Code, rec.Body)
	}
}

// The wheel turns once a day: the first spin answers with a prize,
// the second inside the fence is a refusal, not a second prize.
func TestShopAppSpinRefusesTheSecondSpin(t *testing.T) {
	f, _ := newShopAppFixture(t)
	ctx := context.Background()
	cfg, err := f.store.ConfigFor(ctx, f.seller.ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg.WheelEnabled = true
	if err := f.store.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	rec := shopAppCall(f, f.seller.ID, buyerInitData(4242), "spin", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("spin: status %d, body %s", rec.Code, rec.Body)
	}
	var out struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Kind == "" {
		t.Errorf("the spin answered %+v", out)
	}

	rec = shopAppCall(f, f.seller.ID, buyerInitData(4242), "spin", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("the second spin answered %d, want 400 (body %s)", rec.Code, rec.Body)
	}
}

// A signature made for another bot opens nothing here, and a shop
// with no token at all is not there — the two refusals the page's
// authority rests on.
func TestShopAppRefusesBadSignaturesAndTokenlessShops(t *testing.T) {
	f, _ := newShopAppFixture(t)
	forged := signShopAppInitData("999999:OTHER-TOKEN", 4242, "Buyer", time.Now())
	rec := shopAppCall(f, f.seller.ID, forged, "state", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("a forged signature answered %d, want 401", rec.Code)
	}

	rec = shopAppCall(f, f.other.ID, buyerInitData(4242), "state", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("a shop with no token answered %d, want 404", rec.Code)
	}
}

// The page itself is served off the subscription router, with its
// script externalised the way the listener's policy requires, and
// the API answers through the same door.
func TestShopAppPageAndAPIThroughTheRouter(t *testing.T) {
	f, _ := newShopAppFixture(t)
	handler := f.srv.SubscriptionRouter(http.NotFoundHandler())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/shop/app?op=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("page: status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "/sub-asset/shopapp.") || !strings.Contains(body, "<script src=") {
		t.Errorf("the page's script was not externalised:\n%.200s", body)
	}
	if strings.Contains(body, `"use strict"`) {
		t.Error("the page still carries its script inline")
	}

	raw, _ := json.Marshal(map[string]any{
		"op": f.seller.ID, "initData": buyerInitData(4242), "action": "state",
	})
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/shop/app/api", bytes.NewReader(raw)))
	if rec.Code != http.StatusOK {
		t.Errorf("api through the router: status %d, body %s", rec.Code, rec.Body)
	}
}
