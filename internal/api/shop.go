package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/service"
	"github.com/abolfazl/w-ui/internal/shop"
)

// The «بات فروش» page and the payment gateway's way back in.
//
// Every operator may run a sales bot of their own, so everything here is
// scoped to the caller: an operator reads and writes their own bot, their
// own plans and their own orders, and an order of somebody else's is
// answered as not there at all -- the panel does not confirm to one
// seller that another seller's sale exists.
//
// The bot token is the one secret this page handles, and it is handled
// the way the panel handles every secret: whether one is set is
// reported, the value never is. A page that echoed it back would put it
// in browser history, in screenshots and in support conversations,
// where it has no business being.

// shopOperator is the caller these handlers serve: the shop itself, when
// this panel was built with one, and the signed-in operator. A panel
// without the shop answers 404 -- the feature is not off-with-an-error,
// it is not there -- and a request without an operator is refused the
// way the panel's other operator pages refuse it.
func (s *Server) shopOperator(w http.ResponseWriter, r *http.Request) (*model.Admin, bool) {
	if s.shop == nil {
		writeError(w, http.StatusNotFound, "this panel has no sales bot")
		return nil, false
	}
	op := adminFrom(r.Context())
	if op == nil {
		writeError(w, http.StatusForbidden, "this needs a signed-in administrator, not an API token")
		return nil, false
	}
	return op, true
}

// shopConfigView is one operator's bot as their page reads it. The token
// is present only as tokenSet, for the reason at the top of this file.
type shopConfigView struct {
	Enabled       bool   `json:"enabled"`
	TokenSet      bool   `json:"tokenSet"`
	AdminChatID   int64  `json:"adminChatId"`
	CardNumber    string `json:"cardNumber"`
	CardHolder    string `json:"cardHolder"`
	ZarinpalMerch string `json:"zarinpalMerchant"`
	Welcome       string `json:"welcome"`
	InterfaceIDs  []uint `json:"interfaceIds"`
	BotUsername   string `json:"botUsername"`
	BotRunning    bool   `json:"botRunning"`
	// The shop's growth settings ride along so the page can show and
	// save them with the rest of the bot's configuration: the trial it
	// offers, how its customers are named, the cashback it pays, and
	// the wheel's switch.
	TrialEnabled    bool   `json:"trialEnabled"`
	TrialBytes      uint64 `json:"trialBytes"`
	TrialDays       int    `json:"trialDays"`
	TrialSection    string `json:"trialSection"`
	NameFormat      string `json:"nameFormat"`
	CashbackPercent int    `json:"cashbackPercent"`
	WheelEnabled    bool   `json:"wheelEnabled"`
	// PublicBase is where subscription links live, panel-wide rather
	// than per operator: the bots are each seller's own, but there is
	// one subscription listener for all of them. Only the owner may move
	// it, which PublicBaseEditable says so the page can draw the field
	// read-only instead of letting a reseller type into a box that will
	// refuse them.
	PublicBase         string `json:"publicBase"`
	PublicBaseEditable bool   `json:"publicBaseEditable"`
}

// shopConfig reads the caller's bot and writes its view. Shared by the
// read and the save, so the page is answered after a save with exactly
// what a fresh read would say.
func (s *Server) shopConfig(w http.ResponseWriter, r *http.Request, op *model.Admin) {
	cfg, err := s.shop.ConfigFor(r.Context(), op.ID)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	base, err := s.shop.PublicBase(r.Context())
	if err != nil {
		fail(w, s.log, err)
		return
	}
	view := shopConfigView{
		Enabled:            cfg.Enabled,
		TokenSet:           cfg.Token != "",
		AdminChatID:        cfg.AdminChatID,
		CardNumber:         cfg.CardNumber,
		CardHolder:         cfg.CardHolder,
		ZarinpalMerch:      cfg.ZarinpalMerchant,
		Welcome:            cfg.Welcome,
		InterfaceIDs:       cfg.InterfaceIDs,
		TrialEnabled:       cfg.TrialEnabled,
		TrialBytes:         cfg.TrialBytes,
		TrialDays:          cfg.TrialDays,
		TrialSection:       cfg.TrialSection,
		NameFormat:         cfg.NameFormat,
		CashbackPercent:    cfg.CashbackPercent,
		WheelEnabled:       cfg.WheelEnabled,
		PublicBase:         base,
		PublicBaseEditable: op.Role == model.RoleOwner,
	}
	if view.InterfaceIDs == nil {
		view.InterfaceIDs = []uint{}
	}
	if s.shopBot != nil {
		view.BotUsername, view.BotRunning = s.shopBot.BotInfo(op.ID)
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleShopConfig(w http.ResponseWriter, r *http.Request) {
	op, ok := s.shopOperator(w, r)
	if !ok {
		return
	}
	s.shopConfig(w, r, op)
}

// shopConfigBody is what the page sends back. The token and the public
// base are pointers to absence, not emptiness: both are fields the page
// may legitimately leave alone, and "not mentioned" has to be tellable
// from a value.
type shopConfigBody struct {
	Enabled       bool    `json:"enabled"`
	Token         *string `json:"token"`
	AdminChatID   int64   `json:"adminChatId"`
	CardNumber    string  `json:"cardNumber"`
	CardHolder    string  `json:"cardHolder"`
	ZarinpalMerch string  `json:"zarinpalMerchant"`
	Welcome       string  `json:"welcome"`
	InterfaceIDs  []uint  `json:"interfaceIds"`
	PublicBase    *string `json:"publicBase"`

	TrialEnabled    bool   `json:"trialEnabled"`
	TrialBytes      uint64 `json:"trialBytes"`
	TrialDays       int    `json:"trialDays"`
	TrialSection    string `json:"trialSection"`
	NameFormat      string `json:"nameFormat"`
	CashbackPercent int    `json:"cashbackPercent"`
	WheelEnabled    bool   `json:"wheelEnabled"`
}

func (s *Server) handleShopConfigSave(w http.ResponseWriter, r *http.Request) {
	op, ok := s.shopOperator(w, r)
	if !ok {
		return
	}
	var body shopConfigBody
	if !decode(w, r, &body) {
		return
	}
	ctx := r.Context()

	// The public base is checked before anything is written: a reseller
	// asking to move the whole panel's links is refused with nothing
	// saved, not with their own bot saved and the refusal riding along.
	if body.PublicBase != nil {
		current, err := s.shop.PublicBase(ctx)
		if err != nil {
			fail(w, s.log, err)
			return
		}
		if *body.PublicBase != current {
			if op.Role != model.RoleOwner {
				writeError(w, http.StatusForbidden, "only the panel's owner can change where subscription links live")
				return
			}
			if err := s.shop.SetPublicBase(ctx, *body.PublicBase); err != nil {
				fail(w, s.log, err)
				return
			}
		}
	}

	cfg, err := s.shop.ConfigFor(ctx, op.ID)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	cfg.Enabled = body.Enabled
	cfg.AdminChatID = body.AdminChatID
	cfg.CardNumber = body.CardNumber
	cfg.CardHolder = body.CardHolder
	cfg.ZarinpalMerchant = body.ZarinpalMerch
	cfg.Welcome = body.Welcome
	cfg.InterfaceIDs = body.InterfaceIDs
	cfg.TrialEnabled = body.TrialEnabled
	cfg.TrialBytes = body.TrialBytes
	cfg.TrialDays = body.TrialDays
	cfg.TrialSection = body.TrialSection
	cfg.NameFormat = body.NameFormat
	cfg.CashbackPercent = body.CashbackPercent
	cfg.WheelEnabled = body.WheelEnabled
	// An absent or empty token keeps the one on file: the page never
	// holds the value (it is only ever told one is set), so what it
	// sends back on an ordinary save is "no change", and treating that
	// as "clear it" would switch every bot off at its next save.
	if body.Token != nil && *body.Token != "" {
		cfg.Token = *body.Token
	}
	if err := s.shop.SaveConfig(ctx, cfg); err != nil {
		fail(w, s.log, err)
		return
	}
	// The manager is told after the save, so the bot it (re)starts runs
	// on what was written rather than on what was asked for.
	if s.shopBot != nil {
		s.shopBot.Reload(op.ID)
	}
	s.shopConfig(w, r, op)
}

// shopPlanView is one plan as the page works with it.
type shopPlanView struct {
	ID          uint   `json:"id"`
	OperatorID  uint   `json:"operatorId"`
	Name        string `json:"name"`
	Section     string `json:"section"`
	QuotaBytes  uint64 `json:"quotaBytes"`
	Days        int    `json:"days"`
	PriceToman  int64  `json:"priceToman"`
	DeviceLimit int    `json:"deviceLimit"`
	Enabled     bool   `json:"enabled"`
	Sort        int    `json:"sort"`
}

func planView(p shop.Plan) shopPlanView {
	return shopPlanView{
		ID: p.ID, OperatorID: p.OperatorID, Name: p.Name,
		Section:    shop.NormalizeSection(p.Section),
		QuotaBytes: p.QuotaBytes, Days: p.Days, PriceToman: p.PriceToman,
		DeviceLimit: p.DeviceLimit, Enabled: p.Enabled, Sort: p.Sort,
	}
}

func (s *Server) handleShopPlans(w http.ResponseWriter, r *http.Request) {
	op, ok := s.shopOperator(w, r)
	if !ok {
		return
	}
	plans, err := s.shop.PlansFor(r.Context(), op.ID, false)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	items := make([]shopPlanView, 0, len(plans))
	for _, p := range plans {
		items = append(items, planView(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleShopPlanSave writes one of the caller's plans: a new one when
// the body carries no id, an edit when it does. Whose plan it is comes
// from the caller, never from the body -- a body that named another
// operator would otherwise be a way to write into their shop, and the
// store refuses cross-operator edits besides.
func (s *Server) handleShopPlanSave(w http.ResponseWriter, r *http.Request) {
	op, ok := s.shopOperator(w, r)
	if !ok {
		return
	}
	var body shopPlanView
	if !decode(w, r, &body) {
		return
	}
	plan := shop.Plan{
		ID: body.ID, OperatorID: op.ID, Name: body.Name, Section: body.Section,
		QuotaBytes: body.QuotaBytes, Days: body.Days, PriceToman: body.PriceToman,
		DeviceLimit: body.DeviceLimit, Enabled: body.Enabled, Sort: body.Sort,
	}
	if err := s.shop.SavePlan(r.Context(), &plan); err != nil {
		fail(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, planView(plan))
}

func (s *Server) handleShopPlanDelete(w http.ResponseWriter, r *http.Request) {
	op, ok := s.shopOperator(w, r)
	if !ok {
		return
	}
	id, okID := pathID(w, r)
	if !okID {
		return
	}
	if err := s.shop.DeletePlan(r.Context(), op.ID, id); err != nil {
		fail(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// shopOrderView is one order as its operator's page reads it. The public
// token is left out on purpose: it is the key to the order's one public
// link, and the page has no use for it.
type shopOrderView struct {
	ID            uint   `json:"id"`
	OperatorID    uint   `json:"operatorId"`
	BuyerTGID     int64  `json:"buyerTgId"`
	BuyerName     string `json:"buyerName"`
	PlanID        uint   `json:"planId"`
	PlanName      string `json:"planName"`
	Section       string `json:"section"`
	QuotaBytes    uint64 `json:"quotaBytes"`
	Days          int    `json:"days"`
	PriceToman    int64  `json:"priceToman"`
	DeviceLimit   int    `json:"deviceLimit"`
	Kind          string `json:"kind"`
	Method        string `json:"method"`
	Status        string `json:"status"`
	ReceiptFileID string `json:"receiptFileId"`
	// HasReceipt says the receipt picture itself is kept in the panel,
	// at the receipt endpoint -- the file id above only names it on
	// Telegram, where this page cannot show it from.
	HasReceipt bool      `json:"hasReceipt"`
	Authority  string    `json:"authority"`
	RefID      string    `json:"refId"`
	ClientID   uint      `json:"clientId"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func orderView(o *shop.Order) shopOrderView {
	return shopOrderView{
		ID: o.ID, OperatorID: o.OperatorID,
		BuyerTGID: o.BuyerTGID, BuyerName: o.BuyerName,
		PlanID: o.PlanID, PlanName: o.PlanName, Section: shop.NormalizeSection(o.Section),
		QuotaBytes: o.QuotaBytes,
		Days:       o.Days, PriceToman: o.PriceToman, DeviceLimit: o.DeviceLimit,
		Kind: o.Kind, Method: o.Method, Status: o.Status,
		ReceiptFileID: o.ReceiptFileID, HasReceipt: o.HasReceipt,
		Authority: o.Authority, RefID: o.RefID,
		ClientID: o.ClientID, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}
}

func (s *Server) handleShopOrders(w http.ResponseWriter, r *http.Request) {
	op, ok := s.shopOperator(w, r)
	if !ok {
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	orders, err := s.shop.OrdersFor(r.Context(), op.ID, r.URL.Query().Get("status"), limit)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	items := make([]shopOrderView, 0, len(orders))
	for i := range orders {
		items = append(items, orderView(&orders[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// ownOrder loads the order in the path for the caller, and answers for
// it. An order of another operator's is a 404 rather than a refusal:
// there is nothing here that is theirs to be refused.
func (s *Server) ownOrder(w http.ResponseWriter, r *http.Request, op *model.Admin) (*shop.Order, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return nil, false
	}
	order, err := s.shop.OrderByID(r.Context(), id)
	if err != nil {
		fail(w, s.log, err)
		return nil, false
	}
	if order.OperatorID != op.ID {
		writeError(w, http.StatusNotFound, "no such order")
		return nil, false
	}
	return order, true
}

// handleShopOrderApprove delivers an order the operator has accepted:
// the customer is made (or renewed) by the shop, and the answer carries
// the order as it now stands and the link the buyer can be given again
// from here.
func (s *Server) handleShopOrderApprove(w http.ResponseWriter, r *http.Request) {
	op, ok := s.shopOperator(w, r)
	if !ok {
		return
	}
	order, ok := s.ownOrder(w, r, op)
	if !ok {
		return
	}
	client, err := s.shop.Fulfill(r.Context(), order.ID)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	done, err := s.shop.OrderByID(r.Context(), order.ID)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"order":    orderView(done),
		"clientId": client.ID,
		"subLink":  s.shop.SubLink(r.Context(), client),
	})
}

func (s *Server) handleShopOrderReject(w http.ResponseWriter, r *http.Request) {
	op, ok := s.shopOperator(w, r)
	if !ok {
		return
	}
	order, ok := s.ownOrder(w, r, op)
	if !ok {
		return
	}
	if err := s.shop.Reject(r.Context(), order.ID); err != nil {
		fail(w, s.log, err)
		return
	}
	done, err := s.shop.OrderByID(r.Context(), order.ID)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, orderView(done))
}

func (s *Server) handleShopStats(w http.ResponseWriter, r *http.Request) {
	op, ok := s.shopOperator(w, r)
	if !ok {
		return
	}
	st, err := s.shop.StatsFor(r.Context(), op.ID)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"todaySales":   st.TodaySales,
		"totalSales":   st.TotalSales,
		"revenueToman": st.RevenueToman,
		"pending":      st.Pending,
		"buyers":       st.Buyers,
	})
}

// shopBuyerView is one buyer as the caller's page lists them: the
// customer they are, and the business they have done with this seller.
type shopBuyerView struct {
	ClientID    uint       `json:"clientId"`
	Name        string     `json:"name"`
	TelegramID  int64      `json:"telegramId"`
	Status      string     `json:"status"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	UsedBytes   uint64     `json:"usedBytes"`
	QuotaBytes  uint64     `json:"quotaBytes"`
	Orders      int        `json:"orders"`
	LastOrderAt time.Time  `json:"lastOrderAt"`
}

// handleShopBuyers lists the caller's buyers -- nobody else's, because
// the store builds the list out of the caller's own delivered orders
// under the caller's own scope.
func (s *Server) handleShopBuyers(w http.ResponseWriter, r *http.Request) {
	op, ok := s.shopOperator(w, r)
	if !ok {
		return
	}
	buyers, err := s.shop.BuyersFor(r.Context(), op.ID)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	items := make([]shopBuyerView, 0, len(buyers))
	for _, b := range buyers {
		items = append(items, shopBuyerView{
			ClientID: b.ClientID, Name: b.Name, TelegramID: b.TelegramID,
			Status: b.Status, ExpiresAt: b.ExpiresAt,
			UsedBytes: b.UsedBytes, QuotaBytes: b.QuotaBytes,
			Orders: b.Orders, LastOrderAt: b.LastOrderAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// shopAnnounceBody is an announcement the operator sends their buyers
// through their own bot: a text, and one buyer to send it to -- absent
// (zero, which is nobody's Telegram id) means all of them.
type shopAnnounceBody struct {
	BuyerTGID int64  `json:"buyerTgId"`
	Text      string `json:"text"`
}

// maxAnnounceChars is the longest announcement: a notice is a sentence
// or three, and a bot that can be made to send essays is a bot that
// will be reported for them.
const maxAnnounceChars = 2000

func (s *Server) handleShopAnnounce(w http.ResponseWriter, r *http.Request) {
	op, ok := s.shopOperator(w, r)
	if !ok {
		return
	}
	// The announcement travels by the bot; a panel built without the
	// bot manager has no way to send one, which is the gateway
	// callback's answer too: not refused, not there.
	if s.shopBot == nil {
		writeError(w, http.StatusNotFound, "this panel has no sales bot")
		return
	}
	var body shopAnnounceBody
	if !decode(w, r, &body) {
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "the announcement text is empty")
		return
	}
	if len([]rune(text)) > maxAnnounceChars {
		writeError(w, http.StatusBadRequest, "an announcement is at most 2000 characters")
		return
	}
	sent, err := s.shopBot.SendAnnouncement(r.Context(), op.ID, body.BuyerTGID, text)
	if err != nil {
		if errors.Is(err, service.ErrInvalid) || errors.Is(err, service.ErrNotFound) {
			fail(w, s.log, err)
			return
		}
		// A refusal with a reason, not a fault: this operator's bot is
		// not running, or Telegram would not take the message, and the
		// sentence is the operator's to act on -- the node-pin handler
		// answers its refusals the same way.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": sent})
}

// handleShopOrderReceipt serves the receipt picture kept for one of the
// caller's orders, so it can be looked at in the panel before the order
// is approved. Another operator's order is the approve handlers' 404,
// and an order with no receipt kept is a 404 of its own: in neither
// case is there a picture here for this caller.
func (s *Server) handleShopOrderReceipt(w http.ResponseWriter, r *http.Request) {
	op, ok := s.shopOperator(w, r)
	if !ok {
		return
	}
	order, ok := s.ownOrder(w, r, op)
	if !ok {
		return
	}
	contentType, data, err := s.shop.ReceiptFor(r.Context(), order.ID)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	// The picture is a buyer's proof of payment, shown to its seller
	// alone: private, and never cached on the way.
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// The gateway's way back in.
//
// handleShopCallback is bound on the subscription listener, not the
// panel's: it is a public page a buyer's browser lands on, named only by
// the order's public token. It is therefore HTML, not JSON, and carries
// no script at all -- the subscription listener's content policy allows
// inline style and nothing more, and a page that only has to say one
// sentence needs nothing more.

const shopPageOK = `<!DOCTYPE html>
<html lang="fa" dir="rtl">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>پرداخت موفق</title>
<style>
body{font-family:Tahoma,sans-serif;background:#101418;color:#e8ecef;display:flex;min-height:100vh;margin:0;align-items:center;justify-content:center}
main{background:#1a2129;border-radius:16px;padding:32px 28px;max-width:420px;margin:16px;text-align:center;line-height:2}
</style>
</head>
<body><main><p>پرداخت موفق بود ✅ اشتراک شما در تلگرام تحویل داده شد، به بات برگردید.</p></main></body>
</html>`

const shopPageFailed = `<!DOCTYPE html>
<html lang="fa" dir="rtl">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>پرداخت ناموفق</title>
<style>
body{font-family:Tahoma,sans-serif;background:#101418;color:#e8ecef;display:flex;min-height:100vh;margin:0;align-items:center;justify-content:center}
main{background:#1a2129;border-radius:16px;padding:32px 28px;max-width:420px;margin:16px;text-align:center;line-height:2}
</style>
</head>
<body><main><p>پرداخت تأیید نشد. اگر مبلغی از حساب شما کم شده و اشتراکی تحویل نگرفتید، به پشتیبانی پیام بدهید.</p></main></body>
</html>`

// shopPagePaidUndelivered is the third answer, and it is worded as its
// own case because it is one: the money arrived and the delivery did
// not, so the buyer must be told to ask for help, never that their
// payment failed.
const shopPagePaidUndelivered = `<!DOCTYPE html>
<html lang="fa" dir="rtl">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>پرداخت موفق، تحویل ناموفق</title>
<style>
body{font-family:Tahoma,sans-serif;background:#101418;color:#e8ecef;display:flex;min-height:100vh;margin:0;align-items:center;justify-content:center}
main{background:#1a2129;border-radius:16px;padding:32px 28px;max-width:420px;margin:16px;text-align:center;line-height:2}
</style>
</head>
<body><main><p>پرداخت شما انجام شد ✅ اما تحویل اشتراک به مشکل خورد. لطفاً به پشتیبانی پیام بدهید تا اشتراکتان را تحویل بگیرید.</p></main></body>
</html>`

func writeShopPage(w http.ResponseWriter, page string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(page))
}

func (s *Server) handleShopCallback(w http.ResponseWriter, r *http.Request) {
	if s.shop == nil || s.shopBot == nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	order, err := s.shop.OrderByToken(ctx, r.PathValue("token"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// The gateway says how the buyer left it: anything but its OK is a
	// payment that did not complete, whatever the reason.
	if r.URL.Query().Get("Status") != "OK" {
		writeShopPage(w, shopPageFailed)
		return
	}
	// The authority the gateway returned is the one this order is
	// verified by; it arrives on the callback because the order was
	// written before the buyer was sent away, and a gateway that names
	// a different one is not confirming this order.
	if auth := r.URL.Query().Get("Authority"); auth != "" {
		order.Authority = auth
	}
	paid, err := s.shopBot.VerifyZarinpal(ctx, order)
	if err != nil || !paid {
		if err != nil {
			s.log.Error("zarinpal verify", "order", order.ID, "error", err)
		}
		writeShopPage(w, shopPageFailed)
		return
	}
	if _, err := s.shop.Fulfill(ctx, order.ID); err != nil {
		// The money is in and the customer is not made. The order stays
		// paid, where its operator can deliver it from the panel, and
		// the buyer is told to ask -- not that the payment failed.
		s.log.Error("zarinpal delivery", "order", order.ID, "error", err)
		writeShopPage(w, shopPagePaidUndelivered)
		return
	}
	writeShopPage(w, shopPageOK)
}

// serveShopCallback is the subscription router's half of the gateway's
// way back in: it recognises /shop/callback/<token> on either listener
// and hands the request to handleShopCallback with the token where a mux
// would have put it. The router is not a mux -- the subscription path is
// configurable, so it is checked per request -- which is why the token
// is lifted out of the path here rather than declared as a pattern.
func (s *Server) serveShopCallback(w http.ResponseWriter, r *http.Request) bool {
	const prefix = "/shop/callback/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return false
	}
	token := strings.TrimPrefix(r.URL.Path, prefix)
	if token == "" || strings.Contains(token, "/") {
		return false
	}
	r.SetPathValue("token", token)
	s.handleShopCallback(w, r)
	return true
}
