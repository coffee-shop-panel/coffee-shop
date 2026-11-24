package api

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/abolfazl/w-ui/internal/service"
	"github.com/abolfazl/w-ui/internal/shop"
	"github.com/abolfazl/w-ui/internal/shopbot"
)

// The shops' Telegram Mini App: the buyer's own storefront inside
// Telegram.
//
// A shop's bot menu carries a «فروشگاه» button that opens this page
// in Telegram's web view. The page is a customer of one small JSON
// API beside it, and the API's whole authority is the initData the
// Telegram client signs for the page: every call carries it, and
// every call is checked against the operator's own bot token
// (shop.ValidateInitData) before anything is read or written. There
// is no session and no cookie — a signature per call, scoped to the
// one operator the page names, and every read and write under that
// operator's own scope, exactly as their bot's are.
//
// It hangs off the subscription listener beside the payment callback
// (shop.go): the same public door the buyers' browsers already know,
// named by the panel's public base the bots hand out.

//go:embed assets/shopapp.html
var shopAppPageHTML []byte

// shopAppServedPage is the page as served: its one inline script
// taken out and registered as a file of its own, the coffee page's
// arrangement (subcoffee.go), because the listener's content policy
// allows scripts only from its own origin. The page loads no
// external script at all — window.Telegram is the client itself
// putting it there when the page is opened as a Mini App.
var shopAppServedPage = shopAppExternalScript(shopAppPageHTML)

func shopAppExternalScript(page []byte) []byte {
	open := []byte("<script>")
	start := bytes.Index(page, open)
	if start < 0 {
		return page
	}
	bodyStart := start + len(open)
	end := bytes.Index(page[bodyStart:], []byte("</script>"))
	if end < 0 {
		return page
	}
	body := page[bodyStart : bodyStart+end]
	src := registerSubAsset("shopapp", ".js", "text/javascript; charset=utf-8", body)
	out := make([]byte, 0, len(page))
	out = append(out, page[:start]...)
	out = append(out, []byte(`<script src="`+src+`"></script>`)...)
	out = append(out, page[bodyStart+end+len("</script>"):]...)
	return out
}

// serveShopApp is the subscription router's half of the mini app: it
// recognises the page and its API on either listener, the way
// serveShopCallback recognises the gateway's way back in. True means
// the request was the app's, answered or refused.
func (s *Server) serveShopApp(w http.ResponseWriter, r *http.Request) bool {
	switch {
	case r.URL.Path == "/shop/app" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		s.serveShopAppPage(w, r)
		return true
	case r.URL.Path == "/shop/app/api" && r.Method == http.MethodPost:
		s.handleShopAppAPI(w, r)
		return true
	}
	return false
}

// serveShopAppPage writes the storefront. Its figures are not in the
// markup: the page asks the API below for them, signed, so one copy
// serves every shop's customers.
func (s *Server) serveShopAppPage(w http.ResponseWriter, r *http.Request) {
	if s.shop == nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(shopAppServedPage)
}

// shopAppRequest is one call from the page: who it claims to be
// (initData, checked before anything else), which shop it is talking
// to, and what it wants done, with the fields the action needs
// riding along.
type shopAppRequest struct {
	Op       uint   `json:"op"`
	InitData string `json:"initData"`
	Action   string `json:"action"`

	PlanID uint   `json:"planId"`
	Qty    int    `json:"qty"`
	Code   string `json:"code"`
	Method string `json:"method"`
	Amount int64  `json:"amount"`
}

// writeShopAppError answers a refusal the way the page reads it: a
// JSON body with the sentence to show, in the buyer's language. The
// sentences are written here, never taken from an error's text — an
// error names tables and packages, which are nobody's business on a
// customer's phone.
func writeShopAppError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

// shopAppFail turns a shop failure into the page's answer: a refusal
// the shop named (invalid, not found) is the buyer's 4xx with the
// caller's own sentence for it; anything else is logged and answered
// as the plain "something broke" it is.
func (s *Server) shopAppFail(w http.ResponseWriter, err error, invalidMsg string) {
	switch {
	case errors.Is(err, service.ErrNotFound):
		writeShopAppError(w, http.StatusNotFound, "پیدا نشد.")
	case errors.Is(err, service.ErrInvalid):
		writeShopAppError(w, http.StatusBadRequest, invalidMsg)
	default:
		s.log.Error("shop app call failed", "error", err)
		writeShopAppError(w, http.StatusInternalServerError, "مشکلی پیش آمد؛ دوباره تلاش کنید.")
	}
}

// handleShopAppAPI is every call the page makes. The signature is
// checked first and the shop's own configuration read before that:
// a shop with no bot token cannot check a signature, and answers as
// not there at all, like every switched-off corner of the shop.
func (s *Server) handleShopAppAPI(w http.ResponseWriter, r *http.Request) {
	if s.shop == nil {
		http.NotFound(w, r)
		return
	}
	defer r.Body.Close()
	var req shopAppRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil || req.Op == 0 {
		writeShopAppError(w, http.StatusBadRequest, "درخواست خوانا نیست.")
		return
	}
	ctx := r.Context()
	cfg, err := s.shop.ConfigFor(ctx, req.Op)
	if err != nil {
		s.shopAppFail(w, err, "مشکلی پیش آمد؛ دوباره تلاش کنید.")
		return
	}
	if cfg.Token == "" {
		http.NotFound(w, r)
		return
	}
	tgID, firstName, err := shop.ValidateInitData(cfg.Token, req.InitData, 24*time.Hour, time.Now())
	if err != nil {
		writeShopAppError(w, http.StatusUnauthorized, "ورود تلگرام معتبر نیست؛ فروشگاه را دوباره از داخل بات باز کنید.")
		return
	}
	buyerName := strings.TrimSpace(firstName)
	if buyerName == "" {
		buyerName = fmt.Sprintf("TG-%d", tgID)
	}

	switch req.Action {
	case "state":
		s.shopAppState(w, r, cfg, tgID, firstName)
	case "buy":
		s.shopAppBuy(w, r, cfg, tgID, buyerName, &req)
	case "topup":
		s.shopAppTopup(w, r, cfg, tgID, buyerName, &req)
	case "gift":
		s.shopAppGift(w, r, cfg, tgID, &req)
	case "trial":
		s.shopAppTrial(w, r, cfg, tgID, buyerName)
	case "spin":
		s.shopAppSpin(w, r, cfg, tgID)
	default:
		writeShopAppError(w, http.StatusBadRequest, "این کار را نمی‌شناسم.")
	}
}

// shopAppSub is one of the buyer's subscriptions as the page draws
// it: what it is called, where it stands, and the links it is used
// through — the plain one, and the V2ray bundle link when the
// customer holds that section too.
type shopAppSub struct {
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	UsedBytes  uint64     `json:"usedBytes"`
	QuotaBytes uint64     `json:"quotaBytes"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	Link       string     `json:"link"`
	V2rayLink  string     `json:"v2rayLink"`
}

// shopAppSubscriptions reads the buyer's own customers under the
// operator's scope — nobody else's, because the scope is the
// operator's and the Telegram id is the signed one.
func (s *Server) shopAppSubscriptions(ctx context.Context, cfg *shop.Config, tgID int64) ([]shopAppSub, error) {
	out := []shopAppSub{}
	if s.clients == nil {
		return out, nil
	}
	opCtx, err := s.shop.OperatorCtx(ctx, cfg.OperatorID)
	if err != nil {
		return nil, err
	}
	customers, err := s.clients.ByTelegramID(opCtx, tgID)
	if err != nil {
		return nil, err
	}
	for i := range customers {
		c := &customers[i]
		sub := shopAppSub{
			Name: c.Name, Status: string(c.Status),
			UsedBytes: c.UsedBytes, QuotaBytes: c.QuotaBytes,
			ExpiresAt: c.ExpiresAt,
			Link:      s.shop.SubLink(ctx, c),
		}
		if sub.Link != "" {
			if _, xray, serr := s.shop.ClientSections(ctx, c); serr == nil && xray {
				sub.V2rayLink = sub.Link + "?format=base64"
			}
		}
		out = append(out, sub)
	}
	return out, nil
}

// shopAppPlan is one sellable plan as the page draws it.
type shopAppPlan struct {
	ID         uint   `json:"id"`
	Name       string `json:"name"`
	Section    string `json:"section"`
	QuotaBytes uint64 `json:"quotaBytes"`
	Days       int    `json:"days"`
	PriceToman int64  `json:"priceToman"`
}

// shopAppState is the page's opening call: who the buyer is here,
// what their wallet holds, what they already have, and what the shop
// sells — only the sections that can actually be delivered, as the
// bot's menu counts them.
func (s *Server) shopAppState(w http.ResponseWriter, r *http.Request, cfg *shop.Config, tgID int64, firstName string) {
	ctx := r.Context()
	wallet, err := s.shop.WalletFor(ctx, cfg.OperatorID, tgID)
	if err != nil {
		s.shopAppFail(w, err, "مشکلی پیش آمد؛ دوباره تلاش کنید.")
		return
	}
	subs, err := s.shopAppSubscriptions(ctx, cfg, tgID)
	if err != nil {
		s.shopAppFail(w, err, "مشکلی پیش آمد؛ دوباره تلاش کنید.")
		return
	}
	plans, err := s.shop.PlansFor(ctx, cfg.OperatorID, true)
	if err != nil {
		s.shopAppFail(w, err, "مشکلی پیش آمد؛ دوباره تلاش کنید.")
		return
	}
	type section struct {
		Section string        `json:"section"`
		Plans   []shopAppPlan `json:"plans"`
	}
	sections := []section{}
	for _, name := range []string{shop.SectionWireGuard, shop.SectionV2ray} {
		var own []shopAppPlan
		for i := range plans {
			if shop.NormalizeSection(plans[i].Section) == name {
				own = append(own, shopAppPlan{
					ID: plans[i].ID, Name: plans[i].Name, Section: name,
					QuotaBytes: plans[i].QuotaBytes, Days: plans[i].Days,
					PriceToman: plans[i].PriceToman,
				})
			}
		}
		if len(own) == 0 {
			continue
		}
		ids, err := s.shop.SectionInterfaces(ctx, cfg, name)
		if err != nil {
			s.shopAppFail(w, err, "مشکلی پیش آمد؛ دوباره تلاش کنید.")
			return
		}
		if len(ids) == 0 {
			continue
		}
		sections = append(sections, section{Section: name, Plans: own})
	}
	base, _ := s.shop.PublicBase(ctx)
	writeJSON(w, http.StatusOK, map[string]any{
		"name":            firstName,
		"balance":         wallet.BalanceToman,
		"subscriptions":   subs,
		"sections":        sections,
		"trialAvailable":  cfg.TrialEnabled && wallet.TrialUsedAt == nil,
		"wheelEnabled":    cfg.WheelEnabled,
		"cashbackPercent": cfg.CashbackPercent,
		"tutorial":        cfg.TutorialText,
		"faq":             cfg.FaqText,
		"cardEnabled":     cfg.CardNumber != "",
		"zarinpalEnabled": cfg.ZarinpalMerchant != "" && strings.TrimSpace(base) != "",
	})
}

// shopAppPlanFor reads one of the shop's enabled plans by id at the
// moment of the call: a plan the operator disabled while the page
// sat open is not for sale, as the bot counts it too.
func (s *Server) shopAppPlanFor(ctx context.Context, operatorID, planID uint) *shop.Plan {
	plans, err := s.shop.PlansFor(ctx, operatorID, true)
	if err != nil {
		s.log.Warn("shop app could not read the plans", "error", err)
		return nil
	}
	for i := range plans {
		if plans[i].ID == planID {
			return &plans[i]
		}
	}
	return nil
}

// newShopAppOrder records a purchase the way the bot's checkout
// does: the plan's terms copied onto the order, the basket's
// arithmetic done once — the total for the quantity, less the
// code's discount — so every way of paying charges the number the
// buyer was shown. Kind is "new"; whether it creates a customer or
// renews the buyer's own is Fulfill's call, as it is for the bot.
func (s *Server) newShopAppOrder(ctx context.Context, cfg *shop.Config, tgID int64, buyerName string, plan *shop.Plan, qty int, code string, discount, final int64, method, status string) (*shop.Order, error) {
	o := &shop.Order{
		OperatorID:    cfg.OperatorID,
		BuyerTGID:     tgID,
		BuyerName:     buyerName,
		PlanID:        plan.ID,
		PlanName:      plan.Name,
		Section:       shop.NormalizeSection(plan.Section),
		QuotaBytes:    plan.QuotaBytes,
		Days:          plan.Days,
		PriceToman:    final,
		DeviceLimit:   plan.DeviceLimit,
		Quantity:      qty,
		Code:          code,
		DiscountToman: discount,
		Kind:          shop.KindNew,
		Method:        method,
		Status:        status,
	}
	if err := s.shop.CreateOrder(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
}

// shopAppZarinpal opens a gateway payment for an order standing
// unpaid and answers with its page: the authority is requested
// against the order's public callback, exactly as the bot opens
// one, so the gateway's way back in settles this order the same
// way it settles the bot's.
func (s *Server) shopAppZarinpal(w http.ResponseWriter, r *http.Request, cfg *shop.Config, o *shop.Order, description string) bool {
	ctx := r.Context()
	base, err := s.shop.PublicBase(ctx)
	if err != nil || strings.TrimSpace(base) == "" || o.PublicToken == "" {
		writeShopAppError(w, http.StatusBadRequest, "پرداخت آنلاین فعال نیست.")
		return false
	}
	callback := strings.TrimRight(base, "/") + "/shop/callback/" + o.PublicToken
	authority, err := shopbot.RequestPayment(ctx, cfg.ZarinpalMerchant, o.PriceToman, description, callback)
	if err != nil {
		s.log.Warn("shop app could not open the payment", "order", o.ID, "error", err)
		writeShopAppError(w, http.StatusBadGateway, "درگاه پرداخت پاسخ نداد؛ دوباره تلاش کنید.")
		return false
	}
	o.Authority = authority
	if err := s.shop.UpdateOrder(ctx, o); err != nil {
		s.shopAppFail(w, err, "مشکلی پیش آمد؛ دوباره تلاش کنید.")
		return false
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"orderID": o.ID,
		"payURL":  shopbot.PayURL(authority),
	})
	return true
}

// shopAppBuy is a purchase: price the basket (quantity, then the
// code against the total), write the order down, and set it on the
// road its method travels — a card order waits for its receipt, a
// gateway order for its callback, and a wallet order is paid and
// delivered on the spot, with the bot's own manners for a delivery
// that fails after the debit: the money goes back.
func (s *Server) shopAppBuy(w http.ResponseWriter, r *http.Request, cfg *shop.Config, tgID int64, buyerName string, req *shopAppRequest) {
	ctx := r.Context()
	plan := s.shopAppPlanFor(ctx, cfg.OperatorID, req.PlanID)
	if plan == nil {
		writeShopAppError(w, http.StatusBadRequest, "این پلن دیگر برای فروش فعال نیست.")
		return
	}
	qty := req.Qty
	if qty < 1 {
		qty = 1
	}
	if qty > 5 {
		writeShopAppError(w, http.StatusBadRequest, "در هر خرید حداکثر ۵ عدد می‌توانید بخرید.")
		return
	}
	total := plan.PriceToman * int64(qty)
	final, discount := total, int64(0)
	code := strings.TrimSpace(req.Code)
	if code != "" {
		var err error
		final, discount, err = s.shop.PriceWithCode(ctx, cfg.OperatorID, tgID, code, total)
		if err != nil {
			writeShopAppError(w, http.StatusBadRequest, "این کد تخفیف قابل استفاده نیست؛ یا اشتباه است، یا منقضی شده، یا قبلاً از آن استفاده کرده‌اید.")
			return
		}
	} else {
		code = ""
	}

	switch req.Method {
	case shop.MethodCard:
		if cfg.CardNumber == "" {
			writeShopAppError(w, http.StatusBadRequest, "پرداخت کارتی در این فروشگاه فعال نیست.")
			return
		}
		o, err := s.newShopAppOrder(ctx, cfg, tgID, buyerName, plan, qty, code, discount, final, shop.MethodCard, shop.StatusAwaitingReceipt)
		if err != nil {
			s.shopAppFail(w, err, "ساختن سفارش انجام نشد.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"orderID":    o.ID,
			"cardNumber": cfg.CardNumber,
			"cardHolder": cfg.CardHolder,
			"amount":     final,
		})
	case shop.MethodZarinpal:
		if cfg.ZarinpalMerchant == "" {
			writeShopAppError(w, http.StatusBadRequest, "پرداخت آنلاین در این فروشگاه فعال نیست.")
			return
		}
		o, err := s.newShopAppOrder(ctx, cfg, tgID, buyerName, plan, qty, code, discount, final, shop.MethodZarinpal, shop.StatusUnpaid)
		if err != nil {
			s.shopAppFail(w, err, "ساختن سفارش انجام نشد.")
			return
		}
		s.shopAppZarinpal(w, r, cfg, o, "خرید اشتراک "+plan.Name)
	case shop.MethodWallet:
		o, err := s.newShopAppOrder(ctx, cfg, tgID, buyerName, plan, qty, code, discount, final, shop.MethodWallet, shop.StatusUnpaid)
		if err != nil {
			s.shopAppFail(w, err, "ساختن سفارش انجام نشد.")
			return
		}
		debited := false
		if final > 0 {
			if err := s.shop.DebitWallet(ctx, cfg.OperatorID, tgID, final); err != nil {
				o.Status = shop.StatusExpired
				if uerr := s.shop.UpdateOrder(ctx, o); uerr != nil {
					s.log.Warn("shop app could not close the unpaid wallet order", "order", o.ID, "error", uerr)
				}
				writeShopAppError(w, http.StatusBadRequest, "موجودی کیف پول کافی نیست.")
				return
			}
			debited = true
		}
		o.Status = shop.StatusPaid
		if err := s.shop.UpdateOrder(ctx, o); err != nil {
			// The wallet is already debited and the order still says
			// unpaid: give the money back rather than leaving a charge
			// with nothing delivered, as the failed-delivery path below
			// does.
			if debited {
				if rerr := s.shop.CreditWallet(ctx, cfg.OperatorID, tgID, final); rerr != nil {
					s.log.Error("shop app could not refund a wallet order whose payment could not be recorded", "order", o.ID, "error", rerr)
				}
			}
			s.shopAppFail(w, err, "مشکلی پیش آمد؛ دوباره تلاش کنید.")
			return
		}
		if _, err := s.shop.Fulfill(ctx, o.ID); err != nil {
			s.log.Warn("shop app could not deliver a wallet order", "order", o.ID, "error", err)
			if debited {
				if rerr := s.shop.CreditWallet(ctx, cfg.OperatorID, tgID, final); rerr != nil {
					s.log.Error("shop app could not refund a failed wallet delivery", "order", o.ID, "error", rerr)
				}
			}
			if fresh, ferr := s.shop.OrderByID(ctx, o.ID); ferr == nil {
				fresh.Status = shop.StatusFailed
				_ = s.shop.UpdateOrder(ctx, fresh)
			}
			writeShopAppError(w, http.StatusBadRequest, "تحویل اشتراک انجام نشد؛ مبلغ به کیف پولتان برگشت داده شد.")
			return
		}
		subs, err := s.shopAppSubscriptions(ctx, cfg, tgID)
		if err != nil {
			// Charged and delivered already: the list is only for
			// display, and failing the purchase now would send the buyer
			// back to buy — and be charged — a second time.
			s.log.Warn("shop app could not list a delivered wallet order's subscriptions", "order", o.ID, "error", err)
			subs = []shopAppSub{}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"orderID":       o.ID,
			"delivered":     true,
			"subscriptions": subs,
		})
	default:
		writeShopAppError(w, http.StatusBadRequest, "روش پرداخت را انتخاب کنید.")
	}
}

// shopAppTopup is a wallet top-up: an order of the topup kind riding
// the same two roads a purchase does — the seller's approval for a
// card, the gateway's callback for an online payment — and delivery,
// the shop's Fulfill, turns it into balance. There is no wallet
// method: a wallet cannot charge itself.
func (s *Server) shopAppTopup(w http.ResponseWriter, r *http.Request, cfg *shop.Config, tgID int64, buyerName string, req *shopAppRequest) {
	ctx := r.Context()
	if req.Amount <= 0 {
		writeShopAppError(w, http.StatusBadRequest, "مبلغ شارژ درست نیست.")
		return
	}
	newTopup := func(method, status string) (*shop.Order, bool) {
		o := &shop.Order{
			OperatorID: cfg.OperatorID,
			BuyerTGID:  tgID,
			BuyerName:  buyerName,
			PlanName:   "شارژ کیف پول",
			PriceToman: req.Amount,
			Kind:       shop.KindTopup,
			Method:     method,
			Status:     status,
		}
		if err := s.shop.CreateOrder(ctx, o); err != nil {
			s.shopAppFail(w, err, "ساختن سفارش انجام نشد.")
			return nil, false
		}
		return o, true
	}
	switch req.Method {
	case shop.MethodCard:
		if cfg.CardNumber == "" {
			writeShopAppError(w, http.StatusBadRequest, "پرداخت کارتی در این فروشگاه فعال نیست.")
			return
		}
		o, ok := newTopup(shop.MethodCard, shop.StatusAwaitingReceipt)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"orderID":    o.ID,
			"cardNumber": cfg.CardNumber,
			"cardHolder": cfg.CardHolder,
			"amount":     req.Amount,
		})
	case shop.MethodZarinpal:
		if cfg.ZarinpalMerchant == "" {
			writeShopAppError(w, http.StatusBadRequest, "پرداخت آنلاین در این فروشگاه فعال نیست.")
			return
		}
		o, ok := newTopup(shop.MethodZarinpal, shop.StatusUnpaid)
		if !ok {
			return
		}
		s.shopAppZarinpal(w, r, cfg, o, "شارژ کیف پول")
	default:
		writeShopAppError(w, http.StatusBadRequest, "شارژ کیف پول فقط با کارت یا پرداخت آنلاین ممکن است.")
	}
}

// shopAppGift redeems a gift code into the buyer's wallet and says
// what arrived and what the wallet now holds.
func (s *Server) shopAppGift(w http.ResponseWriter, r *http.Request, cfg *shop.Config, tgID int64, req *shopAppRequest) {
	ctx := r.Context()
	credited, err := s.shop.RedeemGift(ctx, cfg.OperatorID, tgID, strings.TrimSpace(req.Code))
	if err != nil {
		writeShopAppError(w, http.StatusBadRequest, "این کد هدیه معتبر نیست؛ یا اشتباه است، یا منقضی شده، یا قبلاً ثبتش کرده‌اید.")
		return
	}
	wallet, err := s.shop.WalletFor(ctx, cfg.OperatorID, tgID)
	if err != nil {
		s.shopAppFail(w, err, "مشکلی پیش آمد؛ دوباره تلاش کنید.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"credited": credited,
		"balance":  wallet.BalanceToman,
	})
}

// shopAppTrial claims the shop's free trial for this buyer — once
// per buyer per shop, ever, as the shop fences it — and hands back
// the link of the customer it made.
func (s *Server) shopAppTrial(w http.ResponseWriter, r *http.Request, cfg *shop.Config, tgID int64, buyerName string) {
	ctx := r.Context()
	client, err := s.shop.ClaimTrial(ctx, cfg.OperatorID, tgID, buyerName)
	if err != nil {
		if wallet, werr := s.shop.WalletFor(ctx, cfg.OperatorID, tgID); werr == nil && wallet.TrialUsedAt != nil {
			writeShopAppError(w, http.StatusBadRequest, "شما قبلاً اکانت تست این فروشگاه را گرفته‌اید؛ اکانت تست برای هر نفر فقط یک بار است.")
			return
		}
		if !cfg.TrialEnabled {
			writeShopAppError(w, http.StatusBadRequest, "این فروشگاه اکانت تست ارائه نمی‌دهد.")
			return
		}
		s.shopAppFail(w, err, "گرفتن اکانت تست الان ممکن نیست.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"link": s.shop.SubLink(ctx, client),
	})
}

// shopAppSpin turns the shop's wheel once for this buyer and hands
// the prize back as data for the page to celebrate. A spin inside
// the day's fence is a refusal with its own sentence, not a failure.
func (s *Server) shopAppSpin(w http.ResponseWriter, r *http.Request, cfg *shop.Config, tgID int64) {
	prize, err := s.shop.SpinWheel(r.Context(), cfg.OperatorID, tgID)
	if err != nil {
		if errors.Is(err, shop.ErrWheelTooSoon) {
			writeShopAppError(w, http.StatusBadRequest, "امروز گردونه را چرخانده‌اید؛ فردا دوباره تلاش کنید.")
			return
		}
		s.shopAppFail(w, err, "گردونهٔ شانس این فروشگاه فعال نیست.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"kind":    prize.Kind,
		"percent": prize.Percent,
		"code":    prize.Code,
		"bytes":   prize.Bytes,
	})
}
