package shopbot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/abolfazl/w-ui/internal/shop"
)

// The conversation. A buyer's place in it is a small session per chat,
// held in memory: a bot restart mid-purchase costs the buyer one tap on
// /start, and in exchange nothing about a half-finished conversation is
// ever written to the database — the orders are the durable record, and
// they are complete on their own.

const (
	stepNone    = ""
	stepReceipt = "receipt" // the next photo is this order's receipt
	stepSupport = "support" // the next message goes to the seller
)

type session struct {
	step    string
	planID  uint
	orderID uint
}

func (s *session) reset() { *s = session{} }

func (b *bot) session(chatID int64) *session {
	s, ok := b.sessions[chatID]
	if !ok {
		s = &session{}
		b.sessions[chatID] = s
	}
	return s
}

// handle routes one update. It runs on the bot's own goroutine, so the
// sessions and the relay map are only ever touched from here.
func (b *bot) handle(ctx context.Context, u tgUpdate) {
	switch {
	case u.Callback != nil:
		b.handleCallback(ctx, u.Callback)
	case u.Message != nil:
		b.handleMessage(ctx, u.Message)
	}
}

func (b *bot) handleMessage(ctx context.Context, m *tgMessage) {
	if m.From == nil {
		return
	}
	cfg := b.cfg()
	chatID := m.Chat.ID

	// The seller's chat is the shop's back room: the only thing the bot
	// does there is carry a reply back to the buyer it belongs to. A
	// reply to anything else — a receipt, say — is the seller talking to
	// themselves, and approvals happen on the receipt's buttons.
	if cfg.AdminChatID != 0 && chatID == cfg.AdminChatID {
		if m.ReplyTo != nil {
			if buyer, ok := b.relay[m.ReplyTo.ID]; ok {
				if _, err := b.api.copyMessage(ctx, buyer, chatID, m.ID, "", nil); err != nil {
					b.log.Warn("shop bot could not relay the seller's reply", "error", err)
				}
			}
		}
		return
	}
	// Sales happen one-to-one. In a group the bot stays silent.
	if chatID != m.From.ID {
		return
	}

	sess := b.session(chatID)
	text := strings.TrimSpace(m.Text)

	// Commands win over every step: wherever the buyer is, they can
	// always start over.
	switch {
	case text == "/start":
		sess.reset()
		welcome := cfg.Welcome
		if strings.TrimSpace(welcome) == "" {
			welcome = txtWelcomeDefault
		} else {
			welcome = escapeHTML(welcome)
		}
		b.send(ctx, chatID, welcome, menuKeyboard())
		return
	case text == "/cancel" || text == txtBackWord:
		sess.reset()
		b.send(ctx, chatID, txtMenuTitle, menuKeyboard())
		return
	}

	switch sess.step {
	case stepReceipt:
		if m.photoFileID() != "" {
			b.takeReceipt(ctx, m, sess)
			return
		}
		b.send(ctx, chatID, txtReceiptAskPhoto, nil)
		return
	case stepSupport:
		b.forwardSupport(ctx, m, sess)
		return
	}

	// Anything else at the top level earns the menu again: a buyer who
	// typed something the bot does not know is a buyer who is lost.
	b.send(ctx, chatID, txtMenuTitle, menuKeyboard())
}

func (b *bot) handleCallback(ctx context.Context, cb *tgCallback) {
	if cb.From == nil {
		return
	}
	data := cb.Data
	chatID := cb.From.ID
	if cb.Message != nil {
		chatID = cb.Message.Chat.ID
	}
	sess := b.session(chatID)

	switch {
	case data == "buy" || data == "renew":
		b.answer(ctx, cb, "")
		b.showPlans(ctx, cb)
	case data == "mine":
		b.answer(ctx, cb, "")
		text, kb := b.mine(ctx, cb.From.ID)
		b.editOrSend(ctx, cb, text, kb)
	case data == "support":
		b.answer(ctx, cb, "")
		sess.step = stepSupport
		b.editOrSend(ctx, cb, txtSupportAsk, backKeyboard())
	case data == "menu":
		b.answer(ctx, cb, "")
		sess.reset()
		b.editOrSend(ctx, cb, txtMenuTitle, menuKeyboard())
	case strings.HasPrefix(data, "plan:"):
		if id, ok := parseCallbackID(data, "plan:"); ok {
			b.answer(ctx, cb, "")
			b.showPayments(ctx, cb, sess, id)
		}
	case strings.HasPrefix(data, "card:"):
		if id, ok := parseCallbackID(data, "card:"); ok {
			b.startCardOrder(ctx, cb, sess, id)
		}
	case strings.HasPrefix(data, "zp:"):
		if id, ok := parseCallbackID(data, "zp:"); ok {
			b.startZarinpalOrder(ctx, cb, id)
		}
	case strings.HasPrefix(data, "check:"):
		if id, ok := parseCallbackID(data, "check:"); ok {
			b.checkZarinpal(ctx, cb, id)
		}
	case strings.HasPrefix(data, "ok:"):
		if id, ok := parseCallbackID(data, "ok:"); ok {
			b.decide(ctx, cb, id, true)
		}
	case strings.HasPrefix(data, "no:"):
		if id, ok := parseCallbackID(data, "no:"); ok {
			b.decide(ctx, cb, id, false)
		}
	default:
		b.answer(ctx, cb, "")
	}
}

// parseCallbackID reads the number after a callback prefix. The data is
// the bot's own, but a stale button from an older build — or a curious
// buyer — is not a reason to panic.
func parseCallbackID(data, prefix string) (uint, bool) {
	n, err := strconv.ParseUint(strings.TrimPrefix(data, prefix), 10, 64)
	if err != nil {
		return 0, false
	}
	return uint(n), true
}

// menuKeyboard is the main menu, offered at /start and under every
// finished flow.
func menuKeyboard() tgKeyboard {
	return tgKeyboard{
		{{Text: btnBuy, Data: "buy"}, {Text: btnMine, Data: "mine"}},
		{{Text: btnSupport, Data: "support"}},
	}
}

func backKeyboard() tgKeyboard {
	return tgKeyboard{{{Text: btnBack, Data: "menu"}}}
}

// send writes a message and logs, rather than returns, a failure: by the
// time the bot is speaking, the conversation has already moved on.
func (b *bot) send(ctx context.Context, chatID int64, text string, kb tgKeyboard) {
	if _, err := b.api.sendMessage(ctx, chatID, text, kb); err != nil {
		b.log.Warn("shop bot could not send", "error", err)
	}
}

func (b *bot) answer(ctx context.Context, cb *tgCallback, text string) {
	_ = b.api.answerCallbackQuery(ctx, cb.ID, text, false)
}

// editOrSend rewrites the message a button was pressed on, which keeps
// a conversation to one growing thread instead of a pile of menus; when
// the message cannot be edited — too old, or a photo — a fresh message
// carries the screen instead.
func (b *bot) editOrSend(ctx context.Context, cb *tgCallback, text string, kb tgKeyboard) {
	if cb.Message != nil {
		if err := b.api.editMessageText(ctx, cb.Message.Chat.ID, cb.Message.ID, text, kb); err == nil {
			return
		}
	}
	chatID := cb.From.ID
	if cb.Message != nil {
		chatID = cb.Message.Chat.ID
	}
	b.send(ctx, chatID, text, kb)
}

// showPlans lists what this operator sells.
func (b *bot) showPlans(ctx context.Context, cb *tgCallback) {
	plans, err := b.mgr.shop.PlansFor(ctx, b.cfg().OperatorID, true)
	if err != nil {
		b.log.Warn("shop bot could not read the plans", "error", err)
		b.editOrSend(ctx, cb, txtError, backKeyboard())
		return
	}
	if len(plans) == 0 {
		b.editOrSend(ctx, cb, txtNoPlans, backKeyboard())
		return
	}
	kb := tgKeyboard{}
	for i := range plans {
		kb = append(kb, []tgButton{{Text: planButtonText(&plans[i]), Data: fmt.Sprintf("plan:%d", plans[i].ID)}})
	}
	kb = append(kb, []tgButton{{Text: btnBack, Data: "menu"}})
	b.editOrSend(ctx, cb, txtChoosePlan, kb)
}

// plan finds one enabled plan by id, re-read at the moment of the tap:
// a plan the operator disabled while the menu sat open is not for sale.
func (b *bot) plan(ctx context.Context, id uint) *shop.Plan {
	plans, err := b.mgr.shop.PlansFor(ctx, b.cfg().OperatorID, true)
	if err != nil {
		b.log.Warn("shop bot could not read the plans", "error", err)
		return nil
	}
	for i := range plans {
		if plans[i].ID == id {
			return &plans[i]
		}
	}
	return nil
}

// zarinpalOffered is whether online payment can be offered right now:
// a merchant alone is not enough — the gateway must be able to call the
// panel back, which needs the panel's public address.
func (b *bot) zarinpalOffered(ctx context.Context) bool {
	if b.cfg().ZarinpalMerchant == "" {
		return false
	}
	base, err := b.mgr.shop.PublicBase(ctx)
	return err == nil && strings.TrimSpace(base) != ""
}

// showPayments offers the ways this plan can be paid for — only the
// ones the operator has actually set up.
func (b *bot) showPayments(ctx context.Context, cb *tgCallback, sess *session, planID uint) {
	plan := b.plan(ctx, planID)
	if plan == nil {
		b.editOrSend(ctx, cb, txtOrderGone, backKeyboard())
		return
	}
	sess.planID = planID

	var kb tgKeyboard
	if b.cfg().CardNumber != "" {
		kb = append(kb, []tgButton{{Text: btnPayCard, Data: fmt.Sprintf("card:%d", planID)}})
	}
	if b.zarinpalOffered(ctx) {
		kb = append(kb, []tgButton{{Text: btnPayLink, Data: fmt.Sprintf("zp:%d", planID)}})
	}
	if len(kb) == 0 {
		b.editOrSend(ctx, cb, txtPayNotConfigured, backKeyboard())
		return
	}
	kb = append(kb, []tgButton{{Text: btnBack, Data: "buy"}})
	b.editOrSend(ctx, cb, txtChoosePay+"\n\n"+planButtonText(plan), kb)
}

// newOrder records a purchase at the moment the buyer commits to a way
// of paying, with the plan's terms copied onto it: what was sold is what
// the order says, even if the plan is edited tomorrow.
//
// Kind is always "new" here, deliberately: whether this purchase creates
// a customer or renews the one the buyer already has is decided by
// shop.Fulfill, which finds the existing customer by Telegram id. The
// bot would have to read the operator's customers to guess earlier, and
// its guess could still be wrong by the time the money lands.
func (b *bot) newOrder(ctx context.Context, from *tgUser, plan *shop.Plan, method, status string) (*shop.Order, error) {
	o := &shop.Order{
		OperatorID:  b.cfg().OperatorID,
		BuyerTGID:   from.ID,
		BuyerName:   buyerName(from),
		PlanID:      plan.ID,
		PlanName:    plan.Name,
		QuotaBytes:  plan.QuotaBytes,
		Days:        plan.Days,
		PriceToman:  plan.PriceToman,
		DeviceLimit: plan.DeviceLimit,
		Kind:        "new",
		Method:      method,
		Status:      status,
	}
	if err := b.mgr.shop.CreateOrder(ctx, o); err != nil {
		b.log.Warn("shop bot could not create the order", "error", err)
		return nil, err
	}
	return o, nil
}

// startCardOrder opens a card payment: the order waits for its receipt,
// and the buyer is told exactly where the money goes.
func (b *bot) startCardOrder(ctx context.Context, cb *tgCallback, sess *session, planID uint) {
	plan := b.plan(ctx, planID)
	if plan == nil {
		b.answer(ctx, cb, txtOrderGone)
		return
	}
	o, err := b.newOrder(ctx, cb.From, plan, "card", "awaiting_receipt")
	if err != nil {
		_ = b.api.answerCallbackQuery(ctx, cb.ID, txtOrderFailed, true)
		return
	}
	b.answer(ctx, cb, "")
	sess.step = stepReceipt
	sess.planID = planID
	sess.orderID = o.ID
	cfg := b.cfg()
	text := fmt.Sprintf(txtCardInstructions,
		escapeHTML(cfg.CardNumber), escapeHTML(cfg.CardHolder), formatPrice(o.PriceToman))
	b.editOrSend(ctx, cb, text, backKeyboard())
}

// takeReceipt files a receipt photo against the order waiting for it and
// puts it in front of the seller, with the decision on buttons under it.
func (b *bot) takeReceipt(ctx context.Context, m *tgMessage, sess *session) {
	chatID := m.Chat.ID
	o, err := b.mgr.shop.OrderByID(ctx, sess.orderID)
	if err != nil || o == nil || o.BuyerTGID != m.From.ID || o.Status != "awaiting_receipt" {
		sess.reset()
		b.send(ctx, chatID, txtOrderGone, menuKeyboard())
		return
	}
	o.ReceiptFileID = m.photoFileID()
	o.Status = "pending_review"
	if err := b.mgr.shop.UpdateOrder(ctx, o); err != nil {
		b.log.Warn("shop bot could not file the receipt", "error", err)
		b.send(ctx, chatID, txtError, nil)
		return
	}
	sess.reset()
	b.send(ctx, chatID, txtReceiptReceived, menuKeyboard())

	if admin := b.cfg().AdminChatID; admin != 0 {
		caption := fmt.Sprintf(txtReceiptCaption,
			escapeHTML(o.BuyerName), o.BuyerTGID, escapeHTML(o.PlanName), formatPrice(o.PriceToman), o.ID)
		kb := tgKeyboard{{
			{Text: btnApprove, Data: fmt.Sprintf("ok:%d", o.ID)},
			{Text: btnReject, Data: fmt.Sprintf("no:%d", o.ID)},
		}}
		if _, err := b.api.copyMessage(ctx, admin, chatID, m.ID, caption, kb); err != nil {
			b.log.Warn("shop bot could not send the receipt to the seller", "error", err)
		}
	}
}

// startZarinpalOrder opens an online payment and hands the buyer its
// page, with a button to say the money is sent.
func (b *bot) startZarinpalOrder(ctx context.Context, cb *tgCallback, planID uint) {
	plan := b.plan(ctx, planID)
	if plan == nil {
		b.answer(ctx, cb, txtOrderGone)
		return
	}
	o, err := b.newOrder(ctx, cb.From, plan, "zarinpal", "unpaid")
	if err != nil {
		_ = b.api.answerCallbackQuery(ctx, cb.ID, txtOrderFailed, true)
		return
	}
	// The callback URL names the order by its public token, which
	// CreateOrder drew and wrote back onto the order.
	base, baseErr := b.mgr.shop.PublicBase(ctx)
	if o.PublicToken == "" || baseErr != nil {
		b.log.Warn("shop bot could not build the payment callback", "error", baseErr)
		_ = b.api.answerCallbackQuery(ctx, cb.ID, txtZarinpalFailed, true)
		return
	}
	callback := strings.TrimRight(base, "/") + "/shop/callback/" + o.PublicToken
	authority, err := RequestPayment(ctx, b.cfg().ZarinpalMerchant,
		o.PriceToman, fmt.Sprintf(txtZarinpalDesc, plan.Name), callback)
	if err != nil {
		b.log.Warn("shop bot could not open the payment", "error", err)
		_ = b.api.answerCallbackQuery(ctx, cb.ID, txtZarinpalFailed, true)
		b.editOrSend(ctx, cb, txtZarinpalFailed, backKeyboard())
		return
	}
	o.Authority = authority
	if err := b.mgr.shop.UpdateOrder(ctx, o); err != nil {
		b.log.Warn("shop bot could not store the payment authority", "error", err)
	}
	b.answer(ctx, cb, "")
	kb := tgKeyboard{
		{{Text: btnPayLink, URL: PayURL(authority)}},
		{{Text: btnPaid, Data: fmt.Sprintf("check:%d", o.ID)}},
		{{Text: btnBack, Data: "menu"}},
	}
	b.editOrSend(ctx, cb, txtZarinpalReady, kb)
}

// checkZarinpal is the buyer's "I paid" button: ask the gateway, and
// when the money is real, deliver.
func (b *bot) checkZarinpal(ctx context.Context, cb *tgCallback, orderID uint) {
	o, err := b.mgr.shop.OrderByID(ctx, orderID)
	if err != nil || o == nil || o.BuyerTGID != cb.From.ID {
		b.answer(ctx, cb, txtOrderGone)
		return
	}
	if o.Status == "fulfilled" || o.Status == "paid" {
		b.answer(ctx, cb, txtPaidConfirmed)
		_ = b.deliver(ctx, o)
		return
	}
	paid, err := b.mgr.VerifyZarinpal(ctx, o)
	if err != nil {
		b.log.Warn("shop bot could not verify the payment", "order", o.ID, "error", err)
		_ = b.api.answerCallbackQuery(ctx, cb.ID, txtZarinpalFailed, true)
		return
	}
	if !paid {
		b.answer(ctx, cb, txtNotPaidYet)
		return
	}
	b.answer(ctx, cb, txtPaidConfirmed)
	b.editOrSend(ctx, cb, txtPaidConfirmed, nil)
	_ = b.deliver(ctx, o)
}

// decide is the seller ruling on a receipt: approve delivers, reject
// refuses. The buttons live in the seller's chat, and only a press there
// counts — the same button forwarded anywhere else is decoration.
func (b *bot) decide(ctx context.Context, cb *tgCallback, orderID uint, approve bool) {
	cfg := b.cfg()
	if cb.Message == nil || cfg.AdminChatID == 0 || cb.Message.Chat.ID != cfg.AdminChatID {
		_ = b.api.answerCallbackQuery(ctx, cb.ID, txtNotAllowed, true)
		return
	}
	o, err := b.mgr.shop.OrderByID(ctx, orderID)
	if err != nil || o == nil {
		b.answer(ctx, cb, txtOrderGone)
		return
	}

	if !approve {
		if err := b.mgr.shop.Reject(ctx, orderID); err != nil {
			b.log.Warn("shop bot could not reject the order", "order", orderID, "error", err)
			_ = b.api.answerCallbackQuery(ctx, cb.ID, txtDecisionFailed, true)
			return
		}
		b.answer(ctx, cb, "")
		_ = b.api.editMessageReplyMarkup(ctx, cb.Message.Chat.ID, cb.Message.ID, nil)
		b.send(ctx, cfg.AdminChatID, fmt.Sprintf(txtRejectedSeller, o.ID), nil)
		b.send(ctx, o.BuyerTGID, txtRejectedBuyer, menuKeyboard())
		return
	}

	if err := b.deliver(ctx, o); err != nil {
		_ = b.api.answerCallbackQuery(ctx, cb.ID, txtDecisionFailed, true)
		return
	}
	b.answer(ctx, cb, "")
	_ = b.api.editMessageReplyMarkup(ctx, cb.Message.Chat.ID, cb.Message.ID, nil)
	b.send(ctx, cfg.AdminChatID, fmt.Sprintf(txtApprovedSeller, o.ID), nil)
}

// deliver fulfills an order and puts the subscription in the buyer's
// hands. Fulfillment is the shop's and is idempotent, so delivering
// twice — a seller's tap racing the buyer's — hands over the same
// subscription twice rather than making two.
func (b *bot) deliver(ctx context.Context, o *shop.Order) error {
	client, err := b.mgr.shop.Fulfill(ctx, o.ID)
	if err != nil {
		b.log.Warn("shop bot could not fulfill the order", "order", o.ID, "error", err)
		b.send(ctx, o.BuyerTGID, txtDeliverFailed, menuKeyboard())
		if admin := b.cfg().AdminChatID; admin != 0 {
			b.send(ctx, admin, fmt.Sprintf(txtDeliverFailedSeller,
				o.ID, err, escapeHTML(o.BuyerName), o.BuyerTGID), nil)
		}
		return err
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(txtDelivered, escapeHTML(o.PlanName)))
	if client.ExpiresAt != nil {
		sb.WriteString("\n" + fmt.Sprintf(txtDeliveredUntil, formatDate(*client.ExpiresAt)))
	}
	if link := b.mgr.shop.SubLink(ctx, client); link != "" {
		sb.WriteString("\n\n" + fmt.Sprintf(txtDeliverLink, link))
	} else {
		// No public address is configured, so no link can be built. The
		// buyer gets the token that identifies their subscription, and
		// the seller gets it too, with a note to send the link by hand —
		// a paid buyer must never be left holding nothing.
		sb.WriteString("\n\n" + fmt.Sprintf(txtDeliverToken, escapeHTML(client.SubToken)))
		if admin := b.cfg().AdminChatID; admin != 0 {
			b.send(ctx, admin, fmt.Sprintf(txtSellerLinkNote,
				escapeHTML(o.BuyerName), escapeHTML(client.SubToken)), nil)
		}
	}
	b.send(ctx, o.BuyerTGID, sb.String(), menuKeyboard())
	return nil
}

// mine answers "my subscription": the buyer's customers under this
// operator, found by their Telegram id in the operator's own scope.
func (b *bot) mine(ctx context.Context, buyerID int64) (string, tgKeyboard) {
	opCtx, err := b.mgr.shop.OperatorCtx(ctx, b.cfg().OperatorID)
	if err != nil {
		b.log.Warn("shop bot could not build the operator scope", "error", err)
		return txtError, backKeyboard()
	}
	customers, err := b.mgr.clients.ByTelegramID(opCtx, buyerID)
	if err != nil {
		b.log.Warn("shop bot could not look the buyer up", "error", err)
		return txtError, backKeyboard()
	}
	if len(customers) == 0 {
		return txtMineEmpty, tgKeyboard{{{Text: btnBuy, Data: "buy"}}}
	}

	var sb strings.Builder
	sb.WriteString(txtMineTitle)
	now := time.Now()
	for i := range customers {
		c := &customers[i]
		block := "\n\n" + fmt.Sprintf(txtMineStatus, statusText(c.Status))
		block += "\n" + fmt.Sprintf(txtMineUsed, formatBytes(c.UsedBytes), formatBytes(c.QuotaBytes))
		if c.QuotaBytes > 0 {
			block += "\n" + fmt.Sprintf(txtMineLeft, formatBytes(c.RemainingBytes()))
		}
		if c.ExpiresAt != nil {
			block += "\n" + fmt.Sprintf(txtMineExpiry,
				formatDate(*c.ExpiresAt), daysLeftText(*c.ExpiresAt, now))
		} else {
			block += "\n" + txtMineNoExpiry
		}
		if link := b.mgr.shop.SubLink(ctx, c); link != "" {
			block += "\n" + fmt.Sprintf(txtMineLink, link)
		} else if c.SubToken != "" {
			block += "\n" + fmt.Sprintf(txtMineToken, escapeHTML(c.SubToken))
		}
		// A message has a ceiling; a buyer with many subscriptions sees
		// the first ones whole rather than all of them cut in half.
		if sb.Len()+len(block) > 3800 {
			break
		}
		sb.WriteString(block)
	}
	kb := tgKeyboard{
		{{Text: btnRenew, Data: "buy"}},
		{{Text: btnBack, Data: "menu"}},
	}
	return sb.String(), kb
}

// forwardSupport carries the buyer's armed message to the seller and
// remembers the copy, so the seller's reply to it finds its way back.
func (b *bot) forwardSupport(ctx context.Context, m *tgMessage, sess *session) {
	chatID := m.Chat.ID
	sess.reset()
	admin := b.cfg().AdminChatID
	if admin == 0 {
		b.send(ctx, chatID, txtSupportOff, menuKeyboard())
		return
	}
	b.send(ctx, admin, fmt.Sprintf(txtSupportFrom, escapeHTML(buyerName(m.From)), m.From.ID), nil)
	if copied, err := b.api.copyMessage(ctx, admin, chatID, m.ID, "", nil); err != nil {
		b.log.Warn("shop bot could not forward the support message", "error", err)
	} else {
		b.relay[copied.ID] = chatID
	}
	b.send(ctx, chatID, txtSupportSent, menuKeyboard())
}
