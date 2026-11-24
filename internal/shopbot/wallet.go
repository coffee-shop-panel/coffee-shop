package shopbot

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/abolfazl/w-ui/internal/shop"
)

// The buyer's wallet and the doors that hang off it: paying for a
// plan out of the balance, topping the balance up through the shop's
// ordinary payment paths, redeeming a gift code into it, and the
// invitation link whose cashback lands in it. The money itself is
// the shop's (internal/shop); this file is only the conversation.

// walletBackKeyboard is the way back from a wallet prompt to the
// wallet screen the prompt hangs off.
func walletBackKeyboard() tgKeyboard {
	return tgKeyboard{{{Text: btnBack, Data: "wallet"}}}
}

// walletScreen is the buyer's wallet: what it holds, and the three
// things it is for.
func (b *bot) walletScreen(ctx context.Context, buyerID int64) (string, tgKeyboard) {
	balance := int64(0)
	if w, err := b.mgr.shop.WalletFor(ctx, b.cfg().OperatorID, buyerID); err != nil {
		b.log.Warn("shop bot could not read the buyer's wallet", "error", err)
	} else {
		balance = w.BalanceToman
	}
	return fmt.Sprintf(txtWalletTitle, formatPrice(balance)), tgKeyboard{
		{{Text: btnTopup, Data: "topup"}},
		{{Text: btnGift, Data: "gift"}, {Text: btnRefer, Data: "refer"}},
		{{Text: btnBack, Data: "menu"}},
	}
}

// topupAmounts are the one-tap top-up sizes, in Toman: the amounts a
// buyer charges a wallet with are round because the plans they will
// spend it on are priced round.
var topupAmounts = []int64{50_000, 100_000, 200_000}

func topupAmountKeyboard() tgKeyboard {
	kb := tgKeyboard{}
	for _, amount := range topupAmounts {
		kb = append(kb, []tgButton{{
			Text: formatPrice(amount),
			Data: fmt.Sprintf("top:%d", amount),
		}})
	}
	kb = append(kb,
		[]tgButton{{Text: btnCustom, Data: "topcustom"}},
		[]tgButton{{Text: btnBack, Data: "wallet"}},
	)
	return kb
}

// topupPayScreen offers the ways a top-up can be paid for: the same
// two the shop takes for a plan, because a top-up order rides them
// unchanged -- a card top-up is a receipt the seller approves, an
// online one is a gateway payment the callback settles, and delivery
// is the credit either way.
func (b *bot) topupPayScreen(ctx context.Context, amount int64) (string, tgKeyboard) {
	var kb tgKeyboard
	if b.cfg().CardNumber != "" {
		kb = append(kb, []tgButton{{Text: btnPayCard, Data: fmt.Sprintf("topcard:%d", amount)}})
	}
	if b.zarinpalOffered(ctx) {
		kb = append(kb, []tgButton{{Text: btnPayLink, Data: fmt.Sprintf("topzp:%d", amount)}})
	}
	if len(kb) == 0 {
		return txtPayNotConfigured, walletBackKeyboard()
	}
	kb = append(kb, []tgButton{{Text: btnBack, Data: "topup"}})
	return fmt.Sprintf(txtTopupPayChoose, formatPrice(amount)), kb
}

// newTopupOrder records a top-up at the moment the buyer commits to
// a way of paying: the amount is the order's price, and delivery --
// the shop's Fulfill -- turns exactly that into wallet balance.
func (b *bot) newTopupOrder(ctx context.Context, from *tgUser, amount int64, method, status string) (*shop.Order, error) {
	o := &shop.Order{
		OperatorID: b.cfg().OperatorID,
		BuyerTGID:  from.ID,
		BuyerName:  buyerName(from),
		PlanName:   txtTopupPlanName,
		PriceToman: amount,
		Kind:       shop.KindTopup,
		Method:     method,
		Status:     status,
	}
	if err := b.mgr.shop.CreateOrder(ctx, o); err != nil {
		b.log.Warn("shop bot could not create the top-up order", "error", err)
		return nil, err
	}
	return o, nil
}

// startTopupCard opens a card top-up: from here it is the ordinary
// card road -- the receipt step is armed on this order, the seller's
// approval delivers, and delivery is the credit.
func (b *bot) startTopupCard(ctx context.Context, cb *tgCallback, sess *session, amount int64) {
	o, err := b.newTopupOrder(ctx, cb.From, amount, shop.MethodCard, shop.StatusAwaitingReceipt)
	if err != nil {
		_ = b.api.answerCallbackQuery(ctx, cb.ID, txtOrderFailed, true)
		return
	}
	b.answer(ctx, cb, "")
	sess.step = stepReceipt
	sess.orderID = o.ID
	cfg := b.cfg()
	text := fmt.Sprintf(txtCardInstructions,
		escapeHTML(cfg.CardNumber), escapeHTML(cfg.CardHolder), formatPrice(o.PriceToman))
	b.editOrSend(ctx, cb, text, backKeyboard())
}

// startTopupZarinpal opens an online top-up: the ordinary gateway
// road, whose callback finds this order by its public token and
// whose delivery is the credit.
func (b *bot) startTopupZarinpal(ctx context.Context, cb *tgCallback, sess *session, amount int64) {
	o, err := b.newTopupOrder(ctx, cb.From, amount, shop.MethodZarinpal, shop.StatusUnpaid)
	if err != nil {
		_ = b.api.answerCallbackQuery(ctx, cb.ID, txtOrderFailed, true)
		return
	}
	base, baseErr := b.mgr.shop.PublicBase(ctx)
	if o.PublicToken == "" || baseErr != nil {
		b.log.Warn("shop bot could not build the payment callback", "error", baseErr)
		_ = b.api.answerCallbackQuery(ctx, cb.ID, txtZarinpalFailed, true)
		return
	}
	callback := strings.TrimRight(base, "/") + "/shop/callback/" + o.PublicToken
	authority, err := RequestPayment(ctx, b.cfg().ZarinpalMerchant,
		o.PriceToman, txtTopupPlanName, callback)
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

// takeTopupAmount takes the custom amount a buyer typed: digits in
// either alphabet, separators forgiven, and anything else refused in
// words with the prompt left open.
func (b *bot) takeTopupAmount(ctx context.Context, m *tgMessage, sess *session) {
	amount, ok := parseTomanAmount(m.Text)
	if !ok || amount <= 0 {
		b.send(ctx, m.Chat.ID, txtTopupBadAmount, nil)
		return
	}
	sess.step = stepNone
	text, kb := b.topupPayScreen(ctx, amount)
	b.send(ctx, m.Chat.ID, text, kb)
}

// parseTomanAmount reads an amount a buyer typed: Persian and Arabic
// digits become Latin ones, thousand separators and spaces fall away,
// and what remains must be a plain number.
func parseTomanAmount(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= '۰' && r <= '۹': // Persian digits
			b.WriteRune('0' + (r - '۰'))
		case r >= '٠' && r <= '٩': // Arabic digits
			b.WriteRune('0' + (r - '٠'))
		case r == '٬' || r == ',' || r == ' ':
			// a separator: forgiven, not read
		default:
			return 0, false
		}
	}
	if b.Len() == 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(b.String(), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// startWalletOrder is a payment out of the buyer's own balance: the
// one way of paying that needs nobody's approval and no receipt,
// because the money is already the shop's to hold. The debit is the
// payment -- atomic in the shop, refusing what the balance does not
// cover -- and only a paid order is delivered. A delivery that fails
// after the debit puts the money back and marks the order failed:
// wallet money is refundable in a way a card transfer is not, and a
// buyer must never pay for a delivery that did not happen.
func (b *bot) startWalletOrder(ctx context.Context, cb *tgCallback, sess *session, planID uint) {
	plan := b.plan(ctx, planID)
	if plan == nil {
		b.answer(ctx, cb, txtOrderGone)
		return
	}
	o, err := b.newOrder(ctx, cb.From, plan, sess, shop.MethodWallet, shop.StatusUnpaid)
	if err != nil {
		_ = b.api.answerCallbackQuery(ctx, cb.ID, txtOrderFailed, true)
		return
	}
	if err := b.mgr.shop.DebitWallet(ctx, o.OperatorID, cb.From.ID, o.PriceToman); err != nil {
		// The balance moved between the button being drawn and being
		// tapped. The order is closed as expired -- it was never paid
		// for -- and the buyer is put back on the payment screen,
		// where the wallet button will have gone if it no longer
		// covers the price.
		b.log.Warn("shop bot could not debit the buyer's wallet", "order", o.ID, "error", err)
		o.Status = shop.StatusExpired
		if uerr := b.mgr.shop.UpdateOrder(ctx, o); uerr != nil {
			b.log.Warn("shop bot could not close the unpaid wallet order", "order", o.ID, "error", uerr)
		}
		_ = b.api.answerCallbackQuery(ctx, cb.ID, txtWalletPayFailed, true)
		if text, kb, ok := b.paymentsScreen(ctx, cb.From.ID, sess, planID); ok {
			b.editOrSend(ctx, cb, text, kb)
		}
		return
	}
	o.Status = shop.StatusPaid
	if err := b.mgr.shop.UpdateOrder(ctx, o); err != nil {
		b.log.Warn("shop bot could not mark the wallet order paid", "order", o.ID, "error", err)
	}
	b.answer(ctx, cb, "")
	if err := b.deliver(ctx, o); err != nil {
		if rerr := b.mgr.shop.CreditWallet(ctx, o.OperatorID, cb.From.ID, o.PriceToman); rerr != nil {
			b.log.Error("shop bot could not refund a failed wallet delivery",
				"order", o.ID, "error", rerr)
		}
		if fresh, ferr := b.mgr.shop.OrderByID(ctx, o.ID); ferr == nil {
			fresh.Status = shop.StatusFailed
			if uerr := b.mgr.shop.UpdateOrder(ctx, fresh); uerr != nil {
				b.log.Warn("shop bot could not mark the wallet order failed", "order", o.ID, "error", uerr)
			}
		}
		b.send(ctx, cb.From.ID, txtWalletRefunded, b.menuKeyboard(ctx))
	}
}

// takeGift takes the gift code a buyer typed: the shop redeems it --
// the use written, the money landed -- and the buyer hears what
// arrived and what their wallet now holds. A code that does not
// redeem is refused in words with the prompt left open.
func (b *bot) takeGift(ctx context.Context, m *tgMessage, sess *session) {
	code := strings.TrimSpace(m.Text)
	if code == "" {
		b.send(ctx, m.Chat.ID, txtGiftAsk, nil)
		return
	}
	amount, err := b.mgr.shop.RedeemGift(ctx, b.cfg().OperatorID, m.From.ID, code)
	if err != nil {
		b.log.Warn("shop bot could not redeem a gift code", "error", err)
		b.send(ctx, m.Chat.ID, txtGiftInvalid, nil)
		return
	}
	sess.reset()
	balance := int64(0)
	if w, werr := b.mgr.shop.WalletFor(ctx, b.cfg().OperatorID, m.From.ID); werr == nil {
		balance = w.BalanceToman
	}
	b.send(ctx, m.Chat.ID, fmt.Sprintf(txtGiftOK,
		formatPrice(amount), formatPrice(balance)), b.menuKeyboard(ctx))
}

// referralScreen is the invitation: the buyer's own link into this
// bot, how many have arrived through it, and -- in a shop that pays
// it -- the cashback their purchases earn the inviter.
func (b *bot) referralScreen(ctx context.Context, buyerID int64) (string, tgKeyboard) {
	cfg := b.cfg()
	username, _ := b.username.Load().(string)
	link := fmt.Sprintf("https://t.me/%s?start=ref_%d", username, buyerID)
	n, err := b.mgr.shop.ReferralCount(ctx, cfg.OperatorID, buyerID)
	if err != nil {
		b.log.Warn("shop bot could not count a buyer's referrals", "error", err)
		n = 0
	}
	text := fmt.Sprintf(txtReferTitle, link, faNum(int64(n)))
	if cfg.CashbackPercent > 0 {
		text += fmt.Sprintf(txtReferCashback, faNum(int64(cfg.CashbackPercent)))
	}
	return text, backKeyboard()
}

// claimTrial is the free trial's tap: the shop gives it -- once per
// buyer, ever -- and what the buyer holds is delivered the way a
// sale's delivery is, because it is the same customer in the same
// hands. A refusal is said plainly, and the likeliest one -- this
// buyer has had theirs -- is told from the others.
func (b *bot) claimTrial(ctx context.Context, cb *tgCallback) {
	cfg := b.cfg()
	client, err := b.mgr.shop.ClaimTrial(ctx, cfg.OperatorID, cb.From.ID, buyerName(cb.From))
	if err != nil {
		b.log.Warn("shop bot could not give a trial", "buyer", cb.From.ID, "error", err)
		text := txtTrialFailed
		if w, werr := b.mgr.shop.WalletFor(ctx, cfg.OperatorID, cb.From.ID); werr == nil && w.TrialUsedAt != nil {
			text = txtTrialUsed
		}
		b.answer(ctx, cb, "")
		b.send(ctx, cb.From.ID, text, b.menuKeyboard(ctx))
		return
	}
	b.answer(ctx, cb, "")
	b.deliverClient(ctx, cb.From.ID, buyerName(cb.From), client, txtTrialName, cfg.TrialSection)
}

// sendLong sends a text longer than one message allows -- the
// seller's tutorial and FAQ are their own words, at their own
// length -- in plain chunks under Telegram's ceiling. The text is
// split before it is escaped, so a chunk can never cut an escape in
// half.
func (b *bot) sendLong(ctx context.Context, chatID int64, text string) {
	const chunk = 4000
	runes := []rune(strings.TrimSpace(text))
	for len(runes) > chunk {
		b.send(ctx, chatID, escapeHTML(string(runes[:chunk])), nil)
		runes = runes[chunk:]
	}
	if len(runes) > 0 {
		b.send(ctx, chatID, escapeHTML(string(runes)), nil)
	}
}
