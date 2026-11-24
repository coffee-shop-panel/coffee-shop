package shopbot

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/shop"
)

// The conversation. A buyer's place in it is a small session per chat,
// held in memory: a bot restart mid-purchase costs the buyer one tap on
// /start, and in exchange nothing about a half-finished conversation is
// ever written to the database — the orders are the durable record, and
// they are complete on their own.

const (
	stepNone       = ""
	stepReceipt    = "receipt"    // the next photo is this order's receipt
	stepSupport    = "support"    // the next message goes to the seller
	stepSellerText = "sellertext" // the seller is writing at their panel's prompt
	stepCode       = "code"       // the next text is a discount code for the checkout below
	stepGift       = "gift"       // the next text is a gift code to redeem into the wallet
	stepTopup      = "topup"      // the next text is a custom top-up amount
)

type session struct {
	step    string
	planID  uint
	orderID uint
	// The checkout being put together, between the plan tap and the
	// order it becomes: how many of the plan, and the code named
	// against the total. qty under one is one; an empty code is none.
	// A plan tap starts a fresh checkout -- see planPicked -- so a
	// discount priced for one basket is never carried into another.
	qty      int
	code     string
	discount int64
	// renewing says this walk began at a «تمدید» button rather than a
	// «خرید» one: a renewal is more of the one customer's plan, so it
	// keeps a quantity of one and never sees the quantity step.
	renewing bool
	// The seller panel's writing desk: castText holds an announcement
	// waiting for its confirming tap; castTo is the one buyer a
	// message is addressed to (zero = the announcement goes to every
	// buyer). Only ever set in the seller's own session.
	castText string
	castTo   int64
	// testing is the seller walking their own shop as a buyer (the
	// Mirza way: the owner gets the full shop menu too, to test it).
	// While it is set, the seller's messages take the buyer's road —
	// support texts, receipt photos and all — and /start is the way
	// back to the panel.
	testing bool
	// section is the shop section whose plans are on screen, and
	// sectionChoice says the buyer was asked which section they
	// wanted on the way in -- which is what the plan list's back
	// button points at: the question again, or the menu when there
	// was no question to go back to.
	section       string
	sectionChoice bool
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

	// The seller's chat is the shop's back room: its work there is
	// carrying a reply back to the buyer it belongs to, and hosting
	// the seller's own panel. A seller who writes at the panel's
	// prompt is writing an announcement or a buyer message; anything
	// else — /start above all — is answered with the panel itself,
	// never silence: a bot that says nothing to its own seller reads
	// as a dead one.
	if cfg.AdminChatID != 0 && chatID == cfg.AdminChatID {
		if m.ReplyTo != nil {
			if buyer, ok := b.relay[m.ReplyTo.ID]; ok {
				if _, err := b.api.copyMessage(ctx, buyer, chatID, m.ID, "", nil); err != nil {
					b.log.Warn("shop bot could not relay the seller's reply", "error", err)
				}
				return
			}
		}
		sess := b.session(chatID)
		text := strings.TrimSpace(m.Text)
		if sess.step == stepSellerText && text != "" && !strings.HasPrefix(text, "/") {
			b.sellerTextIn(ctx, chatID, sess, text)
			return
		}
		if sess.testing && text != "/start" {
			// The seller is testing their own shop: this message
			// belongs to the buyer's flow below, receipt photo,
			// support text and all.
		} else {
			sess.reset()
			b.sellerHome(ctx, chatID)
			return
		}
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
	case text == "/start" || strings.HasPrefix(text, "/start "):
		sess.reset()
		b.bindReferral(ctx, m)
		welcome := cfg.Welcome
		if strings.TrimSpace(welcome) == "" {
			welcome = txtWelcomeDefault
		} else {
			welcome = escapeHTML(welcome)
		}
		b.send(ctx, chatID, welcome, b.menuKeyboard(ctx))
		return
	case text == "/cancel" || text == txtBackWord:
		sess.reset()
		b.send(ctx, chatID, txtMenuTitle, b.menuKeyboard(ctx))
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
	case stepCode:
		b.takeCode(ctx, m, sess)
		return
	case stepGift:
		b.takeGift(ctx, m, sess)
		return
	case stepTopup:
		b.takeTopupAmount(ctx, m, sess)
		return
	}

	// Anything else at the top level earns the menu again: a buyer who
	// typed something the bot does not know is a buyer who is lost.
	b.send(ctx, chatID, txtMenuTitle, b.menuKeyboard(ctx))
}

// bindReferral reads a /start payload -- ref_<telegram id>, the tail
// of the invitation link the wallet screen hands out -- and records
// who brought this buyer. It is best effort by design: the shop's
// binding rules refuse the stale, the forged and the self-invitation
// quietly, and even a failure of the recording itself is only logged.
// A buyer's arrival must never break because the link they arrived
// holding was odd. The seller's own chat never binds: the seller
// testing their shop is not their own referral.
func (b *bot) bindReferral(ctx context.Context, m *tgMessage) {
	cfg := b.cfg()
	if cfg.AdminChatID != 0 && m.Chat.ID == cfg.AdminChatID {
		return
	}
	fields := strings.Fields(m.Text)
	if len(fields) < 2 || !strings.HasPrefix(fields[1], "ref_") {
		return
	}
	ref, err := strconv.ParseInt(strings.TrimPrefix(fields[1], "ref_"), 10, 64)
	if err != nil || ref <= 0 {
		return
	}
	if err := b.mgr.shop.BindReferral(ctx, cfg.OperatorID, m.From.ID, ref); err != nil {
		b.log.Warn("shop bot could not bind a referral", "buyer", m.From.ID, "error", err)
	}
}

// sellerHome answers the seller in their own chat with the seller
// panel itself (seller.go): proof the shop is alive, the day's
// numbers, and the menu of the seller's own work.
func (b *bot) sellerHome(ctx context.Context, chatID int64) {
	text, kb := b.sellerPanel(ctx)
	b.send(ctx, chatID, text, kb)
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
		// Which door the buyer came through decides whether a
		// quantity is theirs to choose: a purchase may be bulk, a
		// renewal is more of the one plan they already hold.
		sess.renewing = data == "renew"
		b.showSections(ctx, cb, sess)
	case data == "plans":
		// Back from the payment choices: the plan list of the section
		// being browsed, or the section question when the session has
		// lost it (a restart between the two taps).
		b.answer(ctx, cb, "")
		if sess.section == "" {
			b.showSections(ctx, cb, sess)
		} else {
			b.showPlans(ctx, cb, sess, sess.section)
		}
	case data == "sec:wireguard":
		b.answer(ctx, cb, "")
		b.showPlans(ctx, cb, sess, shop.SectionWireGuard)
	case data == "sec:v2ray":
		b.answer(ctx, cb, "")
		b.showPlans(ctx, cb, sess, shop.SectionV2ray)
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
		// The buyer's menu under the seller is still a test drive:
		// going back to it does not end the test.
		if cfg := b.cfg(); cfg.AdminChatID != 0 && chatID == cfg.AdminChatID {
			sess.testing = true
		}
		b.editOrSend(ctx, cb, txtMenuTitle, b.menuKeyboard(ctx))
	case data == "wallet":
		b.answer(ctx, cb, "")
		text, kb := b.walletScreen(ctx, cb.From.ID)
		b.editOrSend(ctx, cb, text, kb)
	case data == "topup":
		b.answer(ctx, cb, "")
		b.editOrSend(ctx, cb, txtTopupChoose, topupAmountKeyboard())
	case data == "topcustom":
		b.answer(ctx, cb, "")
		sess.step = stepTopup
		b.editOrSend(ctx, cb, txtTopupCustomAsk, walletBackKeyboard())
	case data == "gift":
		b.answer(ctx, cb, "")
		sess.step = stepGift
		b.editOrSend(ctx, cb, txtGiftAsk, walletBackKeyboard())
	case data == "refer":
		b.answer(ctx, cb, "")
		text, kb := b.referralScreen(ctx, cb.From.ID)
		b.editOrSend(ctx, cb, text, kb)
	case data == "trial":
		b.claimTrial(ctx, cb)
	case data == "wheel":
		b.spinWheel(ctx, cb)
	case data == "tutorial":
		b.answer(ctx, cb, "")
		b.sendLong(ctx, chatID, b.cfg().TutorialText)
	case data == "faq":
		b.answer(ctx, cb, "")
		b.sendLong(ctx, chatID, b.cfg().FaqText)
	case strings.HasPrefix(data, "plan:"):
		if id, ok := parseCallbackID(data, "plan:"); ok {
			b.answer(ctx, cb, "")
			b.planPicked(ctx, cb, sess, id)
		}
	case strings.HasPrefix(data, "qty:"):
		if planID, n, ok := parseQtyCallback(data); ok {
			b.answer(ctx, cb, "")
			sess.qty = n
			sess.planID = planID
			b.showPayments(ctx, cb, sess, planID)
		} else {
			b.answer(ctx, cb, "")
		}
	case strings.HasPrefix(data, "pay:"):
		// The way past the code prompt without a code: the payment
		// screen as it stands, checkout untouched.
		if id, ok := parseCallbackID(data, "pay:"); ok {
			b.answer(ctx, cb, "")
			sess.step = stepNone
			b.showPayments(ctx, cb, sess, id)
		}
	case strings.HasPrefix(data, "code:"):
		if id, ok := parseCallbackID(data, "code:"); ok {
			b.answer(ctx, cb, "")
			sess.step = stepCode
			sess.planID = id
			b.editOrSend(ctx, cb, txtCodeAsk, tgKeyboard{
				{{Text: btnSkipCode, Data: fmt.Sprintf("pay:%d", id)}},
				{{Text: btnBack, Data: "plans"}},
			})
		}
	case strings.HasPrefix(data, "wal:"):
		if id, ok := parseCallbackID(data, "wal:"); ok {
			b.startWalletOrder(ctx, cb, sess, id)
		}
	case strings.HasPrefix(data, "card:"):
		if id, ok := parseCallbackID(data, "card:"); ok {
			b.startCardOrder(ctx, cb, sess, id)
		}
	case strings.HasPrefix(data, "zp:"):
		if id, ok := parseCallbackID(data, "zp:"); ok {
			b.startZarinpalOrder(ctx, cb, sess, id)
		}
	case strings.HasPrefix(data, "top:"):
		if amount, ok := parseCallbackID(data, "top:"); ok && amount > 0 {
			b.answer(ctx, cb, "")
			text, kb := b.topupPayScreen(ctx, int64(amount))
			b.editOrSend(ctx, cb, text, kb)
		} else {
			b.answer(ctx, cb, "")
		}
	case strings.HasPrefix(data, "topcard:"):
		if amount, ok := parseCallbackID(data, "topcard:"); ok && amount > 0 {
			b.startTopupCard(ctx, cb, sess, int64(amount))
		} else {
			b.answer(ctx, cb, "")
		}
	case strings.HasPrefix(data, "topzp:"):
		if amount, ok := parseCallbackID(data, "topzp:"); ok && amount > 0 {
			b.startTopupZarinpal(ctx, cb, sess, int64(amount))
		} else {
			b.answer(ctx, cb, "")
		}
	case strings.HasPrefix(data, "check:"):
		if id, ok := parseCallbackID(data, "check:"); ok {
			b.checkZarinpal(ctx, cb, id)
		}
	case strings.HasPrefix(data, "sp:"):
		b.sellerPanelCallback(ctx, cb, sess, data)
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
// finished flow. What it holds beyond the three standing doors is
// the shop's own: the trial button exists only in a shop that offers
// a trial, the wheel only in one whose wheel is on, and the
// teaching buttons only where the seller has written the words --
// a menu that offers what the shop does not have is a menu that
// lies.
func (b *bot) menuKeyboard(ctx context.Context) tgKeyboard {
	cfg := b.cfg()
	kb := tgKeyboard{
		{{Text: btnBuy, Data: "buy"}, {Text: btnMine, Data: "mine"}},
		{{Text: btnWallet, Data: "wallet"}},
	}
	if cfg.TrialEnabled {
		kb = append(kb, []tgButton{{Text: btnTrial, Data: "trial"}})
	}
	if cfg.WheelEnabled {
		kb = append(kb, []tgButton{{Text: btnWheel, Data: "wheel"}})
	}
	var docs []tgButton
	if strings.TrimSpace(cfg.TutorialText) != "" {
		docs = append(docs, tgButton{Text: btnTutorial, Data: "tutorial"})
	}
	if strings.TrimSpace(cfg.FaqText) != "" {
		docs = append(docs, tgButton{Text: btnFaq, Data: "faq"})
	}
	if len(docs) > 0 {
		kb = append(kb, docs)
	}
	return append(kb, []tgButton{{Text: btnSupport, Data: "support"}})
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

// sellableSections is the sections this shop can actually sell right
// now, in the order they are offered: a section counts when it has an
// enabled plan of its own and at least one of the seller's tunnels
// belongs to it. A plan alone is not sellable -- fulfillment delivers
// on the section's tunnels, and a section with none would take the
// money and have nothing to hand over.
func (b *bot) sellableSections(ctx context.Context) ([]string, error) {
	plans, err := b.mgr.shop.PlansFor(ctx, b.cfg().OperatorID, true)
	if err != nil {
		return nil, err
	}
	cfg := b.cfg()
	out := []string{}
	for _, section := range []string{shop.SectionWireGuard, shop.SectionV2ray} {
		hasPlan := false
		for i := range plans {
			if shop.NormalizeSection(plans[i].Section) == section {
				hasPlan = true
				break
			}
		}
		if !hasPlan {
			continue
		}
		ids, err := b.mgr.shop.SectionInterfaces(ctx, &cfg, section)
		if err != nil {
			return nil, err
		}
		if len(ids) > 0 {
			out = append(out, section)
		}
	}
	return out, nil
}

// showSections opens a purchase. With two sections to sell, the buyer
// chooses one first -- the two are different products, in different
// apps, and a mixed plan list sells the wrong one to somebody every
// day. With one, the question would be a tap that answers itself, so
// it is skipped and the plans open directly.
func (b *bot) showSections(ctx context.Context, cb *tgCallback, sess *session) {
	sellable, err := b.sellableSections(ctx)
	if err != nil {
		b.log.Warn("shop bot could not work out what it can sell", "error", err)
		b.editOrSend(ctx, cb, txtError, backKeyboard())
		return
	}
	switch len(sellable) {
	case 0:
		sess.sectionChoice = false
		b.editOrSend(ctx, cb, txtNoPlans, backKeyboard())
	case 1:
		sess.sectionChoice = false
		b.showPlans(ctx, cb, sess, sellable[0])
	default:
		sess.sectionChoice = true
		kb := tgKeyboard{}
		for _, section := range sellable {
			kb = append(kb, []tgButton{{Text: sectionName(section), Data: "sec:" + section}})
		}
		kb = append(kb, []tgButton{{Text: btnBack, Data: "menu"}})
		b.editOrSend(ctx, cb, txtChooseSection, kb)
	}
}

// showPlans lists what this operator sells in one section: only that
// section's plans, under a header that names it.
func (b *bot) showPlans(ctx context.Context, cb *tgCallback, sess *session, section string) {
	section = shop.NormalizeSection(section)
	plans, err := b.mgr.shop.PlansFor(ctx, b.cfg().OperatorID, true)
	if err != nil {
		b.log.Warn("shop bot could not read the plans", "error", err)
		b.editOrSend(ctx, cb, txtError, backKeyboard())
		return
	}
	var own []shop.Plan
	for i := range plans {
		if shop.NormalizeSection(plans[i].Section) == section {
			own = append(own, plans[i])
		}
	}
	if len(own) == 0 {
		b.editOrSend(ctx, cb, txtNoPlans, backKeyboard())
		return
	}
	sess.section = section
	kb := tgKeyboard{}
	for i := range own {
		kb = append(kb, []tgButton{{Text: planButtonText(&own[i]), Data: fmt.Sprintf("plan:%d", own[i].ID)}})
	}
	back := "menu"
	if sess.sectionChoice {
		back = "buy"
	}
	kb = append(kb, []tgButton{{Text: btnBack, Data: back}})
	b.editOrSend(ctx, cb, fmt.Sprintf(txtChoosePlanSection, sectionName(section)), kb)
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

// planPicked is a tap on a plan: a fresh checkout begins there --
// no quantity and no code carried over from whatever the buyer was
// pricing before, because a discount is priced against one basket
// and must never leak into another. The walk then forks: a purchase
// chooses how many first, a renewal goes straight to paying for the
// one plan it extends.
func (b *bot) planPicked(ctx context.Context, cb *tgCallback, sess *session, planID uint) {
	sess.planID = planID
	sess.qty = 0
	sess.code = ""
	sess.discount = 0
	if sess.renewing {
		b.showPayments(ctx, cb, sess, planID)
		return
	}
	b.showQuantity(ctx, cb, sess, planID)
}

// showQuantity asks how many of the plan the buyer wants: one button
// per count, each carrying the total for that many, so the price of
// the choice is on the choice itself.
func (b *bot) showQuantity(ctx context.Context, cb *tgCallback, sess *session, planID uint) {
	plan := b.plan(ctx, planID)
	if plan == nil {
		b.editOrSend(ctx, cb, txtOrderGone, backKeyboard())
		return
	}
	kb := tgKeyboard{}
	var row []tgButton
	for n := 1; n <= 5; n++ {
		row = append(row, tgButton{
			Text: fmt.Sprintf("%s — %s", faNum(int64(n)), formatPrice(plan.PriceToman*int64(n))),
			Data: fmt.Sprintf("qty:%d:%d", planID, n),
		})
		if len(row) == 3 {
			kb = append(kb, row)
			row = nil
		}
	}
	if len(row) > 0 {
		kb = append(kb, row)
	}
	kb = append(kb, []tgButton{{Text: btnBack, Data: "plans"}})
	b.editOrSend(ctx, cb, planButtonText(plan)+"\n\n"+txtChooseQty, kb)
}

// parseQtyCallback reads qty:<plan>:<count>. Two numbers ride in the
// one callback, which parseCallbackID cannot read, and the count is
// fenced to the buttons the bot itself offers.
func parseQtyCallback(data string) (planID uint, n int, ok bool) {
	parts := strings.Split(strings.TrimPrefix(data, "qty:"), ":")
	if len(parts) != 2 {
		return 0, 0, false
	}
	p, err1 := strconv.ParseUint(parts[0], 10, 64)
	c, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || c < 1 || c > 5 {
		return 0, 0, false
	}
	return uint(p), c, true
}

// checkoutTotals prices the session's basket against a plan: the
// total for the quantity chosen, and what is finally owed after the
// code named against it. Every order the checkout makes is priced
// here, so the card instructions, the gateway and the wallet debit
// can never disagree about what a sale costs.
func checkoutTotals(sess *session, plan *shop.Plan) (qty int, total, final int64) {
	qty = 1
	var discount int64
	if sess != nil {
		if sess.qty > 1 {
			qty = sess.qty
		}
		discount = sess.discount
	}
	total = plan.PriceToman * int64(qty)
	final = total - discount
	if final < 0 {
		final = 0
	}
	return qty, total, final
}

// paymentsScreen builds the payment screen for a checkout: the ways
// this plan can be paid for -- only the ones the operator has
// actually set up, plus the buyer's own wallet when it covers what
// is owed -- over the basket's arithmetic. ok is false when the plan
// is gone or no way of paying exists; the caller words the refusal.
func (b *bot) paymentsScreen(ctx context.Context, buyerID int64, sess *session, planID uint) (string, tgKeyboard, bool) {
	plan := b.plan(ctx, planID)
	if plan == nil {
		return "", nil, false
	}
	qty, total, final := checkoutTotals(sess, plan)

	var kb tgKeyboard
	methods := 0
	if b.cfg().CardNumber != "" {
		kb = append(kb, []tgButton{{Text: btnPayCard, Data: fmt.Sprintf("card:%d", planID)}})
		methods++
	}
	if b.zarinpalOffered(ctx) {
		kb = append(kb, []tgButton{{Text: btnPayLink, Data: fmt.Sprintf("zp:%d", planID)}})
		methods++
	}
	if final > 0 {
		w, err := b.mgr.shop.WalletFor(ctx, b.cfg().OperatorID, buyerID)
		if err != nil {
			b.log.Warn("shop bot could not read the buyer's wallet", "error", err)
		} else if w.BalanceToman >= final {
			kb = append(kb, []tgButton{{
				Text: fmt.Sprintf(txtPayWalletBtn, formatPrice(w.BalanceToman)),
				Data: fmt.Sprintf("wal:%d", planID),
			}})
			methods++
		}
	}
	if methods == 0 {
		return "", nil, false
	}
	if sess == nil || sess.code == "" {
		kb = append(kb, []tgButton{{Text: btnCode, Data: fmt.Sprintf("code:%d", planID)}})
	}
	kb = append(kb, []tgButton{{Text: btnBack, Data: "plans"}})

	text := txtChoosePay + "\n\n" + planButtonText(plan)
	if qty > 1 {
		text += "\n" + fmt.Sprintf(txtPayTotal, faNum(int64(qty)), formatPrice(total))
	}
	if sess != nil && sess.code != "" {
		text += "\n" + fmt.Sprintf(txtPayDiscount, formatPrice(sess.discount), formatPrice(final))
	}
	return text, kb, true
}

// showPayments offers the payment screen on the message the buyer
// tapped through, wording the two refusals -- the plan gone, no way
// of paying set up -- the way it always has.
func (b *bot) showPayments(ctx context.Context, cb *tgCallback, sess *session, planID uint) {
	if b.plan(ctx, planID) == nil {
		b.editOrSend(ctx, cb, txtOrderGone, backKeyboard())
		return
	}
	sess.planID = planID
	text, kb, ok := b.paymentsScreen(ctx, cb.From.ID, sess, planID)
	if !ok {
		b.editOrSend(ctx, cb, txtPayNotConfigured, backKeyboard())
		return
	}
	b.editOrSend(ctx, cb, text, kb)
}

// takeCode takes the discount code a buyer typed at the checkout
// prompt: priced against the basket's total by the shop -- which
// validates it and records nothing -- and, when it holds, stamped
// onto the session for the order the payment will make. A code that
// does not hold is refused in words, with the prompt left open: the
// buyer may type another, or go on without one.
func (b *bot) takeCode(ctx context.Context, m *tgMessage, sess *session) {
	chatID := m.Chat.ID
	code := strings.TrimSpace(m.Text)
	plan := b.plan(ctx, sess.planID)
	if plan == nil {
		sess.reset()
		b.send(ctx, chatID, txtOrderGone, b.menuKeyboard(ctx))
		return
	}
	if code == "" {
		b.send(ctx, chatID, txtCodeAsk, nil)
		return
	}
	_, total, _ := checkoutTotals(sess, plan)
	_, discount, err := b.mgr.shop.PriceWithCode(ctx, b.cfg().OperatorID, m.From.ID, code, total)
	if err != nil {
		b.log.Warn("shop bot could not price a buyer's code", "error", err)
		b.send(ctx, chatID, txtCodeInvalid, tgKeyboard{
			{{Text: btnSkipCode, Data: fmt.Sprintf("pay:%d", plan.ID)}},
		})
		return
	}
	sess.code = code
	sess.discount = discount
	sess.step = stepNone
	text, kb, ok := b.paymentsScreen(ctx, m.From.ID, sess, plan.ID)
	if !ok {
		b.send(ctx, chatID, txtPayNotConfigured, backKeyboard())
		return
	}
	b.send(ctx, chatID, text, kb)
}

// newOrder records a purchase at the moment the buyer commits to a way
// of paying, with the plan's terms copied onto it: what was sold is what
// the order says, even if the plan is edited tomorrow. The plan's
// section is copied with the rest, because the section is what delivery
// hands over -- the tunnels of that half of the shop, and no other.
//
// The checkout rides along with the terms: the quantity the buyer
// chose, the code they named, and the price as the two of them leave
// it -- the total for that many, less the code's discount. That final
// price is the order's PriceToman, so every way of paying charges the
// same number the buyer was shown.
//
// Kind is always "new" here, deliberately: whether this purchase creates
// a customer or renews the one the buyer already has is decided by
// shop.Fulfill, which finds the existing customer by Telegram id. The
// bot would have to read the operator's customers to guess earlier, and
// its guess could still be wrong by the time the money lands.
func (b *bot) newOrder(ctx context.Context, from *tgUser, plan *shop.Plan, sess *session, method, status string) (*shop.Order, error) {
	qty, _, final := checkoutTotals(sess, plan)
	o := &shop.Order{
		OperatorID:  b.cfg().OperatorID,
		BuyerTGID:   from.ID,
		BuyerName:   buyerName(from),
		PlanID:      plan.ID,
		PlanName:    plan.Name,
		Section:     shop.NormalizeSection(plan.Section),
		QuotaBytes:  plan.QuotaBytes,
		Days:        plan.Days,
		PriceToman:  final,
		DeviceLimit: plan.DeviceLimit,
		Kind:        "new",
		Method:      method,
		Status:      status,
	}
	if sess != nil {
		o.Quantity = qty
		o.Code = sess.code
		o.DiscountToman = sess.discount
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
	o, err := b.newOrder(ctx, cb.From, plan, sess, "card", "awaiting_receipt")
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
		b.send(ctx, chatID, txtOrderGone, b.menuKeyboard(ctx))
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
	b.send(ctx, chatID, txtReceiptReceived, b.menuKeyboard(ctx))

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

	// The panel keeps its own copy of the picture, so the seller can
	// approve from their page as well as from this chat, against the
	// same receipt. Strictly best effort: the seller already holds the
	// copy above, and a picture the panel could not fetch must not cost
	// the buyer the sale it belongs to.
	b.keepReceipt(ctx, m, o.ID)
}

// keepReceipt downloads the receipt photo the buyer just sent and files
// it with the shop for the panel. Every failure is a log line and
// nothing more -- see takeReceipt for why.
func (b *bot) keepReceipt(ctx context.Context, m *tgMessage, orderID uint) {
	fileID := m.photoFileID()
	if fileID == "" {
		return
	}
	path, err := b.api.getFile(ctx, fileID)
	if err != nil || path == "" {
		b.log.Warn("shop bot could not locate the receipt photo", "order", orderID, "error", err)
		return
	}
	data, err := b.api.downloadFile(ctx, path)
	if err != nil {
		b.log.Warn("shop bot could not download the receipt photo", "order", orderID, "error", err)
		return
	}
	if err := b.mgr.shop.SaveReceipt(ctx, orderID, receiptContentType(path), data); err != nil {
		b.log.Warn("shop bot could not keep the receipt for the panel", "order", orderID, "error", err)
	}
}

// receiptContentType names a downloaded receipt's kind from the name
// Telegram gave the file: camera photos arrive as JPEG, screenshots
// sometimes as PNG, and anything unrecognised is called JPEG -- the
// panel serves the bytes back under this name, and a picture named
// slightly wrong still opens.
func receiptContentType(path string) string {
	if strings.HasSuffix(strings.ToLower(path), ".png") {
		return "image/png"
	}
	return "image/jpeg"
}

// startZarinpalOrder opens an online payment and hands the buyer its
// page, with a button to say the money is sent.
func (b *bot) startZarinpalOrder(ctx context.Context, cb *tgCallback, sess *session, planID uint) {
	plan := b.plan(ctx, planID)
	if plan == nil {
		b.answer(ctx, cb, txtOrderGone)
		return
	}
	o, err := b.newOrder(ctx, cb.From, plan, sess, "zarinpal", "unpaid")
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
		b.send(ctx, o.BuyerTGID, txtRejectedBuyer, b.menuKeyboard(ctx))
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

// deliver fulfills an order and puts what it bought in the buyer's
// hands: a customer for a service order, money in the wallet for a
// top-up. Fulfillment is the shop's and is idempotent, so delivering
// twice — a seller's tap racing the buyer's — hands over the same
// subscription twice rather than making two.
func (b *bot) deliver(ctx context.Context, o *shop.Order) error {
	client, err := b.mgr.shop.Fulfill(ctx, o.ID)
	if err != nil {
		b.log.Warn("shop bot could not fulfill the order", "order", o.ID, "error", err)
		b.send(ctx, o.BuyerTGID, txtDeliverFailed, b.menuKeyboard(ctx))
		if admin := b.cfg().AdminChatID; admin != 0 {
			b.send(ctx, admin, fmt.Sprintf(txtDeliverFailedSeller,
				o.ID, err, escapeHTML(o.BuyerName), o.BuyerTGID), nil)
		}
		return err
	}

	// A top-up hands back no customer: what the buyer is owed is the
	// news that their wallet now holds the money.
	if client == nil {
		balance := int64(0)
		if w, werr := b.mgr.shop.WalletFor(ctx, o.OperatorID, o.BuyerTGID); werr == nil {
			balance = w.BalanceToman
		} else {
			b.log.Warn("shop bot could not read the topped-up wallet", "order", o.ID, "error", werr)
		}
		b.send(ctx, o.BuyerTGID, fmt.Sprintf(txtTopupDelivered,
			formatPrice(o.PriceToman), formatPrice(balance)), b.menuKeyboard(ctx))
		return nil
	}
	if o.Quantity > 1 {
		b.deliverBulk(ctx, o, client)
		return nil
	}
	b.deliverClient(ctx, o.BuyerTGID, o.BuyerName, client, o.PlanName, o.Section)
	return nil
}

// deliverClient puts one delivered customer in its buyer's hands:
// the link that opens it, and the section's own word about which
// apps speak it. A sale's delivery calls it, and so does the free
// trial -- what the buyer holds is the same either way, and so is
// what they are told.
func (b *bot) deliverClient(ctx context.Context, buyerID int64, buyerName string, client *model.Client, planName, section string) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(txtDelivered, escapeHTML(planName)))
	if client.ExpiresAt != nil {
		sb.WriteString("\n" + fmt.Sprintf(txtDeliveredUntil, formatDate(*client.ExpiresAt)))
	}
	if link := b.mgr.shop.SubLink(ctx, client); link != "" {
		sb.WriteString("\n\n" + fmt.Sprintf(txtDeliverLink, link))
		// What the link is opened with is the section's own business:
		// a WireGuard buyer is told which apps speak it, and a V2ray
		// buyer gets the direct bundle link as well, because that --
		// not the subscription page -- is what their apps import.
		if shop.NormalizeSection(section) == shop.SectionV2ray {
			sb.WriteString("\n\n" + fmt.Sprintf(txtDeliverV2rayLink, link+"?format=base64"))
		} else {
			sb.WriteString("\n\n" + txtDeliverWireGuardGuide)
		}
	} else {
		// No public address is configured, so no link can be built. The
		// buyer gets the token that identifies their subscription, and
		// the seller gets it too, with a note to send the link by hand —
		// a paid buyer must never be left holding nothing.
		sb.WriteString("\n\n" + fmt.Sprintf(txtDeliverToken, escapeHTML(client.SubToken)))
		if admin := b.cfg().AdminChatID; admin != 0 {
			b.send(ctx, admin, fmt.Sprintf(txtSellerLinkNote,
				escapeHTML(buyerName), escapeHTML(client.SubToken)), nil)
		}
	}
	b.send(ctx, buyerID, sb.String(), b.menuKeyboard(ctx))
}

// deliverBulk is a bulk order's delivery: every customer the order
// made, numbered, each with its own link -- one message a buyer can
// hand one service each to the people they bought them for. The
// customers are gathered from the buyer's own under this operator,
// newest first, as many as the order bought: the units stand beside
// each other under the buyer's Telegram id, which is where a bulk
// delivery has to look for them.
func (b *bot) deliverBulk(ctx context.Context, o *shop.Order, first *model.Client) {
	customers := []*model.Client{first}
	if opCtx, err := b.mgr.shop.OperatorCtx(ctx, o.OperatorID); err != nil {
		b.log.Warn("shop bot could not build the operator scope", "error", err)
	} else if list, err := b.mgr.clients.ByTelegramID(opCtx, o.BuyerTGID); err != nil {
		b.log.Warn("shop bot could not gather the bulk delivery", "error", err)
	} else if len(list) > 0 {
		sort.Slice(list, func(i, j int) bool { return list[i].ID > list[j].ID })
		customers = customers[:0]
		for i := range list {
			if len(customers) >= o.Quantity {
				break
			}
			customers = append(customers, &list[i])
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(txtDeliveredBulk, escapeHTML(o.PlanName), faNum(int64(len(customers)))))
	if first.ExpiresAt != nil {
		sb.WriteString("\n" + fmt.Sprintf(txtDeliveredUntil, formatDate(*first.ExpiresAt)))
	}
	noLink := false
	for i, c := range customers {
		num := faNum(int64(i + 1))
		if link := b.mgr.shop.SubLink(ctx, c); link != "" {
			sb.WriteString(fmt.Sprintf(txtDeliverBulkOne, num, escapeHTML(c.Name), link))
		} else {
			noLink = true
			sb.WriteString(fmt.Sprintf(txtDeliverBulkTok, num, escapeHTML(c.Name), escapeHTML(c.SubToken)))
		}
	}
	if !noLink {
		if shop.NormalizeSection(o.Section) == shop.SectionV2ray {
			sb.WriteString(txtDeliverBulkV2)
		} else {
			sb.WriteString("\n\n" + txtDeliverWireGuardGuide)
		}
	} else if admin := b.cfg().AdminChatID; admin != 0 {
		// As with a single delivery: no public address, no links --
		// the seller is told, with the first token, to send them by
		// hand rather than leave a paid buyer holding nothing.
		b.send(ctx, admin, fmt.Sprintf(txtSellerLinkNote,
			escapeHTML(o.BuyerName), escapeHTML(first.SubToken)), nil)
	}
	b.send(ctx, o.BuyerTGID, sb.String(), b.menuKeyboard(ctx))
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
			// One block per section the customer actually holds, each
			// with the link that section is used through: a customer
			// who bought both halves sees both, named, instead of one
			// link and a guess about which app it belongs in. A
			// customer holding neither -- an account on some other
			// kind of tunnel -- gets the plain link, as before.
			wg, xray, serr := b.mgr.shop.ClientSections(ctx, c)
			if serr != nil {
				b.log.Warn("shop bot could not read the customer's sections", "error", serr)
			}
			switch {
			case serr == nil && wg:
				block += "\n" + fmt.Sprintf(txtMineWireGuard, link)
				if xray {
					block += "\n" + fmt.Sprintf(txtMineV2ray, link+"?format=base64")
				}
			case serr == nil && xray:
				block += "\n" + fmt.Sprintf(txtMineV2ray, link+"?format=base64")
			default:
				block += "\n" + fmt.Sprintf(txtMineLink, link)
			}
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
		{{Text: btnRenew, Data: "renew"}},
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
		b.send(ctx, chatID, txtSupportOff, b.menuKeyboard(ctx))
		return
	}
	b.send(ctx, admin, fmt.Sprintf(txtSupportFrom, escapeHTML(buyerName(m.From)), m.From.ID), nil)
	if copied, err := b.api.copyMessage(ctx, admin, chatID, m.ID, "", nil); err != nil {
		b.log.Warn("shop bot could not forward the support message", "error", err)
	} else {
		b.relay[copied.ID] = chatID
	}
	b.send(ctx, chatID, txtSupportSent, b.menuKeyboard(ctx))
}
