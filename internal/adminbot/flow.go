package adminbot

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/service"
)

// The conversation: the operator's whole panel work with their own
// customers, as a Telegram conversation — the list, one customer's
// detail and everything that can be done to them there, the add
// wizard, a search, and the operator's own totals.
//
// Two rules hold the whole file together. The first is the guard in
// handleMessage and handleCallback: nobody but the named owner gets
// past it, ever, because this bot can do everything its operator can.
// The second is that every service call runs under b.opCtx — the
// operator's own scope, the one their sales are delivered under — so a
// customer the bot shows, extends or cuts off is fenced exactly as if
// the operator had done it in the panel, and a customer outside that
// fence is simply not there. Interface rows are read from the database
// directly only to name them on screen; no customer is ever touched
// that way.

// listPageSize is how many customers one list screen holds: enough to
// find someone by eye, few enough to stay one thumb's reach tall.
const listPageSize = 8

// awaitKind is what a plain text message currently means. Most of the
// time it means nothing — the operator is pressing buttons — but the
// wizard's name step, the custom amounts and the search are all a
// question the bot asked and a text answer it is waiting for.
type awaitKind int

const (
	awaitNone awaitKind = iota
	awaitWizardName
	awaitWizardQuota
	awaitWizardDays
	awaitSearch
	awaitExtendDays
	awaitAddQuota
)

// session is one operator's place in the conversation: the question a
// text message would answer, the add wizard's half-filled form, and
// where the list and detail screens were left. In memory only — a bot
// restart costs the operator one tap on /start, and in exchange no
// half-finished conversation is ever written anywhere durable.
type session struct {
	await awaitKind

	// The add wizard's draft.
	name    string
	picked  map[uint]bool
	quota   uint64
	days    int
	devices int

	// Where the operator is standing: the list page on screen, and
	// the customer whose detail is open — the page the detail's back
	// button returns to kept beside it, because the operator came
	// from there and not from wherever the list sits now.
	listPage   int
	detailID   uint
	detailPage int
}

func (s *session) reset() { *s = session{listPage: 1} }

// startWizard clears the draft for a fresh run through the wizard.
func (s *session) startWizard() {
	s.name = ""
	s.picked = map[uint]bool{}
	s.quota, s.days, s.devices = 0, 0, 0
}

// sessions holds the conversations, one per bot. The bot struct is
// the lifecycle's and carries no conversation state, so the state
// lives here, keyed by the bot it belongs to: a bot restarted for a
// new token starts a fresh conversation, which is what its operator
// expects of it anyway.
var sessions = struct {
	sync.Mutex
	byBot map[*bot]*session
}{byBot: map[*bot]*session{}}

func (b *bot) session() *session {
	sessions.Lock()
	defer sessions.Unlock()
	s, ok := sessions.byBot[b]
	if !ok {
		s = &session{listPage: 1}
		sessions.byBot[b] = s
	}
	return s
}

// handle routes one update. It runs on the bot's own goroutine.
func (b *bot) handle(ctx context.Context, u tgUpdate) {
	switch {
	case u.Callback != nil:
		b.handleCallback(ctx, u.Callback)
	case u.Message != nil:
		b.handleMessage(ctx, u.Message)
	}
}

// isOwner is the guard's whole question: is this the operator the
// config names? A config that names nobody (AdminChatID zero — the bot
// switched on before its owner said who they are) has no owner, and
// every chat is a stranger to it.
func (b *bot) isOwner(id int64) bool {
	cfg := b.cfg()
	return cfg.AdminChatID != 0 && id == cfg.AdminChatID
}

func (b *bot) handleMessage(ctx context.Context, m *tgMessage) {
	if m.From == nil {
		return
	}
	// The guard, before anything else: a management bot in a stranger's
	// hands is the operator's customers in a stranger's hands, so a
	// sender who is not the named owner is refused and nothing else
	// ever happens for them — no state is kept, no service is called.
	if !b.isOwner(m.From.ID) {
		b.send(ctx, m.Chat.ID, txtNotOwner, nil)
		return
	}

	sess := b.session()
	switch strings.TrimSpace(m.Text) {
	case "/start", "/cancel":
		// The two commands that always mean the same thing, whatever
		// the bot was in the middle of: back to the beginning, with
		// any half-typed wizard or amount forgotten.
		sess.reset()
		b.send(ctx, m.Chat.ID, txtWelcome, menuKeyboard())
	case "":
		// A photo, a sticker, a voice note: nothing the bot can act
		// on, and not worth an answer that pretends otherwise.
		return
	default:
		if sess.await != awaitNone {
			b.feedText(ctx, m, sess, strings.TrimSpace(m.Text))
			return
		}
		b.send(ctx, m.Chat.ID, txtMenuTitle, menuKeyboard())
	}
}

func (b *bot) handleCallback(ctx context.Context, cb *tgCallback) {
	if cb.From == nil {
		return
	}
	// The same guard, in the callback's own shape: a stranger's press
	// is answered with the refusal as an alert, and the press goes no
	// further.
	if !b.isOwner(cb.From.ID) {
		_ = b.api.answerCallbackQuery(ctx, cb.ID, txtNotOwner, true)
		return
	}

	sess := b.session()
	parts := strings.Split(cb.Data, ":")
	switch parts[0] {
	case "menu":
		b.answer(ctx, cb, "")
		sess.reset()
		b.editOrSend(ctx, cb, txtMenuTitle, menuKeyboard())
	case "list":
		page := sess.listPage
		if len(parts) == 2 {
			if n, err := strconv.Atoi(parts[1]); err == nil && n > 0 {
				page = n
			}
		}
		b.answer(ctx, cb, "")
		b.showList(ctx, cb, sess, page)
	case "c":
		id, ok := cbID(parts)
		if !ok {
			b.answer(ctx, cb, "")
			return
		}
		b.answer(ctx, cb, "")
		sess.detailID, sess.detailPage = id, sess.listPage
		b.showDetailEdit(ctx, cb, sess, id)
	case "link":
		if id, ok := cbID(parts); ok {
			b.sendLink(ctx, cb, id)
		} else {
			b.answer(ctx, cb, "")
		}
	case "cut", "uncut":
		if id, ok := cbID(parts); ok {
			b.setStatus(ctx, cb, sess, id, parts[0] == "cut")
		} else {
			b.answer(ctx, cb, "")
		}
	case "ext":
		if id, ok := cbID(parts); ok {
			b.answer(ctx, cb, "")
			b.showExtend(ctx, cb, id)
		} else {
			b.answer(ctx, cb, "")
		}
	case "extd":
		if id, days, ok := cbIDDays(parts); ok {
			b.applyDays(ctx, cb, sess, id, days)
		} else {
			b.answer(ctx, cb, "")
		}
	case "extc":
		if id, ok := cbID(parts); ok {
			b.answer(ctx, cb, "")
			sess.detailID, sess.await = id, awaitExtendDays
			b.editOrSend(ctx, cb, txtAskExtendDays, backToDetailKeyboard(id))
		} else {
			b.answer(ctx, cb, "")
		}
	case "quo":
		if id, ok := cbID(parts); ok {
			b.answer(ctx, cb, "")
			b.showQuota(ctx, cb, id)
		} else {
			b.answer(ctx, cb, "")
		}
	case "quod":
		if id, gb, ok := cbIDDays(parts); ok {
			b.applyQuota(ctx, cb, sess, id, uint64(gb)<<30)
		} else {
			b.answer(ctx, cb, "")
		}
	case "quoc":
		if id, ok := cbID(parts); ok {
			b.answer(ctx, cb, "")
			sess.detailID, sess.await = id, awaitAddQuota
			b.editOrSend(ctx, cb, txtAskQuotaGB, backToDetailKeyboard(id))
		} else {
			b.answer(ctx, cb, "")
		}
	case "rst":
		if id, ok := cbID(parts); ok {
			b.answer(ctx, cb, "")
			b.showResetAsk(ctx, cb, id)
		} else {
			b.answer(ctx, cb, "")
		}
	case "rsty":
		if id, ok := cbID(parts); ok {
			b.resetUsage(ctx, cb, sess, id)
		} else {
			b.answer(ctx, cb, "")
		}
	case "del":
		if id, ok := cbID(parts); ok {
			b.answer(ctx, cb, "")
			b.showDeleteAsk(ctx, cb, id)
		} else {
			b.answer(ctx, cb, "")
		}
	case "dely":
		if id, ok := cbID(parts); ok {
			b.deleteClient(ctx, cb, sess, id)
		} else {
			b.answer(ctx, cb, "")
		}
	case "add":
		b.answer(ctx, cb, "")
		sess.startWizard()
		sess.await = awaitWizardName
		b.editOrSend(ctx, cb, txtWizardAskName, cancelKeyboard())
	case "wzif":
		if id, ok := cbID(parts); ok {
			b.toggleTunnel(ctx, cb, sess, id)
		} else {
			b.answer(ctx, cb, "")
		}
	case "wznext":
		if len(sess.picked) == 0 {
			// The one press that is answered with words instead of a
			// screen: there is no next step to show yet.
			b.answer(ctx, cb, txtWizardNeedTunnel, true)
			return
		}
		b.answer(ctx, cb, "")
		b.editOrSend(ctx, cb, txtWizardAskQuota, wizardQuotaKeyboard())
	case "wzq":
		if gb, ok := cbAmount(parts); ok {
			b.answer(ctx, cb, "")
			sess.quota = uint64(gb) << 30
			b.editOrSend(ctx, cb, txtWizardAskDays, wizardDaysKeyboard())
		} else {
			b.answer(ctx, cb, "")
		}
	case "wzqc":
		b.answer(ctx, cb, "")
		sess.await = awaitWizardQuota
		b.editOrSend(ctx, cb, txtWizardAskQuotaGB, cancelKeyboard())
	case "wzd":
		if days, ok := cbAmount(parts); ok {
			b.answer(ctx, cb, "")
			sess.days = days
			b.editOrSend(ctx, cb, txtWizardAskDevices, wizardDevicesKeyboard())
		} else {
			b.answer(ctx, cb, "")
		}
	case "wzdc":
		b.answer(ctx, cb, "")
		sess.await = awaitWizardDays
		b.editOrSend(ctx, cb, txtWizardAskDaysCustom, cancelKeyboard())
	case "wzv":
		if n, ok := cbAmount(parts); ok {
			b.answer(ctx, cb, "")
			sess.devices = n
			b.showWizardSummary(ctx, cb, sess)
		} else {
			b.answer(ctx, cb, "")
		}
	case "wzsum":
		b.answer(ctx, cb, "")
		b.showWizardSummary(ctx, cb, sess)
	case "wzmake":
		b.makeClient(ctx, cb, sess)
	case "search":
		b.answer(ctx, cb, "")
		sess.await = awaitSearch
		b.editOrSend(ctx, cb, txtSearchPrompt, cancelKeyboard())
	case "stats":
		b.answer(ctx, cb, "")
		b.showStats(ctx, cb)
	case "help":
		b.answer(ctx, cb, "")
		b.editOrSend(ctx, cb, txtHelp, backToMenuKeyboard())
	default:
		b.answer(ctx, cb, "")
	}
}

// cbID reads the customer id out of a callback's second field.
func cbID(parts []string) (uint, bool) {
	if len(parts) != 2 {
		return 0, false
	}
	n, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || n == 0 {
		return 0, false
	}
	return uint(n), true
}

// cbIDDays reads a customer id and an amount (days, or gigabytes) out
// of a callback's second and third fields.
func cbIDDays(parts []string) (uint, int, bool) {
	if len(parts) != 3 {
		return 0, 0, false
	}
	id, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || id == 0 {
		return 0, 0, false
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil || n <= 0 {
		return 0, 0, false
	}
	return uint(id), n, true
}

// cbAmount reads a single amount out of a callback's second field. A
// wizard amount of zero is a real answer — it is the panel's
// "unlimited" — so zero is let through here, unlike a customer id.
func cbAmount(parts []string) (int, bool) {
	if len(parts) != 2 {
		return 0, false
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// feedText hands a text message to whatever question the bot is
// waiting on an answer to: a wizard step, a custom amount, a search.
// An answer that does not parse is not consumed — the question is
// repeated and the operator is still being asked it.
func (b *bot) feedText(ctx context.Context, m *tgMessage, sess *session, text string) {
	chatID := m.Chat.ID
	switch sess.await {
	case awaitWizardName:
		if text == "" {
			b.send(ctx, chatID, txtWizardNameEmpty, nil)
			return
		}
		sess.name = text
		sess.await = awaitNone
		text2, kb, err := b.wizardTunnelsScreen(ctx, sess)
		if err != nil {
			sess.startWizard()
			b.send(ctx, chatID, failText(err), menuKeyboard())
			return
		}
		if kb == nil {
			// No tunnels at all: the wizard cannot go on, and the
			// reason is the operator's to take to the panel's owner.
			sess.startWizard()
			b.send(ctx, chatID, txtWizardNoTunnels, menuKeyboard())
			return
		}
		b.send(ctx, chatID, text2, kb)
	case awaitWizardQuota:
		gb, ok := parseAmount(text)
		if !ok {
			b.send(ctx, chatID, txtInvalidGB, nil)
			return
		}
		sess.quota = uint64(gb * (1 << 30))
		sess.await = awaitNone
		b.send(ctx, chatID, txtWizardAskDays, wizardDaysKeyboard())
	case awaitWizardDays:
		f, ok := parseAmount(text)
		if !ok || f != float64(int(f)) {
			b.send(ctx, chatID, txtInvalidDays, nil)
			return
		}
		sess.days = int(f)
		sess.await = awaitNone
		b.send(ctx, chatID, txtWizardAskDevices, wizardDevicesKeyboard())
	case awaitSearch:
		sess.await = awaitNone
		b.runSearch(ctx, chatID, sess, text)
	case awaitExtendDays:
		f, ok := parseAmount(text)
		if !ok || f != float64(int(f)) {
			b.send(ctx, chatID, txtInvalidDays, nil)
			return
		}
		sess.await = awaitNone
		b.applyDaysSend(ctx, chatID, sess, sess.detailID, int(f))
	case awaitAddQuota:
		gb, ok := parseAmount(text)
		if !ok {
			b.send(ctx, chatID, txtInvalidGB, nil)
			return
		}
		sess.await = awaitNone
		b.applyQuotaSend(ctx, chatID, sess, sess.detailID, uint64(gb*(1<<30)))
	}
}

// ── The customer list ────────────────────────────────────────────────

// showList renders one page of the operator's customers onto the
// message the button was pressed on.
func (b *bot) showList(ctx context.Context, cb *tgCallback, sess *session, page int) {
	text, kb, err := b.listScreen(ctx, sess, page)
	if err != nil {
		b.editOrSend(ctx, cb, failText(err), backToMenuKeyboard())
		return
	}
	b.editOrSend(ctx, cb, text, kb)
}

// listScreen is one list page as text and keyboard: the operator's
// customers under their own scope, newest first, a page at a time.
func (b *bot) listScreen(ctx context.Context, sess *session, page int) (string, tgKeyboard, error) {
	opCtx, err := b.opCtx(ctx)
	if err != nil {
		return "", nil, err
	}
	if page < 1 {
		page = 1
	}
	p, err := b.clients().List(opCtx, service.ListFilter{Page: page, PerPage: listPageSize})
	if err != nil {
		return "", nil, err
	}
	pages := int((p.Total + listPageSize - 1) / listPageSize)
	if pages < 1 {
		pages = 1
	}
	if page > pages {
		// The list shrank under the operator — a customer deleted
		// from the last page — so the last page that exists is the
		// one to show, not an empty screen insisting it is page
		// nine.
		return b.listScreen(ctx, sess, pages)
	}
	sess.listPage = page

	if p.Total == 0 {
		return txtNoClients, tgKeyboard{
			{{Text: btnAddClient, Data: "add"}},
			{{Text: btnBackToMenu, Data: "menu"}},
		}, nil
	}

	text := fmt.Sprintf(txtListTitle, faNum(p.Total))
	if pages > 1 {
		text += "\n" + fmt.Sprintf(txtListPage, faNum(int64(page)), faNum(int64(pages)))
	}
	kb := tgKeyboard{}
	for i := range p.Items {
		c := &p.Items[i]
		kb = append(kb, []tgButton{{
			Text: statusEmoji(c.Status) + " " + c.Name,
			Data: "c:" + strconv.FormatUint(uint64(c.ID), 10),
		}})
	}
	if pages > 1 {
		nav := []tgButton{}
		if page > 1 {
			nav = append(nav, tgButton{Text: btnPrev, Data: "list:" + strconv.Itoa(page-1)})
		}
		if page < pages {
			nav = append(nav, tgButton{Text: btnNext, Data: "list:" + strconv.Itoa(page+1)})
		}
		kb = append(kb, nav)
	}
	kb = append(kb, []tgButton{{Text: btnBackToMenu, Data: "menu"}})
	return text, kb, nil
}

// statusEmoji is a customer's state at a glance in a list.
func statusEmoji(s model.ClientStatus) string {
	switch s {
	case model.StatusActive:
		return "🟢"
	case model.StatusDisabled:
		return "🔴"
	case model.StatusExpired:
		return "⏰"
	case model.StatusExhausted:
		return "📛"
	}
	return "⚪"
}

// statusName is a customer's state in words, beside the emoji.
func statusName(s model.ClientStatus) string {
	switch s {
	case model.StatusActive:
		return txtStatusActive
	case model.StatusDisabled:
		return txtStatusDisabled
	case model.StatusExpired:
		return txtStatusExpired
	case model.StatusExhausted:
		return txtStatusExhausted
	}
	return txtStatusOther
}

// ── One customer ─────────────────────────────────────────────────────

// showDetailEdit renders a customer's detail onto the pressed message.
func (b *bot) showDetailEdit(ctx context.Context, cb *tgCallback, sess *session, id uint) {
	text, kb, err := b.detailScreen(ctx, sess, id)
	if err != nil {
		b.editOrSend(ctx, cb, failText(err), backToListKeyboard(sess))
		return
	}
	b.editOrSend(ctx, cb, text, kb)
}

// showDetailSend renders the same detail as a fresh message, for the
// end of a text answer, where there is no button message to rewrite.
func (b *bot) showDetailSend(ctx context.Context, chatID int64, sess *session, id uint) {
	text, kb, err := b.detailScreen(ctx, sess, id)
	if err != nil {
		b.send(ctx, chatID, failText(err), menuKeyboard())
		return
	}
	b.send(ctx, chatID, text, kb)
}

// detailScreen is one customer as the operator needs them: who they
// are, what state they are in, what they have used and what is left,
// and the buttons that do things to them.
func (b *bot) detailScreen(ctx context.Context, sess *session, id uint) (string, tgKeyboard, error) {
	opCtx, err := b.opCtx(ctx)
	if err != nil {
		return "", nil, err
	}
	c, err := b.clients().Get(opCtx, id)
	if err != nil {
		return "", nil, err
	}
	idStr := strconv.FormatUint(uint64(c.ID), 10)

	var sb strings.Builder
	fmt.Fprintf(&sb, txtDetailName+"\n", escapeHTML(c.Name))
	fmt.Fprintf(&sb, txtDetailStatus+"\n", statusEmoji(c.Status), statusName(c.Status))
	if c.QuotaBytes > 0 {
		pct := c.UsedBytes * 100 / c.QuotaBytes
		fmt.Fprintf(&sb, txtDetailUsed+"\n", gbText(c.UsedBytes), gbText(c.QuotaBytes), faNum(int64(pct)))
	} else {
		fmt.Fprintf(&sb, txtDetailUsedUnlimited+"\n", gbText(c.UsedBytes))
	}
	switch {
	case c.Waiting():
		fmt.Fprintf(&sb, txtDetailWaiting+"\n", formatDays(c.DurationDays))
	case c.ExpiresAt == nil:
		sb.WriteString(txtDetailNoExpiry + "\n")
	default:
		fmt.Fprintf(&sb, txtDetailExpiry+"\n", formatDate(*c.ExpiresAt), daysLeftText(*c.ExpiresAt, time.Now()))
	}
	devices := txtUnlimitedWd
	if c.DeviceLimit > 0 {
		devices = faNum(int64(c.DeviceLimit))
	}
	fmt.Fprintf(&sb, txtDetailDevices+"\n", devices)
	if names := b.interfaceNames(ctx, c); len(names) > 0 {
		fmt.Fprintf(&sb, txtDetailTunnels, strings.Join(names, "، "))
	}
	text := strings.TrimRight(sb.String(), "\n")

	// The token rides in the detail only when there is no public
	// address to make a link of it with: with one, the link button
	// below is the cleaner thing to hand over, and the token stays
	// out of a screen the operator might screenshot.
	if base, _ := b.publicBase(ctx); base == "" && c.SubToken != "" {
		text += "\n\n" + fmt.Sprintf(txtDetailToken, escapeHTML(c.SubToken))
	}

	kb := tgKeyboard{
		{{Text: btnSendLink, Data: "link:" + idStr}},
	}
	switch c.Status {
	case model.StatusActive:
		kb = append(kb, []tgButton{{Text: btnCut, Data: "cut:" + idStr}})
	case model.StatusDisabled:
		kb = append(kb, []tgButton{{Text: btnRestore, Data: "uncut:" + idStr}})
	}
	kb = append(kb,
		[]tgButton{
			{Text: btnExtend, Data: "ext:" + idStr},
			{Text: btnAddQuota, Data: "quo:" + idStr},
		},
		[]tgButton{
			{Text: btnResetUsage, Data: "rst:" + idStr},
			{Text: btnDelete, Data: "del:" + idStr},
		},
		[]tgButton{{Text: btnBackToList, Data: "list:" + strconv.Itoa(sess.detailPage)}},
	)
	return text, kb, nil
}

// interfaceNames resolves a customer's accounts to the names of the
// tunnels they sit on, in account order, each once. Display only —
// the rows are read from the database because a name is all that is
// wanted of them, and the customer service is not a directory.
func (b *bot) interfaceNames(ctx context.Context, c *model.Client) []string {
	seen := map[uint]bool{}
	var ids []uint
	for _, a := range c.Accounts {
		if !seen[a.InterfaceID] {
			seen[a.InterfaceID] = true
			ids = append(ids, a.InterfaceID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var rows []model.Interface
	if err := b.mgr.db.WithContext(ctx).Select("id", "name").
		Where("id IN ?", ids).Find(&rows).Error; err != nil {
		b.log.Warn("management bot could not name the tunnels", "error", err)
		return nil
	}
	byID := make(map[uint]string, len(rows))
	for i := range rows {
		byID[rows[i].ID] = rows[i].Name
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if name, ok := byID[id]; ok {
			out = append(out, name)
		}
	}
	return out
}

// sendLink answers the link button with a fresh message holding the
// link itself: a thing the operator taps to forward, not a screen to
// navigate, so it is sent rather than edited into the detail.
func (b *bot) sendLink(ctx context.Context, cb *tgCallback, id uint) {
	b.answer(ctx, cb, "")
	chatID := cb.From.ID
	if cb.Message != nil {
		chatID = cb.Message.Chat.ID
	}
	opCtx, err := b.opCtx(ctx)
	if err != nil {
		b.send(ctx, chatID, failText(err), nil)
		return
	}
	c, err := b.clients().Get(opCtx, id)
	if err != nil {
		b.send(ctx, chatID, failText(err), nil)
		return
	}
	b.send(ctx, chatID, b.linkText(ctx, c), nil)
}

// linkText is what a customer is handed: their subscription link, or
// — when the panel has no public address yet — their token, which is
// the half of the link that is secret and theirs.
func (b *bot) linkText(ctx context.Context, c *model.Client) string {
	if link := b.subLink(ctx, c); link != "" {
		return fmt.Sprintf(txtLinkMessage, escapeHTML(c.Name), link)
	}
	if c.SubToken != "" {
		return fmt.Sprintf(txtLinkToken, escapeHTML(c.Name), escapeHTML(c.SubToken))
	}
	return txtError
}

// setStatus cuts a customer off or restores them, through the panel's
// own status write — the same Update the panel's form saves — and
// shows the detail again so the new state is what is on screen.
func (b *bot) setStatus(ctx context.Context, cb *tgCallback, sess *session, id uint, cut bool) {
	opCtx, err := b.opCtx(ctx)
	if err != nil {
		b.answer(ctx, cb, "")
		b.editOrSend(ctx, cb, failText(err), backToListKeyboard(sess))
		return
	}
	status := model.StatusActive
	toast := txtRestoreDone
	if cut {
		status = model.StatusDisabled
		toast = txtCutDone
	}
	if _, err := b.clients().Update(opCtx, id, service.UpdateInput{Status: &status}); err != nil {
		b.answer(ctx, cb, "")
		b.editOrSend(ctx, cb, failText(err), backToListKeyboard(sess))
		return
	}
	b.answer(ctx, cb, toast)
	b.showDetailEdit(ctx, cb, sess, id)
}

// showExtend is the extend-time submenu: the two amounts an operator
// reaches for, and the door to any other.
func (b *bot) showExtend(ctx context.Context, cb *tgCallback, id uint) {
	c, err := b.loadClient(ctx, id)
	if err != nil {
		b.editOrSend(ctx, cb, failText(err), backToMenuKeyboard())
		return
	}
	idStr := strconv.FormatUint(uint64(id), 10)
	b.editOrSend(ctx, cb, fmt.Sprintf(txtExtendTitle, escapeHTML(c.Name)), tgKeyboard{
		{
			{Text: btnDays7, Data: "extd:" + idStr + ":7"},
			{Text: btnDays30, Data: "extd:" + idStr + ":30"},
		},
		{{Text: btnCustom, Data: "extc:" + idStr}},
		{{Text: btnBack, Data: "c:" + idStr}},
	})
}

// showQuota is the add-volume submenu, the extend screen's twin.
func (b *bot) showQuota(ctx context.Context, cb *tgCallback, id uint) {
	c, err := b.loadClient(ctx, id)
	if err != nil {
		b.editOrSend(ctx, cb, failText(err), backToMenuKeyboard())
		return
	}
	idStr := strconv.FormatUint(uint64(id), 10)
	b.editOrSend(ctx, cb, fmt.Sprintf(txtQuotaTitle, escapeHTML(c.Name)), tgKeyboard{
		{
			{Text: btnGB10, Data: "quod:" + idStr + ":10"},
			{Text: btnGB50, Data: "quod:" + idStr + ":50"},
		},
		{{Text: btnCustom, Data: "quoc:" + idStr}},
		{{Text: btnBack, Data: "c:" + idStr}},
	})
}

// loadClient reads one customer under the operator's scope, for the
// screens that need the customer's name before they can ask anything.
func (b *bot) loadClient(ctx context.Context, id uint) (*model.Client, error) {
	opCtx, err := b.opCtx(ctx)
	if err != nil {
		return nil, err
	}
	return b.clients().Get(opCtx, id)
}

// errNoDate and errNoCeiling are the two refusals that are not
// failures: the customer's plan has no end date to extend, or no
// ceiling to raise. They are answered with their own words rather
// than as errors, because nothing went wrong.
var (
	errNoDate    = errors.New("adminbot: the customer has no expiry to extend")
	errNoCeiling = errors.New("adminbot: the customer has no quota to raise")
)

// extendDays adds days to one customer, by the panel's own rules for
// adding to a plan — the same care the shop's renewal takes: a plan
// still waiting for its first connection grows in length and still
// starts when they connect; a running plan's end moves from the later
// of now and its current end, so time already paid for is never taken
// back; a plan with no end date is not given one by accident.
func (b *bot) extendDays(ctx context.Context, id uint, days int) error {
	opCtx, err := b.opCtx(ctx)
	if err != nil {
		return err
	}
	c, err := b.clients().Get(opCtx, id)
	if err != nil {
		return err
	}
	if c.Waiting() {
		_, err := b.clients().Extend(opCtx, service.ExtendInput{
			IDs: []uint{id}, Kind: service.ExtendTime,
			Days: float64(days), IncludeWaiting: true,
		})
		return err
	}
	if c.ExpiresAt == nil {
		return errNoDate
	}
	f := float64(days)
	_, err = b.clients().Adjust(opCtx, service.AdjustInput{IDs: []uint{id}, AddDays: &f})
	return err
}

// applyDays extends from a button press and shows the detail again.
func (b *bot) applyDays(ctx context.Context, cb *tgCallback, sess *session, id uint, days int) {
	err := b.extendDays(ctx, id, days)
	switch {
	case errors.Is(err, errNoDate):
		b.answer(ctx, cb, txtExtendNoDate, true)
		return
	case err != nil:
		b.answer(ctx, cb, "")
		b.editOrSend(ctx, cb, failText(err), backToListKeyboard(sess))
		return
	}
	b.answer(ctx, cb, txtDoneToast)
	b.showDetailEdit(ctx, cb, sess, id)
}

// applyDaysSend is applyDays at the end of a typed answer.
func (b *bot) applyDaysSend(ctx context.Context, chatID int64, sess *session, id uint, days int) {
	err := b.extendDays(ctx, id, days)
	switch {
	case errors.Is(err, errNoDate):
		b.send(ctx, chatID, txtExtendNoDate, nil)
		b.showDetailSend(ctx, chatID, sess, id)
		return
	case err != nil:
		b.send(ctx, chatID, failText(err), menuKeyboard())
		return
	}
	b.showDetailSend(ctx, chatID, sess, id)
}

// addQuota grows one customer's allowance by the given bytes. The
// panel's Adjust sets the allowance rather than adding to it, so the
// sum is worked out here and handed over whole — the shop's renewal
// arithmetic, for the same reason: traffic a customer had not spent
// is still theirs. A customer with no ceiling is not given one.
func (b *bot) addQuota(ctx context.Context, id uint, add uint64) error {
	opCtx, err := b.opCtx(ctx)
	if err != nil {
		return err
	}
	c, err := b.clients().Get(opCtx, id)
	if err != nil {
		return err
	}
	if c.QuotaBytes == 0 {
		return errNoCeiling
	}
	grown := c.QuotaBytes + add
	_, err = b.clients().Adjust(opCtx, service.AdjustInput{IDs: []uint{id}, QuotaBytes: &grown})
	return err
}

// applyQuota adds volume from a button press and shows the detail.
func (b *bot) applyQuota(ctx context.Context, cb *tgCallback, sess *session, id uint, add uint64) {
	err := b.addQuota(ctx, id, add)
	switch {
	case errors.Is(err, errNoCeiling):
		b.answer(ctx, cb, txtQuotaNoCeil, true)
		return
	case err != nil:
		b.answer(ctx, cb, "")
		b.editOrSend(ctx, cb, failText(err), backToListKeyboard(sess))
		return
	}
	b.answer(ctx, cb, txtDoneToast)
	b.showDetailEdit(ctx, cb, sess, id)
}

// applyQuotaSend is applyQuota at the end of a typed answer.
func (b *bot) applyQuotaSend(ctx context.Context, chatID int64, sess *session, id uint, add uint64) {
	err := b.addQuota(ctx, id, add)
	switch {
	case errors.Is(err, errNoCeiling):
		b.send(ctx, chatID, txtQuotaNoCeil, nil)
		b.showDetailSend(ctx, chatID, sess, id)
		return
	case err != nil:
		b.send(ctx, chatID, failText(err), menuKeyboard())
		return
	}
	b.showDetailSend(ctx, chatID, sess, id)
}

// showResetAsk is the reset's confirm step: the act is one tap from
// here and cannot be undone, so the tap that asks and the tap that
// does are different taps.
func (b *bot) showResetAsk(ctx context.Context, cb *tgCallback, id uint) {
	c, err := b.loadClient(ctx, id)
	if err != nil {
		b.editOrSend(ctx, cb, failText(err), backToMenuKeyboard())
		return
	}
	idStr := strconv.FormatUint(uint64(id), 10)
	b.editOrSend(ctx, cb, fmt.Sprintf(txtResetAsk, escapeHTML(c.Name)), tgKeyboard{
		{{Text: btnConfirmRst, Data: "rsty:" + idStr}},
		{{Text: btnBack, Data: "c:" + idStr}},
	})
}

// resetUsage zeroes a customer's usage through the panel's own reset
// — the service call behind the panel's reset button — and shows the
// detail again at zero.
func (b *bot) resetUsage(ctx context.Context, cb *tgCallback, sess *session, id uint) {
	opCtx, err := b.opCtx(ctx)
	if err == nil {
		_, err = b.clients().ResetTraffic(opCtx, id)
	}
	if err != nil {
		b.answer(ctx, cb, "")
		b.editOrSend(ctx, cb, failText(err), backToListKeyboard(sess))
		return
	}
	b.answer(ctx, cb, txtDoneToast)
	b.showDetailEdit(ctx, cb, sess, id)
}

// showDeleteAsk is the delete's confirm step, naming the customer:
// the wrong row deleted is the one support call nobody can undo.
func (b *bot) showDeleteAsk(ctx context.Context, cb *tgCallback, id uint) {
	c, err := b.loadClient(ctx, id)
	if err != nil {
		b.editOrSend(ctx, cb, failText(err), backToMenuKeyboard())
		return
	}
	idStr := strconv.FormatUint(uint64(id), 10)
	b.editOrSend(ctx, cb, fmt.Sprintf(txtDeleteAsk, escapeHTML(c.Name)), tgKeyboard{
		{{Text: btnConfirmDel, Data: "dely:" + idStr}},
		{{Text: btnBack, Data: "c:" + idStr}},
	})
}

// deleteClient removes a customer and lands back on the list page
// they were opened from, one shorter.
func (b *bot) deleteClient(ctx context.Context, cb *tgCallback, sess *session, id uint) {
	c, err := b.loadClient(ctx, id)
	if err == nil {
		opCtx, oerr := b.opCtx(ctx)
		if oerr != nil {
			err = oerr
		} else {
			err = b.clients().Delete(opCtx, id)
		}
	}
	if err != nil {
		b.answer(ctx, cb, "")
		b.editOrSend(ctx, cb, failText(err), backToListKeyboard(sess))
		return
	}
	b.answer(ctx, cb, txtDoneToast)
	sess.detailID = 0
	text, kb, lerr := b.listScreen(ctx, sess, sess.detailPage)
	if lerr != nil {
		b.editOrSend(ctx, cb, fmt.Sprintf(txtDeleted, escapeHTML(c.Name)), backToMenuKeyboard())
		return
	}
	b.editOrSend(ctx, cb, fmt.Sprintf(txtDeleted, escapeHTML(c.Name))+"\n\n"+text, kb)
}

// ── The add wizard ───────────────────────────────────────────────────

// wizardTunnels reads the tunnels the wizard may offer: every enabled
// interface the operator's scope allows. The owner's scope allows
// them all; a reseller's allows exactly the set the owner assigned
// them, which OperatorCtx has already read off their grants.
func (b *bot) wizardTunnels(ctx context.Context) ([]model.Interface, error) {
	opCtx, err := b.opCtx(ctx)
	if err != nil {
		return nil, err
	}
	sc := service.ScopeOf(opCtx)
	var rows []model.Interface
	if err := b.mgr.db.WithContext(ctx).Where("enabled = ?", true).
		Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]model.Interface, 0, len(rows))
	for i := range rows {
		if sc.Allows(rows[i].ID) {
			out = append(out, rows[i])
		}
	}
	return out, nil
}

// wizardTunnelsScreen is the wizard's second step: the tunnels as
// toggles, the ones already chosen marked. A nil keyboard says there
// are no tunnels to offer, which the caller answers with its own
// words.
func (b *bot) wizardTunnelsScreen(ctx context.Context, sess *session) (string, tgKeyboard, error) {
	rows, err := b.wizardTunnels(ctx)
	if err != nil {
		return "", nil, err
	}
	if len(rows) == 0 {
		return txtWizardNoTunnels, nil, nil
	}
	kb := tgKeyboard{}
	for i := range rows {
		mark := "⬜ "
		if sess.picked[rows[i].ID] {
			mark = "✅ "
		}
		kb = append(kb, []tgButton{{
			Text: mark + rows[i].Name,
			Data: "wzif:" + strconv.FormatUint(uint64(rows[i].ID), 10),
		}})
	}
	kb = append(kb,
		[]tgButton{{Text: btnWizardNext, Data: "wznext"}},
		[]tgButton{{Text: btnCancel, Data: "menu"}},
	)
	return txtWizardPickTunnels, kb, nil
}

// toggleTunnel flips one tunnel in the wizard's draft and redraws the
// step. A tunnel outside the operator's allowed set is not toggled:
// the buttons offered are already narrowed to it, so an id outside it
// arrived by hand and is ignored the same way.
func (b *bot) toggleTunnel(ctx context.Context, cb *tgCallback, sess *session, id uint) {
	b.answer(ctx, cb, "")
	opCtx, err := b.opCtx(ctx)
	if err != nil {
		b.editOrSend(ctx, cb, failText(err), backToMenuKeyboard())
		return
	}
	if !service.ScopeOf(opCtx).Allows(id) {
		return
	}
	if sess.picked == nil {
		sess.picked = map[uint]bool{}
	}
	if sess.picked[id] {
		delete(sess.picked, id)
	} else {
		sess.picked[id] = true
	}
	text, kb, err := b.wizardTunnelsScreen(ctx, sess)
	if err != nil {
		b.editOrSend(ctx, cb, failText(err), backToMenuKeyboard())
		return
	}
	if kb == nil {
		b.editOrSend(ctx, cb, txtWizardNoTunnels, backToMenuKeyboard())
		return
	}
	b.editOrSend(ctx, cb, text, kb)
}

func wizardQuotaKeyboard() tgKeyboard {
	return tgKeyboard{
		{
			{Text: faNum(10) + " گیگ", Data: "wzq:10"},
			{Text: faNum(20) + " گیگ", Data: "wzq:20"},
		},
		{
			{Text: faNum(50) + " گیگ", Data: "wzq:50"},
			{Text: faNum(100) + " گیگ", Data: "wzq:100"},
		},
		{
			{Text: btnUnlimited, Data: "wzq:0"},
			{Text: btnCustom, Data: "wzqc"},
		},
		{{Text: btnCancel, Data: "menu"}},
	}
}

func wizardDaysKeyboard() tgKeyboard {
	return tgKeyboard{
		{
			{Text: faNum(30) + " روز", Data: "wzd:30"},
			{Text: faNum(90) + " روز", Data: "wzd:90"},
		},
		{
			{Text: faNum(180) + " روز", Data: "wzd:180"},
			{Text: btnUnlimited, Data: "wzd:0"},
		},
		{{Text: btnCustom, Data: "wzdc"}},
		{{Text: btnCancel, Data: "menu"}},
	}
}

func wizardDevicesKeyboard() tgKeyboard {
	return tgKeyboard{
		{
			{Text: faNum(1), Data: "wzv:1"},
			{Text: faNum(2), Data: "wzv:2"},
		},
		{
			{Text: faNum(3), Data: "wzv:3"},
			{Text: btnUnlimited, Data: "wzv:0"},
		},
		{{Text: btnCancel, Data: "menu"}},
	}
}

// showWizardSummary is the wizard's last screen before the act: the
// whole form read back, so a wrong tap earlier is caught here rather
// than in the customer it made.
func (b *bot) showWizardSummary(ctx context.Context, cb *tgCallback, sess *session) {
	if sess.name == "" || len(sess.picked) == 0 {
		// The summary of a form that was never filled in — a stale
		// button from a finished or abandoned run — is the menu, not
		// a summary of nothing.
		sess.reset()
		b.editOrSend(ctx, cb, txtMenuTitle, menuKeyboard())
		return
	}
	names := b.pickedNames(ctx, sess)
	devices := txtUnlimitedWd
	if sess.devices > 0 {
		devices = faNum(int64(sess.devices))
	}
	text := fmt.Sprintf(txtWizardSummary,
		escapeHTML(sess.name), escapeHTML(strings.Join(names, "، ")),
		formatBytes(sess.quota), formatDays(sess.days), devices)
	b.editOrSend(ctx, cb, text, tgKeyboard{
		{{Text: btnWizardMake, Data: "wzmake"}},
		{{Text: btnCancel, Data: "menu"}},
	})
}

// pickedNames names the wizard's chosen tunnels, in id order.
func (b *bot) pickedNames(ctx context.Context, sess *session) []string {
	ids := make([]uint, 0, len(sess.picked))
	for id := range sess.picked {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	if len(ids) == 0 {
		return nil
	}
	var rows []model.Interface
	if err := b.mgr.db.WithContext(ctx).Select("id", "name").
		Where("id IN ?", ids).Find(&rows).Error; err != nil {
		b.log.Warn("management bot could not name the tunnels", "error", err)
		return nil
	}
	byID := make(map[uint]string, len(rows))
	for i := range rows {
		byID[rows[i].ID] = rows[i].Name
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, byID[id])
	}
	return out
}

// makeClient is the wizard's last tap: the customer is created under
// the operator's scope, with the panel's own semantics — a quota of
// zero is no ceiling, a device limit of zero is no limit, and a length
// in days is an end date counted from now, the way the shop sells
// them. Create's refusal, when there is one, is shown verbatim: it
// names the field and the reason, and both are the operator's to fix.
func (b *bot) makeClient(ctx context.Context, cb *tgCallback, sess *session) {
	if sess.name == "" || len(sess.picked) == 0 {
		b.answer(ctx, cb, "")
		sess.reset()
		b.editOrSend(ctx, cb, txtMenuTitle, menuKeyboard())
		return
	}
	opCtx, err := b.opCtx(ctx)
	if err != nil {
		b.answer(ctx, cb, "")
		b.editOrSend(ctx, cb, failText(err), backToMenuKeyboard())
		return
	}
	ids := make([]uint, 0, len(sess.picked))
	for id := range sess.picked {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	in := service.CreateInput{
		Name:         sess.name,
		InterfaceIDs: ids,
		QuotaBytes:   sess.quota,
		DeviceLimit:  sess.devices,
	}
	if sess.days > 0 {
		expires := time.Now().UTC().Add(time.Duration(sess.days) * 24 * time.Hour)
		in.ExpiresAt = &expires
	}
	client, err := b.clients().Create(opCtx, in)
	if err != nil {
		b.answer(ctx, cb, "")
		b.editOrSend(ctx, cb, failText(err), tgKeyboard{
			{{Text: btnTryAgain, Data: "wzsum"}},
			{{Text: btnBackToMenu, Data: "menu"}},
		})
		return
	}
	b.answer(ctx, cb, txtDoneToast)
	name := sess.name
	sess.startWizard()
	sess.detailID, sess.detailPage = client.ID, 1
	text := fmt.Sprintf(txtWizardCreated, escapeHTML(name)) + "\n\n" + b.linkText(ctx, client)
	b.editOrSend(ctx, cb, text, tgKeyboard{
		{{Text: btnViewClient, Data: "c:" + strconv.FormatUint(uint64(client.ID), 10)}},
		{{Text: btnBackToMenu, Data: "menu"}},
	})
}

// ── Search and stats ─────────────────────────────────────────────────

// runSearch answers a search text with the matches as detail buttons.
func (b *bot) runSearch(ctx context.Context, chatID int64, sess *session, query string) {
	opCtx, err := b.opCtx(ctx)
	if err != nil {
		b.send(ctx, chatID, failText(err), menuKeyboard())
		return
	}
	p, err := b.clients().List(opCtx, service.ListFilter{Search: query, Page: 1, PerPage: 25})
	if err != nil {
		b.send(ctx, chatID, failText(err), menuKeyboard())
		return
	}
	if p.Total == 0 {
		b.send(ctx, chatID, txtSearchNone, menuKeyboard())
		return
	}
	kb := tgKeyboard{}
	for i := range p.Items {
		c := &p.Items[i]
		kb = append(kb, []tgButton{{
			Text: statusEmoji(c.Status) + " " + c.Name,
			Data: "c:" + strconv.FormatUint(uint64(c.ID), 10),
		}})
	}
	kb = append(kb, []tgButton{{Text: btnBackToMenu, Data: "menu"}})
	b.send(ctx, chatID, fmt.Sprintf(txtSearchResults, faNum(p.Total)), kb)
}

// showStats renders the operator's totals: the whole of their book,
// read through the scoped list a page at a time — the list is the
// fence, so the totals are the fence's too.
func (b *bot) showStats(ctx context.Context, cb *tgCallback) {
	opCtx, err := b.opCtx(ctx)
	if err != nil {
		b.editOrSend(ctx, cb, failText(err), backToMenuKeyboard())
		return
	}
	var total int64
	var used uint64
	counts := map[model.ClientStatus]int64{}
	// Two hundred pages of two hundred is forty thousand customers:
	// past that the totals would be a report, not a screen, and the
	// loop stops rather than walk a book that size one page at a
	// time behind a button press.
	for page := 1; page <= 200; page++ {
		p, err := b.clients().List(opCtx, service.ListFilter{Page: page, PerPage: 200})
		if err != nil {
			b.editOrSend(ctx, cb, failText(err), backToMenuKeyboard())
			return
		}
		total = p.Total
		for i := range p.Items {
			counts[p.Items[i].Status]++
			used += p.Items[i].UsedBytes
		}
		if int64(page)*int64(p.PerPage) >= p.Total {
			break
		}
	}

	var sb strings.Builder
	sb.WriteString(txtStatsTitle + "\n\n")
	fmt.Fprintf(&sb, txtStatsTotal+"\n", faNum(total))
	fmt.Fprintf(&sb, txtStatsActive+"\n", faNum(counts[model.StatusActive]))
	fmt.Fprintf(&sb, txtStatsDisabled+"\n", faNum(counts[model.StatusDisabled]))
	fmt.Fprintf(&sb, txtStatsExpired+"\n", faNum(counts[model.StatusExpired]))
	fmt.Fprintf(&sb, txtStatsExhausted+"\n", faNum(counts[model.StatusExhausted]))
	if limit := service.ScopeOf(opCtx).ClientLimit; limit > 0 {
		fmt.Fprintf(&sb, txtStatsLimit+"\n", faNum(total), faNum(int64(limit)))
	} else {
		fmt.Fprintf(&sb, txtStatsLimitUnlimited+"\n", faNum(total))
	}
	fmt.Fprintf(&sb, txtStatsUsed, gbText(used))
	b.editOrSend(ctx, cb, sb.String(), backToMenuKeyboard())
}

// ── Keyboards and plumbing ───────────────────────────────────────────

// menuKeyboard is the main menu, offered at /start and under the menu
// button everywhere.
func menuKeyboard() tgKeyboard {
	return tgKeyboard{
		{
			{Text: btnMyClients, Data: "list"},
			{Text: btnAddClient, Data: "add"},
		},
		{
			{Text: btnSearch, Data: "search"},
			{Text: btnStats, Data: "stats"},
		},
		{{Text: btnHelp, Data: "help"}},
	}
}

func backToMenuKeyboard() tgKeyboard {
	return tgKeyboard{{{Text: btnBackToMenu, Data: "menu"}}}
}

func backToListKeyboard(sess *session) tgKeyboard {
	return tgKeyboard{{{Text: btnBackToList, Data: "list:" + strconv.Itoa(sess.detailPage)}}}
}

func backToDetailKeyboard(id uint) tgKeyboard {
	return tgKeyboard{{{Text: btnBack, Data: "c:" + strconv.FormatUint(uint64(id), 10)}}}
}

func cancelKeyboard() tgKeyboard {
	return tgKeyboard{{{Text: btnCancel, Data: "menu"}}}
}

// failText is an error the way the operator reads it: the standard
// words, then the service's own, verbatim.
func failText(err error) string {
	return fmt.Sprintf(txtFailed, escapeHTML(err.Error()))
}

// answer acknowledges a button press; text, when given, is what
// Telegram shows the operator — a toast, or an alert when it is one
// they must read before going on.
func (b *bot) answer(ctx context.Context, cb *tgCallback, text string, alert ...bool) {
	show := len(alert) > 0 && alert[0]
	_ = b.api.answerCallbackQuery(ctx, cb.ID, text, show)
}

// editOrSend rewrites the message a button was pressed on, which
// keeps a conversation to one growing thread instead of a pile of
// menus; when the message cannot be edited — too old, or not the
// bot's — a fresh message carries the screen instead.
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

// send writes a message and logs, rather than returns, a failure: by
// the time the bot is speaking, the conversation has already moved on.
func (b *bot) send(ctx context.Context, chatID int64, text string, kb tgKeyboard) {
	if _, err := b.api.sendMessage(ctx, chatID, text, kb); err != nil {
		b.log.Warn("management bot could not send", "error", err)
	}
}
