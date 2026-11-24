package shopbot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/abolfazl/w-ui/internal/shop"
)

// fakeZarinpal is the gateway, scripted: what a payment is worth and
// whether it ever happened are the test's to choose.
type fakeZarinpal struct {
	verifyMode  string // "paid", "already", "unpaid-data", "unpaid-errors"
	requestFail bool
	amounts     []int64
	callback    string
}

func newFakeZarinpal(t *testing.T, z *fakeZarinpal) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/pg/v4/payment/request.json", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Amount     int64  `json:"amount"`
			Callback   string `json:"callback_url"`
			MerchantID string `json:"merchant_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		z.amounts = append(z.amounts, body.Amount)
		z.callback = body.Callback
		w.Header().Set("Content-Type", "application/json")
		if z.requestFail {
			fmt.Fprint(w, `{"data":[],"errors":{"code":-9,"message":"invalid merchant"}}`)
			return
		}
		fmt.Fprint(w, `{"data":{"code":100,"message":"Success","authority":"A00000000000000000000000000000001","fee_type":"Customer","fee":1000},"errors":[]}`)
	})
	mux.HandleFunc("/pg/v4/payment/verify.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch z.verifyMode {
		case "paid":
			fmt.Fprint(w, `{"data":{"code":100,"message":"Verified","ref_id":555001},"errors":[]}`)
		case "already":
			fmt.Fprint(w, `{"data":{"code":101,"message":"Verified","ref_id":555001},"errors":[]}`)
		case "unpaid-data":
			fmt.Fprint(w, `{"data":{"code":-51,"message":"Session is not valid"},"errors":[]}`)
		case "unpaid-errors":
			fmt.Fprint(w, `{"data":[],"errors":{"code":-51,"message":"Session is not valid"}}`)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func useZarinpal(t *testing.T, srv *httptest.Server) {
	t.Helper()
	old := ZarinpalBase
	ZarinpalBase = srv.URL
	t.Cleanup(func() { ZarinpalBase = old })
}

// TestTheGatewayRoundTrip: an authority is opened for the price asked,
// in Toman, and a paid verification hands back its reference.
func TestTheGatewayRoundTrip(t *testing.T) {
	z := &fakeZarinpal{verifyMode: "paid"}
	useZarinpal(t, newFakeZarinpal(t, z))
	ctx := context.Background()

	authority, err := RequestPayment(ctx, "merchant-1", 120000, "خرید اشتراک", "https://panel.example/shop/callback/tok")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if authority == "" {
		t.Fatal("no authority came back")
	}
	if got := PayURL(authority); !strings.HasSuffix(got, "/pg/StartPay/"+authority) {
		t.Errorf("pay URL is %q", got)
	}
	if len(z.amounts) != 1 || z.amounts[0] != 120000 {
		t.Errorf("the gateway was asked for %v, want the price in Toman, unchanged", z.amounts)
	}
	if z.callback != "https://panel.example/shop/callback/tok" {
		t.Errorf("callback is %q", z.callback)
	}

	paid, ref, err := VerifyPayment(ctx, "merchant-1", authority, 120000)
	if err != nil || !paid || ref != "555001" {
		t.Errorf("verify: paid=%v ref=%q err=%v", paid, ref, err)
	}

	// Already verified is still paid: the money was taken once, and an
	// earlier check having recorded it does not unspend it.
	z.verifyMode = "already"
	paid, _, err = VerifyPayment(ctx, "merchant-1", authority, 120000)
	if err != nil || !paid {
		t.Errorf("verify (already): paid=%v err=%v", paid, err)
	}

	// Not paid is a fact in either shape the gateway reports it, never
	// an error: the buyer is told to finish paying, not that the
	// gateway broke.
	for _, mode := range []string{"unpaid-data", "unpaid-errors"} {
		z.verifyMode = mode
		paid, _, err = VerifyPayment(ctx, "merchant-1", authority, 120000)
		if err != nil || paid {
			t.Errorf("verify (%s): paid=%v err=%v, want a calm false", mode, paid, err)
		}
	}
}

// TestARefusedRequestSaysWhy: a gateway refusal carries its own words
// into the error, so the operator is not left guessing at a bare code.
func TestARefusedRequestSaysWhy(t *testing.T) {
	z := &fakeZarinpal{requestFail: true}
	useZarinpal(t, newFakeZarinpal(t, z))

	_, err := RequestPayment(context.Background(), "bad-merchant", 1000, "x", "https://panel.example/cb")
	if err == nil || !strings.Contains(err.Error(), "invalid merchant") {
		t.Errorf("err = %v, want the gateway's own message", err)
	}
}

// TestVerifyZarinpalMarksTheOrderPaid: the manager's half of the check —
// the order learns its reference and its state, and fulfillment is left
// to the caller.
func TestVerifyZarinpalMarksTheOrderPaid(t *testing.T) {
	fx := newShopFixture(t)
	ctx := context.Background()

	if err := fx.sh.SaveConfig(ctx, &shop.Config{
		OperatorID: fx.opID, Enabled: true, Token: "TESTTOKEN",
		AdminChatID: 999, ZarinpalMerchant: "merchant-1",
	}); err != nil {
		t.Fatalf("save config: %v", err)
	}
	o := &shop.Order{
		OperatorID: fx.opID, BuyerTGID: 111, BuyerName: "Buyer",
		PlanID: fx.plan.ID, PlanName: fx.plan.Name, PriceToman: fx.plan.PriceToman,
		Kind: "new", Method: "zarinpal", Status: "unpaid", Authority: "A0001",
	}
	if err := fx.sh.CreateOrder(ctx, o); err != nil {
		t.Fatalf("create order: %v", err)
	}

	z := &fakeZarinpal{verifyMode: "unpaid-data"}
	useZarinpal(t, newFakeZarinpal(t, z))
	mgr := New(fx.sh, fx.clients, quietLog())

	paid, err := mgr.VerifyZarinpal(ctx, o)
	if err != nil || paid {
		t.Fatalf("unpaid: paid=%v err=%v", paid, err)
	}
	if fresh, _ := fx.sh.OrderByID(ctx, o.ID); fresh.Status != "unpaid" || fresh.RefID != "" {
		t.Errorf("the order moved on an unpaid check: %+v", fresh)
	}

	z.verifyMode = "paid"
	paid, err = mgr.VerifyZarinpal(ctx, o)
	if err != nil || !paid {
		t.Fatalf("paid: paid=%v err=%v", paid, err)
	}
	fresh, err := fx.sh.OrderByID(ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != "paid" || fresh.RefID != "555001" {
		t.Errorf("the order after a paid check: %+v", fresh)
	}
	if fresh.ClientID != 0 {
		t.Errorf("verification created a customer; fulfillment is the caller's job")
	}
}
