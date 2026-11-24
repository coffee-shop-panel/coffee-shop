package shopbot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/service"
)

// The reminders: the bot speaks first, before a customer's plan
// runs out under them.
//
// A scan walks every running bot's customers -- under that
// operator's own scope, the way the admin bot lists them, so a
// customer is only ever reminded by their own seller's bot -- and
// sends the reminders that have come due: the plan ending within
// three days, within one day, ended, or nine-tenths spent. What
// stops a reminder repeating every half hour is the notices table
// (internal/shop): a reminder is recorded against the occasion it
// belongs to -- the plan's end date, the quota's size -- and a scan
// asks before it speaks. A renewed plan is a new occasion and is
// reminded afresh; the same occasion never is.

// The reminder kinds, as the notices table stores them.
const (
	noticeExpiry3  = "expiry3"
	noticeExpiry1  = "expiry1"
	noticeExpired  = "expired"
	noticeVolume90 = "volume90"
)

// reminderInterval is how often the scan runs: often enough that a
// plan entering its last day is caught the day it enters it, seldom
// enough that the walk is never the panel's load.
const reminderInterval = 30 * time.Minute

// reminderLoop is the scan's life: a ticker on the manager's own
// context, started by Start and ended when that context ends --
// the manager has no other stop, and the loop needs none of its own.
func (m *Manager) reminderLoop(ctx context.Context) {
	t := time.NewTicker(reminderInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.scanReminders(ctx, now)
		}
	}
}

// scanReminders is one pass over every running bot's customers.
// now is the caller's clock, handed in rather than read, so a test
// can stand the scan at the moment it means to exercise.
func (m *Manager) scanReminders(ctx context.Context, now time.Time) {
	m.mu.Lock()
	bots := make([]*bot, 0, len(m.bots))
	for _, b := range m.bots {
		bots = append(bots, b)
	}
	m.mu.Unlock()
	for _, b := range bots {
		b.scanBuyerReminders(ctx, now)
	}
}

// scanBuyerReminders walks this bot's operator's customers a page
// at a time -- the admin bot's own walk -- and reminds each one
// whose plan has come due. A page that fails stops this bot's pass
// rather than the fleet's: the next tick walks it again.
func (b *bot) scanBuyerReminders(ctx context.Context, now time.Time) {
	cfg := b.cfg()
	opCtx, err := b.mgr.shop.OperatorCtx(ctx, cfg.OperatorID)
	if err != nil {
		b.log.Warn("shop bot reminders could not build the operator scope", "error", err)
		return
	}
	for page := 1; ; page++ {
		p, err := b.mgr.clients.List(opCtx, service.ListFilter{Page: page, PerPage: 200})
		if err != nil {
			b.log.Warn("shop bot reminders could not list the customers", "error", err)
			return
		}
		for i := range p.Items {
			b.remindCustomer(ctx, cfg.OperatorID, &p.Items[i], now)
		}
		if int64(page)*int64(p.PerPage) >= p.Total {
			return
		}
	}
}

// remindCustomer works out which reminders one customer has come
// due and sends the ones not yet sent. Only a customer the bot can
// reach -- one carrying a Telegram id -- is reminded at all.
func (b *bot) remindCustomer(ctx context.Context, operatorID uint, c *model.Client, now time.Time) {
	if c.TelegramID == 0 {
		return
	}
	if c.ExpiresAt != nil {
		exp := *c.ExpiresAt
		// The stamp is the ending itself: renew the plan and the
		// date moves, and with it the reminders start over.
		stamp := exp.UTC().Format("2006-01-02")
		switch {
		case !exp.After(now):
			// Ended. Only a customer whose standing says the time
			// ran out -- expired, or switched off -- is told so; a
			// date in the past on a customer standing otherwise is
			// the reconciler's business, not a message to send.
			if c.Status == model.StatusExpired || c.Status == model.StatusDisabled {
				b.sendNotice(ctx, operatorID, c, noticeExpired, stamp, txtNoticeExpired)
			}
		case c.Status == model.StatusActive && exp.Sub(now) <= 24*time.Hour:
			b.sendNotice(ctx, operatorID, c, noticeExpiry1, stamp,
				fmt.Sprintf(txtNoticeExpiring, formatDate(exp), daysLeftText(exp, now)))
		case c.Status == model.StatusActive && exp.Sub(now) <= 72*time.Hour:
			b.sendNotice(ctx, operatorID, c, noticeExpiry3, stamp,
				fmt.Sprintf(txtNoticeExpiring, formatDate(exp), daysLeftText(exp, now)))
		}
	}
	if c.QuotaBytes > 0 && c.UsedBytes*10 >= c.QuotaBytes*9 {
		// Nine-tenths spent. The stamp is the quota itself: a plan
		// renewed to a larger allowance is a new occasion, and the
		// reminder may come again when that one runs low.
		b.sendNotice(ctx, operatorID, c, noticeVolume90,
			strconv.FormatUint(c.QuotaBytes, 10),
			fmt.Sprintf(txtNoticeVolume, formatBytes(c.UsedBytes), formatBytes(c.QuotaBytes)))
	}
}

// sendNotice sends one reminder, once: the notices table is asked
// first, and written after the send. A chat Telegram refuses
// outright -- the buyer blocked the bot, or their account is gone
// -- is recorded anyway: that chat will refuse every scan alike,
// and re-asking it every half hour helps nobody. Any other failure
// leaves the reminder unrecorded, so the next tick tries again.
func (b *bot) sendNotice(ctx context.Context, operatorID uint, c *model.Client, kind, stamp, text string) {
	sent, err := b.mgr.shop.NoticeSent(ctx, operatorID, c.ID, kind, stamp)
	if err != nil {
		b.log.Warn("shop bot could not read the notices", "error", err)
		return
	}
	if sent {
		return
	}
	kb := tgKeyboard{{{Text: btnNoticeBuy, Data: "buy"}}}
	if _, err := b.api.sendMessage(ctx, c.TelegramID, text, kb); err != nil {
		if !isChatGone(err) {
			b.log.Warn("shop bot could not send a reminder",
				"client", c.ID, "kind", kind, "error", err)
			return
		}
		b.log.Info("shop bot reminder reached a dead chat; recording it as sent",
			"client", c.ID, "kind", kind)
	}
	if err := b.mgr.shop.RecordNotice(ctx, operatorID, c.ID, kind, stamp); err != nil {
		b.log.Warn("shop bot could not record the reminder", "client", c.ID, "error", err)
	}
}

// isChatGone reads a Telegram refusal as the permanent kind: the
// chat cannot be reached now or ever -- the bot blocked, the
// account deactivated, the chat never existed. Anything else
// (a timeout, a rate limit) may pass, and is retried next tick.
func isChatGone(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "Forbidden") ||
		strings.Contains(msg, "blocked") ||
		strings.Contains(msg, "deactivated") ||
		strings.Contains(msg, "chat not found")
}
