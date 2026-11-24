// Package shopbot is the panel's sales bots: one Telegram bot per
// operator, each run from that operator's own BotFather token, selling
// that operator's plans to that operator's customers.
//
// The panel's other bot (internal/tgbot) is the panel's own — one bot,
// run by the owner, for administering the panel and answering its
// customers. This one is a shopfront: an operator pastes a token into
// their shop settings and gets a bot that takes orders and money, and
// the panel (internal/shop) turns paid orders into customers. A bot
// never sees another operator's token, plans or buyers.
package shopbot

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/abolfazl/w-ui/internal/service"
	"github.com/abolfazl/w-ui/internal/shop"
)

// Manager runs every operator's bot. Bots are independent on purpose: one
// operator's dead token must cost that operator their shopfront and
// nobody else's, so each bot polls in its own goroutine and retries on
// its own backoff.
type Manager struct {
	shop    *shop.Shop
	clients *service.Clients
	log     *slog.Logger

	mu   sync.Mutex
	ctx  context.Context // the root the bots' contexts hang from; set by Start
	bots map[uint]*bot
}

// New builds the manager. Nothing runs until Start.
func New(s *shop.Shop, clients *service.Clients, log *slog.Logger) *Manager {
	return &Manager{
		shop:    s,
		clients: clients,
		log:     log,
		bots:    map[uint]*bot{},
	}
}

// Start boots every enabled bot and keeps them running until ctx ends.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()

	cfgs, err := m.shop.Configs(ctx)
	if err != nil {
		m.log.Warn("shop bots could not read their configs", "error", err)
		return
	}
	for i := range cfgs {
		if cfgs[i].Enabled && cfgs[i].Token != "" {
			m.startBot(cfgs[i])
		}
	}
	// The reminder scan rides the same context as the bots: it
	// starts with the fleet and stops when the manager's context
	// ends, which is the only stop the manager has.
	go m.reminderLoop(ctx)
}

// Reload brings one operator's bot in line with its stored config after
// a save: a bot that should not run is stopped, one that should is
// started, and a token change restarts it — a token is the bot's whole
// identity, so there is nothing to patch in place. Other changes (card
// number, welcome text, admin chat) are picked up in place; the bot
// reads them from the stored config each time it acts.
func (m *Manager) Reload(operatorID uint) {
	ctx := m.rootCtx()
	if ctx == nil {
		// Start has not run: when it does, it boots from the stored
		// configs, which are already what this save wrote.
		return
	}
	cfg, err := m.shop.ConfigFor(ctx, operatorID)
	if err != nil {
		m.log.Warn("shop bot reload could not read the config", "operator", operatorID, "error", err)
		return
	}
	want := cfg != nil && cfg.Enabled && cfg.Token != ""

	m.mu.Lock()
	b, running := m.bots[operatorID]
	m.mu.Unlock()

	if running && (!want || b.token() != cfg.Token) {
		m.stopBot(operatorID)
		running = false
	}
	if want && !running {
		m.startBot(*cfg)
	} else if running {
		b.setConfig(*cfg)
	}
}

// BotInfo reports a bot's username — learned from Telegram when it
// started — and whether it is running.
func (m *Manager) BotInfo(operatorID uint) (username string, running bool) {
	m.mu.Lock()
	b := m.bots[operatorID]
	m.mu.Unlock()
	if b == nil {
		return "", false
	}
	name, _ := b.username.Load().(string)
	return name, true
}

// SendAnnouncement carries one operator's message to their buyers
// through their own bot: to the one buyer buyerTGID names, or — at
// zero, which is nobody's Telegram id — to every buyer the operator
// has. It returns how many buyers the message reached.
//
// The bot that sends it is the running one, and there is no queue to
// fall back on: an announcement is the operator speaking now, so a bot
// that is switched off is a refusal with the reason on it, not a
// message saved for later. A broadcast walks its buyers one at a time,
// a breath apart — a bot that fires a hundred messages in one instant
// is a bot Telegram rate-limits, and one buyer's dead chat (they
// blocked the bot, as buyers do) is logged and passed over, never a
// reason the rest hear nothing.
func (m *Manager) SendAnnouncement(ctx context.Context, operatorID uint, buyerTGID int64, text string) (int, error) {
	m.mu.Lock()
	b := m.bots[operatorID]
	m.mu.Unlock()
	if b == nil {
		return 0, errAnnounceBotOff
	}
	msg := txtAnnouncePrefix + text

	if buyerTGID != 0 {
		if _, err := b.api.sendMessage(ctx, buyerTGID, msg, nil); err != nil {
			return 0, err
		}
		return 1, nil
	}

	buyers, err := m.shop.BuyersFor(ctx, operatorID)
	if err != nil {
		return 0, err
	}
	if len(buyers) == 0 {
		return 0, errAnnounceNoBuyers
	}
	sent := 0
	for i := range buyers {
		if i > 0 && !sleepCtx(ctx, 60*time.Millisecond) {
			break
		}
		if _, err := b.api.sendMessage(ctx, buyers[i].TelegramID, msg, nil); err != nil {
			m.log.Warn("shop bot could not deliver an announcement",
				"operator", operatorID, "buyer", buyers[i].TelegramID, "error", err)
			continue
		}
		sent++
	}
	return sent, nil
}

func (m *Manager) rootCtx() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ctx
}

// startBot launches one operator's bot. A bot already running under the
// same operator is replaced.
func (m *Manager) startBot(cfg shop.Config) {
	ctx := m.rootCtx()
	if ctx == nil {
		return
	}
	m.stopBot(cfg.OperatorID)

	bctx, cancel := context.WithCancel(ctx)
	b := &bot{
		mgr:      m,
		api:      newTGClient(cfg.Token),
		log:      m.log.With("shopbot", cfg.OperatorID),
		cancel:   cancel,
		done:     make(chan struct{}),
		sessions: map[int64]*session{},
		relay:    map[int64]int64{},
	}
	b.setConfig(cfg)
	m.mu.Lock()
	m.bots[cfg.OperatorID] = b
	m.mu.Unlock()
	go b.run(bctx)
}

// stopBot stops one operator's bot and waits for its loop to end. The
// loop's requests are bound to its context, so the wait is short.
func (m *Manager) stopBot(operatorID uint) {
	m.mu.Lock()
	b := m.bots[operatorID]
	delete(m.bots, operatorID)
	m.mu.Unlock()
	if b == nil {
		return
	}
	b.cancel()
	<-b.done
}

// bot is one operator's running bot. Its conversation state — sessions
// and the support relay — lives here and is only ever touched by the
// bot's own goroutine, so it needs no locks; what other goroutines read
// (the config, the username) goes through atomics.
type bot struct {
	mgr *Manager
	api *tgClient
	log *slog.Logger

	cancel context.CancelFunc
	done   chan struct{}

	cfgA     atomic.Value // shop.Config
	username atomic.Value // string
	lastErr  atomic.Value // string

	sessions map[int64]*session // chat id -> where that buyer is in a flow
	relay    map[int64]int64    // seller-chat message id -> buyer chat id
}

func (b *bot) cfg() shop.Config {
	c, _ := b.cfgA.Load().(shop.Config)
	return c
}

func (b *bot) setConfig(cfg shop.Config) { b.cfgA.Store(cfg) }

func (b *bot) token() string { return b.cfg().Token }

// run is the bot's life: learn who it is, clear any webhook that would
// starve the polling, then poll until the context ends. Failures retry
// with a backoff rather than dying — an operator who pastes a token a
// minute before Telegram accepts it, or whose network blips, should get
// a working bot without anyone restarting anything.
func (b *bot) run(ctx context.Context) {
	defer close(b.done)

	backoff := 5 * time.Second
	for {
		me, err := b.api.getMe(ctx)
		if err == nil {
			b.username.Store(me.Username)
			break
		}
		if ctx.Err() != nil {
			return
		}
		b.lastErr.Store(err.Error())
		b.log.Warn("shop bot cannot reach Telegram yet", "error", err)
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, 60*time.Second)
	}
	// Best effort: a leftover webhook means polling hears nothing.
	if err := b.api.deleteWebhook(ctx); err != nil && ctx.Err() == nil {
		b.log.Warn("shop bot could not clear its webhook", "error", err)
	}

	var offset int64
	backoff = 5 * time.Second
	for {
		updates, err := b.api.getUpdates(ctx, offset, 30)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			b.lastErr.Store(err.Error())
			b.log.Warn("shop bot polling failed", "error", err)
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, 60*time.Second)
			continue
		}
		backoff = 5 * time.Second
		for _, u := range updates {
			if u.ID >= offset {
				offset = u.ID + 1
			}
			b.handle(ctx, u)
		}
	}
}

// sleepCtx waits, or reports that the context ended first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
