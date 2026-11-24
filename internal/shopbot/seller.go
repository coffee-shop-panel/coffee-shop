package shopbot

// The seller panel: the seller runs their shop from their own chat.
//
// Until this panel existed, the seller's chat answered a greeting and
// then only ever carried receipts and support relays — the seller had
// no way to see their own shop from the bot that sells for them, which
// read as a bot that shows its owner nothing. Now the chat is a small
// Mirza-style panel: the orders waiting on the seller, their buyers,
// their plans, their numbers, and an announcement box, every screen
// one tap deep with its way back. Nothing here reaches past the
// seller's own scope: orders and buyers come from the operator's own
// rows, and the panel's buttons exist only in the seller's chat —
// the gate below refuses them anywhere else.

import (
	"context"
	"fmt"
	"strings"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/shop"
)

// sellerPanel renders the panel's home: the greeting with the day's
// two numbers, over the menu. The orders button carries its waiting
// count when there is one — the seller should see work waiting before
// they tap anything.
func (b *bot) sellerPanel(ctx context.Context) (string, tgKeyboard) {
	text := txtSellerHomePlain
	ordersBtn := btnSpOrders
	if st, err := b.mgr.shop.StatsFor(ctx, b.cfg().OperatorID); err == nil {
		text = fmt.Sprintf(txtSellerHome, st.TodaySales, st.Pending)
		if st.Pending > 0 {
			ordersBtn = fmt.Sprintf("%s (%s)", btnSpOrders, faNum(int64(st.Pending)))
		}
	}
	return text, tgKeyboard{
		{{Text: ordersBtn, Data: "sp:orders"}},
		{{Text: btnSpBuyers, Data: "sp:buyers"}, {Text: btnSpStats, Data: "sp:stats"}},
		{{Text: btnSpPlans, Data: "sp:plans"}, {Text: btnSpCast, Data: "sp:cast"}},
	}
}

func panelBackKeyboard() tgKeyboard {
	return tgKeyboard{{{Text: btnSpPanel, Data: "sp:home"}}}
}

// sellerPanelCallback routes the panel's buttons. The gate is the
// whole security of the panel: these buttons are served only in the
// seller's chat, and a callback claiming them from anywhere else — a
// forwarded message, a curious buyer — is refused before it can read
// a single order.
func (b *bot) sellerPanelCallback(ctx context.Context, cb *tgCallback, sess *session, data string) {
	cfg := b.cfg()
	chatID := cb.From.ID
	if cb.Message != nil {
		chatID = cb.Message.Chat.ID
	}
	if cfg.AdminChatID == 0 || chatID != cfg.AdminChatID {
		_ = b.api.answerCallbackQuery(ctx, cb.ID, txtNotAllowed, true)
		return
	}
	switch {
	case data == "sp:home":
		b.answer(ctx, cb, "")
		sess.reset()
		text, kb := b.sellerPanel(ctx)
		b.editOrSend(ctx, cb, text, kb)
	case data == "sp:stats":
		b.answer(ctx, cb, "")
		b.spStats(ctx, cb)
	case data == "sp:orders":
		b.answer(ctx, cb, "")
		b.spOrders(ctx, cb)
	case data == "sp:buyers":
		b.answer(ctx, cb, "")
		b.spBuyers(ctx, cb)
	case data == "sp:plans":
		b.answer(ctx, cb, "")
		b.spPlans(ctx, cb)
	case data == "sp:cast":
		b.answer(ctx, cb, "")
		sess.step = stepSellerText
		sess.castTo = 0
		sess.castText = ""
		b.editOrSend(ctx, cb, txtSpCastAsk, panelBackKeyboard())
	case data == "sp:casty":
		b.spCastSend(ctx, cb, sess)
	case strings.HasPrefix(data, "sp:ord:"):
		if id, ok := parseCallbackID(data, "sp:ord:"); ok {
			b.answer(ctx, cb, "")
			b.spOrderDetail(ctx, cb, id)
		} else {
			b.answer(ctx, cb, "")
		}
	case strings.HasPrefix(data, "sp:buy:"):
		if id, ok := parseCallbackID(data, "sp:buy:"); ok {
			b.answer(ctx, cb, "")
			b.spBuyerDetail(ctx, cb, uint(id))
		} else {
			b.answer(ctx, cb, "")
		}
	case strings.HasPrefix(data, "sp:msg:"):
		if tg, ok := parseCallbackID(data, "sp:msg:"); ok {
			b.answer(ctx, cb, "")
			sess.step = stepSellerText
			sess.castTo = int64(tg)
			b.editOrSend(ctx, cb, txtSpMsgAsk, panelBackKeyboard())
		} else {
			b.answer(ctx, cb, "")
		}
	case strings.HasPrefix(data, "sp:retry:"):
		if id, ok := parseCallbackID(data, "sp:retry:"); ok {
			b.spRetry(ctx, cb, id)
		} else {
			b.answer(ctx, cb, "")
		}
	default:
		b.answer(ctx, cb, "")
	}
}

// sellerTextIn takes the text the seller wrote at the panel's prompt:
// a message to one buyer goes out at once; an announcement is first
// read back with its audience for one confirming tap, because a
// broadcast cannot be unsent.
func (b *bot) sellerTextIn(ctx context.Context, chatID int64, sess *session, text string) {
	cfg := b.cfg()
	if sess.castTo != 0 {
		to := sess.castTo
		sess.reset()
		if _, err := b.mgr.SendAnnouncement(ctx, cfg.OperatorID, to, text); err != nil {
			b.log.Warn("shop bot could not send the seller's message", "error", err)
			_, kb := b.sellerPanel(ctx)
			b.send(ctx, chatID, txtSpCastFailed, kb)
			return
		}
		_, kb := b.sellerPanel(ctx)
		b.send(ctx, chatID, txtSpMsgDone, kb)
		return
	}
	sess.castText = text
	sess.step = stepNone
	buyers, err := b.mgr.shop.BuyersFor(ctx, cfg.OperatorID)
	n := 0
	if err == nil {
		n = len(buyers)
	}
	b.send(ctx, chatID,
		fmt.Sprintf(txtSpCastConfirm, faNum(int64(n)), escapeHTML(text)),
		tgKeyboard{
			{{Text: btnSpSendAll, Data: "sp:casty"}},
			{{Text: btnSpPanel, Data: "sp:home"}},
		})
}

// spCastSend is the confirming tap: the announcement stored by
// sellerTextIn goes to every buyer, and the seller hears how many it
// reached.
func (b *bot) spCastSend(ctx context.Context, cb *tgCallback, sess *session) {
	text := sess.castText
	sess.reset()
	if strings.TrimSpace(text) == "" {
		b.answer(ctx, cb, "")
		t, kb := b.sellerPanel(ctx)
		b.editOrSend(ctx, cb, t, kb)
		return
	}
	n, err := b.mgr.SendAnnouncement(ctx, b.cfg().OperatorID, 0, text)
	_, kb := b.sellerPanel(ctx)
	if err != nil {
		b.log.Warn("shop bot could not broadcast the announcement", "error", err)
		b.answer(ctx, cb, "")
		b.editOrSend(ctx, cb, txtSpCastFailed, kb)
		return
	}
	b.answer(ctx, cb, "")
	b.editOrSend(ctx, cb, fmt.Sprintf(txtSpCastDone, faNum(int64(n))), kb)
}

func (b *bot) spStats(ctx context.Context, cb *tgCallback) {
	st, err := b.mgr.shop.StatsFor(ctx, b.cfg().OperatorID)
	if err != nil {
		b.editOrSend(ctx, cb, txtError, panelBackKeyboard())
		return
	}
	b.editOrSend(ctx, cb, fmt.Sprintf(txtSpStats,
		faNum(int64(st.TodaySales)), faNum(int64(st.TotalSales)),
		formatPrice(st.RevenueToman), faNum(int64(st.Buyers)),
		faNum(int64(st.Pending))), panelBackKeyboard())
}

// pipelineStatus says whether an order is still the seller's business:
// asked for, waiting on a receipt, waiting on their decision, paid
// and undelivered, or failed to deliver. Delivered, rejected and
// expired orders are history, not a queue.
func pipelineStatus(s string) bool {
	switch s {
	case "pending_review", "paid", "awaiting_receipt", "unpaid", "failed":
		return true
	}
	return false
}

func (b *bot) spOrders(ctx context.Context, cb *tgCallback) {
	orders, err := b.mgr.shop.OrdersFor(ctx, b.cfg().OperatorID, "", 50)
	if err != nil {
		b.editOrSend(ctx, cb, txtError, panelBackKeyboard())
		return
	}
	var queue []shop.Order
	for _, o := range orders {
		if pipelineStatus(o.Status) {
			queue = append(queue, o)
		}
	}
	if len(queue) == 0 {
		b.editOrSend(ctx, cb, txtSpNoOrders, panelBackKeyboard())
		return
	}
	kb := tgKeyboard{}
	for i, o := range queue {
		if i >= 8 {
			break
		}
		kb = append(kb, []tgButton{{
			Text: fmt.Sprintf("#%s · %s · %s", faNum(int64(o.ID)),
				o.PlanName, formatPrice(o.PriceToman)),
			Data: fmt.Sprintf("sp:ord:%d", o.ID),
		}})
	}
	kb = append(kb, []tgButton{{Text: btnSpPanel, Data: "sp:home"}})
	b.editOrSend(ctx, cb,
		fmt.Sprintf(txtSpOrdersTitle, faNum(int64(len(queue)))), kb)
}

func orderStatusText(s string) string {
	switch s {
	case "unpaid":
		return "پرداخت‌نشده"
	case "awaiting_receipt":
		return "در انتظار رسید"
	case "pending_review":
		return "در انتظار تأیید شما"
	case "paid":
		return "پرداخت‌شده، تحویل‌نشده"
	case "fulfilled":
		return "تحویل‌شده"
	case "rejected":
		return "ردشده"
	case "expired":
		return "منقضی‌شده"
	case "failed":
		return "تحویل ناموفق"
	}
	return s
}

func sectionText(s string) string {
	if s == shop.SectionV2ray {
		return "V2ray"
	}
	return "وایرگارد"
}

func methodText(s string) string {
	if s == "zarinpal" {
		return "زرین‌پال"
	}
	return "کارت‌به‌کارت"
}

func (b *bot) spOrderDetail(ctx context.Context, cb *tgCallback, id uint) {
	o, err := b.mgr.shop.OrderByID(ctx, id)
	if err != nil || o == nil || o.OperatorID != b.cfg().OperatorID {
		b.editOrSend(ctx, cb, txtOrderGone, panelBackKeyboard())
		return
	}
	text := fmt.Sprintf(txtSpOrderDetail,
		faNum(int64(o.ID)), escapeHTML(o.BuyerName), escapeHTML(o.PlanName),
		sectionText(o.Section), formatPrice(o.PriceToman),
		methodText(o.Method), orderStatusText(o.Status),
		formatDate(o.CreatedAt))
	kb := tgKeyboard{}
	switch o.Status {
	case "pending_review":
		// The decision itself is the receipt message's own buttons'
		// handler: one code path decides, wherever it is tapped.
		kb = append(kb, []tgButton{
			{Text: btnApprove, Data: fmt.Sprintf("ok:%d", o.ID)},
			{Text: btnReject, Data: fmt.Sprintf("no:%d", o.ID)},
		})
	case "paid", "failed":
		kb = append(kb, []tgButton{
			{Text: btnSpRetry, Data: fmt.Sprintf("sp:retry:%d", o.ID)},
		})
	}
	kb = append(kb,
		[]tgButton{{Text: btnBack, Data: "sp:orders"}},
		[]tgButton{{Text: btnSpPanel, Data: "sp:home"}},
	)
	b.editOrSend(ctx, cb, text, kb)
}

// spRetry is the paid-but-undelivered order's second chance: Fulfill
// is idempotent, so the tap is safe to offer and safe to repeat.
func (b *bot) spRetry(ctx context.Context, cb *tgCallback, id uint) {
	o, err := b.mgr.shop.OrderByID(ctx, id)
	if err != nil || o == nil || o.OperatorID != b.cfg().OperatorID {
		_ = b.api.answerCallbackQuery(ctx, cb.ID, txtOrderGone, true)
		return
	}
	if _, err := b.mgr.shop.Fulfill(ctx, id); err != nil {
		b.log.Warn("shop bot could not redeliver the order", "order", id, "error", err)
		_ = b.api.answerCallbackQuery(ctx, cb.ID, txtSpRetryFailed, true)
		return
	}
	b.answer(ctx, cb, "")
	_, kb := b.sellerPanel(ctx)
	b.editOrSend(ctx, cb, fmt.Sprintf(txtSpRetryOK, id), kb)
}

func (b *bot) spBuyers(ctx context.Context, cb *tgCallback) {
	buyers, err := b.mgr.shop.BuyersFor(ctx, b.cfg().OperatorID)
	if err != nil {
		b.editOrSend(ctx, cb, txtError, panelBackKeyboard())
		return
	}
	if len(buyers) == 0 {
		b.editOrSend(ctx, cb, txtSpNoBuyers, panelBackKeyboard())
		return
	}
	kb := tgKeyboard{}
	for i, by := range buyers {
		if i >= 8 {
			break
		}
		kb = append(kb, []tgButton{{
			Text: fmt.Sprintf("%s · %s", by.Name,
				statusText(model.ClientStatus(by.Status))),
			Data: fmt.Sprintf("sp:buy:%d", by.ClientID),
		}})
	}
	kb = append(kb, []tgButton{{Text: btnSpPanel, Data: "sp:home"}})
	b.editOrSend(ctx, cb,
		fmt.Sprintf(txtSpBuyersTitle, faNum(int64(len(buyers)))), kb)
}

func (b *bot) spBuyerDetail(ctx context.Context, cb *tgCallback, clientID uint) {
	cfg := b.cfg()
	buyers, err := b.mgr.shop.BuyersFor(ctx, cfg.OperatorID)
	if err != nil {
		b.editOrSend(ctx, cb, txtError, panelBackKeyboard())
		return
	}
	var found *shop.Buyer
	for i := range buyers {
		if buyers[i].ClientID == clientID {
			found = &buyers[i]
			break
		}
	}
	if found == nil {
		b.editOrSend(ctx, cb, txtOrderGone, panelBackKeyboard())
		return
	}
	expiry := "بدون محدودیت زمانی"
	if found.ExpiresAt != nil {
		expiry = formatDate(*found.ExpiresAt)
	}
	text := fmt.Sprintf(txtSpBuyerDetail,
		escapeHTML(found.Name), statusText(model.ClientStatus(found.Status)),
		expiry, formatBytes(found.UsedBytes), formatBytes(found.QuotaBytes),
		faNum(int64(found.Orders)))
	// The link is the seller's to re-send when a buyer loses it.
	if opCtx, err := b.mgr.shop.OperatorCtx(ctx, cfg.OperatorID); err == nil {
		if client, err := b.mgr.clients.Get(opCtx, clientID); err == nil && client != nil {
			if link := b.mgr.shop.SubLink(ctx, client); link != "" {
				text += "\n\n" + fmt.Sprintf(txtSpBuyerLink, link)
			} else {
				text += "\n\n" + fmt.Sprintf(txtSpBuyerToken, client.SubToken)
			}
		}
	}
	kb := tgKeyboard{
		{{Text: btnSpMsg, Data: fmt.Sprintf("sp:msg:%d", found.TelegramID)}},
		{{Text: btnBack, Data: "sp:buyers"}},
		{{Text: btnSpPanel, Data: "sp:home"}},
	}
	b.editOrSend(ctx, cb, text, kb)
}

func (b *bot) spPlans(ctx context.Context, cb *tgCallback) {
	plans, err := b.mgr.shop.PlansFor(ctx, b.cfg().OperatorID, false)
	if err != nil {
		b.editOrSend(ctx, cb, txtError, panelBackKeyboard())
		return
	}
	var sb strings.Builder
	sb.WriteString(txtSpPlansTitle)
	if len(plans) == 0 {
		sb.WriteString("\n\nهنوز پلنی نساخته‌اید.")
	}
	for _, p := range plans {
		state := "⏸ غیرفعال"
		if p.Enabled {
			state = "✅ فعال"
		}
		sb.WriteString(fmt.Sprintf(txtSpPlanLine,
			escapeHTML(p.Name), sectionText(p.Section),
			formatPrice(p.PriceToman), formatBytes(p.QuotaBytes),
			formatDays(p.Days), state))
	}
	b.editOrSend(ctx, cb, sb.String(), panelBackKeyboard())
}
