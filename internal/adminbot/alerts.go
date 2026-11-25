package adminbot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/service"
)

// The alerts: this bot speaks first, to its own operator.
//
// The sales bots remind a buyer that their plan is ending; this is
// the same courtesy turned around. A reseller's customers are theirs
// to keep, and a plan that lapses unnoticed is a customer lost
// quietly -- so each operator's management bot watches its own book
// and tells the operator, in their own chat, whose plan is ending
// within three days, within one day, has ended, or is nine-tenths
// spent. The scan walks each running bot's customers under that
// operator's scope, the way the bot itself lists them, so an alert
// only ever names a customer to the operator they belong to.
//
// What stops an alert repeating every half hour is the notices
// table the shop keeps (internal/shop), shared on purpose: an alert
// is recorded against the occasion it belongs to -- the plan's end
// date, the quota's size -- under its own kinds, apart from the
// buyer reminders' kinds, because the two messages go to different
// people about the same ending and neither may stand in for the
// other. A renewed plan is a new occasion and is alerted afresh;
// the same occasion never is.

// The alert kinds, as the notices table stores them. The buyer
// reminders' kinds (expiry3, expiry1, expired, volume90) are the
// same occasions told to the buyer; these are told to the seller.
const (
	alertExpiry3  = "op_expiry3"
	alertExpiry1  = "op_expiry1"
	alertExpired  = "op_expired"
	alertVolume90 = "op_volume90"
)

// alertInterval is how often the scan runs, on the sales bots'
// reminder cadence: often enough that a plan entering its last day
// is caught the day it enters it, seldom enough that the walk is
// never the panel's load.
const alertInterval = 30 * time.Minute

// alertLoop is the scan's life: a ticker on the manager's own
// context, started by Start and ended when that context ends --
// the manager has no other stop, and the loop needs none of its own.
func (m *Manager) alertLoop(ctx context.Context) {
	t := time.NewTicker(alertInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			m.scanAlerts(ctx, now)
		}
	}
}

// scanAlerts is one pass over every running bot's customers. The
// fleet is read fresh each pass, so a bot started by a later save
// joins the watch on the next tick without anything rewiring it.
func (m *Manager) scanAlerts(ctx context.Context, now time.Time) {
	m.mu.Lock()
	bots := make([]*bot, 0, len(m.bots))
	for _, b := range m.bots {
		bots = append(bots, b)
	}
	m.mu.Unlock()
	for _, b := range bots {
		b.scanCustomerAlerts(ctx, now)
	}
}

// scanCustomerAlerts walks this bot's operator's customers a page
// at a time -- the bot's own walk -- and alerts the operator about
// each one whose plan has come due. A bot with no chat named has
// nobody to tell: until the operator names themselves the guard
// answers no one, and an alert has no more right to a chat than an
// order does. A page that fails stops this bot's pass rather than
// the fleet's: the next tick walks it again.
func (b *bot) scanCustomerAlerts(ctx context.Context, now time.Time) {
	cfg := b.cfg()
	if cfg.AdminChatID == 0 || b.mgr.shop == nil || b.mgr.clients == nil {
		return
	}
	opCtx, err := b.opCtx(ctx)
	if err != nil {
		b.log.Warn("management bot alerts could not build the operator scope", "error", err)
		return
	}
	for page := 1; ; page++ {
		p, err := b.mgr.clients.List(opCtx, service.ListFilter{Page: page, PerPage: 200})
		if err != nil {
			b.log.Warn("management bot alerts could not list the customers", "error", err)
			return
		}
		for i := range p.Items {
			b.alertCustomer(ctx, cfg, &p.Items[i], now)
		}
		if int64(page)*int64(p.PerPage) >= p.Total {
			return
		}
	}
}

// alertCustomer works out which alerts one customer has come due
// and sends the ones not yet sent. The occasions and their bands
// are the buyer reminders' own -- the same ending, told here to
// the seller instead of the buyer.
func (b *bot) alertCustomer(ctx context.Context, cfg Config, c *model.Client, now time.Time) {
	if c.ExpiresAt != nil {
		exp := *c.ExpiresAt
		// The stamp is the ending itself: renew the plan and the
		// date moves, and with it the alerts start over.
		stamp := exp.UTC().Format("2006-01-02")
		name := escapeHTML(c.Name)
		switch {
		case !exp.After(now):
			// Ended. Only a customer whose standing says the time
			// ran out -- expired, or switched off -- is named; a
			// date in the past on a customer standing otherwise is
			// the reconciler's business, not an alert to send.
			if c.Status == model.StatusExpired || c.Status == model.StatusDisabled {
				b.sendAlert(ctx, cfg, c, alertExpired, stamp,
					fmt.Sprintf(txtAlertExpired, name, formatDate(exp)))
			}
		case c.Status == model.StatusActive && exp.Sub(now) <= 24*time.Hour:
			b.sendAlert(ctx, cfg, c, alertExpiry1, stamp,
				fmt.Sprintf(txtAlertExpiring, name, daysLeftText(exp, now), formatDate(exp)))
		case c.Status == model.StatusActive && exp.Sub(now) <= 72*time.Hour:
			b.sendAlert(ctx, cfg, c, alertExpiry3, stamp,
				fmt.Sprintf(txtAlertExpiring, name, daysLeftText(exp, now), formatDate(exp)))
		}
	}
	if c.QuotaBytes > 0 && c.UsedBytes*10 >= c.QuotaBytes*9 {
		// Nine-tenths spent. The stamp is the quota itself: a plan
		// renewed to a larger allowance is a new occasion, and the
		// alert may come again when that one runs low.
		pct := c.UsedBytes * 100 / c.QuotaBytes
		b.sendAlert(ctx, cfg, c, alertVolume90,
			strconv.FormatUint(c.QuotaBytes, 10),
			fmt.Sprintf(txtAlertVolume, escapeHTML(c.Name),
				formatBytes(c.UsedBytes), formatBytes(c.QuotaBytes), faNum(int64(pct))))
	}
}

// sendAlert sends one alert, once: the notices table is asked
// first, and written after the send. A chat Telegram refuses
// outright -- the operator blocked their own bot, or their account
// is gone -- is recorded anyway: that chat will refuse every scan
// alike, and re-asking it every half hour helps nobody. Any other
// failure leaves the alert unrecorded, so the next tick tries again.
func (b *bot) sendAlert(ctx context.Context, cfg Config, c *model.Client, kind, stamp, text string) {
	sent, err := b.mgr.shop.NoticeSent(ctx, cfg.OperatorID, c.ID, kind, stamp)
	if err != nil {
		b.log.Warn("management bot could not read the notices", "error", err)
		return
	}
	if sent {
		return
	}
	if _, err := b.api.sendMessage(ctx, cfg.AdminChatID, text, nil); err != nil {
		if !isChatGone(err) {
			b.log.Warn("management bot could not send an alert",
				"client", c.ID, "kind", kind, "error", err)
			return
		}
		b.log.Info("management bot alert reached a dead chat; recording it as sent",
			"client", c.ID, "kind", kind)
	}
	if err := b.mgr.shop.RecordNotice(ctx, cfg.OperatorID, c.ID, kind, stamp); err != nil {
		b.log.Warn("management bot could not record the alert", "client", c.ID, "error", err)
	}
}

// isChatGone reads a Telegram refusal as the permanent kind: the
// chat cannot be reached now or ever -- the bot blocked, the
// account deactivated, the chat never existed. Anything else
// (a timeout, a rate limit) may pass, and is retried next tick.
// The sales bots' client reads refusals the same way; each family
// keeps its own copy, as it keeps its own client.
func isChatGone(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "Forbidden") ||
		strings.Contains(msg, "blocked") ||
		strings.Contains(msg, "deactivated") ||
		strings.Contains(msg, "chat not found")
}
