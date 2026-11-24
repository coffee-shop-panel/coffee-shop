package shopbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/abolfazl/w-ui/internal/shop"
)

// ZarinPal, the online half of the shop's payments. The v4 API is three
// JSON calls; like Telegram's, it is spoken directly.
//
// ZarinpalBase is a variable so tests can point the client at a fake
// gateway. Production never changes it.
var ZarinpalBase = "https://payment.zarinpal.com"

var zarinpalHTTP = &http.Client{Timeout: 30 * time.Second}

// zarinpalAmount is the one place a price becomes a gateway amount.
// ZarinPal's v4 API counts in Toman — the same unit the panel prices
// plans in — so today this is the identity. It exists so that if the
// gateway ever changes its unit, the conversion has exactly one home
// and every call site is already right.
func zarinpalAmount(priceToman int64) int64 { return priceToman }

// zpEnvelope is the gateway's answer shape. data is an object on success
// and an empty array on failure, which is why it is kept raw and only
// decoded once it is known to be an object.
type zpEnvelope struct {
	Data   json.RawMessage `json:"data"`
	Errors json.RawMessage `json:"errors"`
}

func (e *zpEnvelope) dataInto(out any) bool {
	if len(e.Data) == 0 || string(e.Data) == "[]" || string(e.Data) == "null" {
		return false
	}
	return json.Unmarshal(e.Data, out) == nil
}

// errorCode is the gateway's numeric complaint, or 0 when it sent none.
func (e *zpEnvelope) errorCode() int {
	var one struct {
		Code int `json:"code"`
	}
	if json.Unmarshal(e.Errors, &one) == nil {
		return one.Code
	}
	return 0
}

// errorText is the gateway's own explanation of a refusal, best effort:
// the errors field has worn several shapes over the API's life.
func (e *zpEnvelope) errorText() string {
	if len(e.Errors) == 0 || string(e.Errors) == "[]" {
		return ""
	}
	var one struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	}
	if json.Unmarshal(e.Errors, &one) == nil && one.Message != "" {
		return one.Message
	}
	var many []struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(e.Errors, &many) == nil {
		for _, m := range many {
			if m.Message != "" {
				return m.Message
			}
		}
	}
	return strings.TrimSpace(string(e.Errors))
}

func zarinpalCall(ctx context.Context, path string, payload map[string]any) (*zpEnvelope, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ZarinpalBase+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := zarinpalHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var env zpEnvelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&env); err != nil {
		return nil, fmt.Errorf("zarinpal %s: %s: %w", path, resp.Status, err)
	}
	return &env, nil
}

// RequestPayment opens a payment for an order and returns its authority,
// the handle both the payment page and the later verification key on.
func RequestPayment(ctx context.Context, merchant string, priceToman int64, description, callbackURL string) (string, error) {
	env, err := zarinpalCall(ctx, "/pg/v4/payment/request.json", map[string]any{
		"merchant_id":  merchant,
		"amount":       zarinpalAmount(priceToman),
		"description":  description,
		"callback_url": callbackURL,
	})
	if err != nil {
		return "", err
	}
	var data struct {
		Code      int    `json:"code"`
		Message   string `json:"message"`
		Authority string `json:"authority"`
	}
	if !env.dataInto(&data) || data.Code != 100 || data.Authority == "" {
		msg := env.errorText()
		if msg == "" {
			msg = data.Message
		}
		if msg == "" {
			msg = "the gateway refused the payment request"
		}
		return "", fmt.Errorf("zarinpal: %s", msg)
	}
	return data.Authority, nil
}

// PayURL is where the buyer pays for an authority.
func PayURL(authority string) string {
	return ZarinpalBase + "/pg/StartPay/" + authority
}

// VerifyPayment asks the gateway whether an authority was paid. Paid is
// a fact, not an error: a buyer who closed the payment page gets a calm
// false, and only a gateway that cannot be understood gets an error.
// Code 101 is "already verified" — the money was taken and an earlier
// check already recorded it, so it counts as paid.
func VerifyPayment(ctx context.Context, merchant, authority string, priceToman int64) (paid bool, refID string, err error) {
	env, err := zarinpalCall(ctx, "/pg/v4/payment/verify.json", map[string]any{
		"merchant_id": merchant,
		"authority":   authority,
		"amount":      zarinpalAmount(priceToman),
	})
	if err != nil {
		return false, "", err
	}
	var data struct {
		Code  int   `json:"code"`
		RefID int64 `json:"ref_id"`
	}
	if !env.dataInto(&data) {
		// An unpaid session is reported with an error code of its own
		// (-51) rather than a data block. That is "not paid", the calm
		// answer — not a failure to ask.
		if env.errorCode() == -51 {
			return false, "", nil
		}
		if msg := env.errorText(); msg != "" {
			return false, "", fmt.Errorf("zarinpal: %s", msg)
		}
		return false, "", errors.New("zarinpal: empty answer")
	}
	switch data.Code {
	case 100, 101:
		return true, fmt.Sprint(data.RefID), nil
	}
	return false, "", nil
}

// VerifyZarinpal checks one order's payment with the gateway and, when
// it was paid, marks the order paid with its reference id. It does not
// fulfill: the caller decides when the customer is created or renewed —
// the web callback and the bot's "I paid" button share this check and
// differ in what they do next.
func (m *Manager) VerifyZarinpal(ctx context.Context, o *shop.Order) (bool, error) {
	if o.Authority == "" {
		return false, nil
	}
	cfg, err := m.shop.ConfigFor(ctx, o.OperatorID)
	if err != nil {
		return false, err
	}
	if cfg.ZarinpalMerchant == "" {
		return false, errors.New("shopbot: this operator has no ZarinPal merchant configured")
	}
	paid, refID, err := VerifyPayment(ctx, cfg.ZarinpalMerchant, o.Authority, o.PriceToman)
	if err != nil || !paid {
		return paid, err
	}
	o.RefID = refID
	o.Status = "paid"
	if err := m.shop.UpdateOrder(ctx, o); err != nil {
		return false, err
	}
	return true, nil
}
