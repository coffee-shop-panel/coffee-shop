// Package adminbot is the panel's management bots: one Telegram bot
// per operator, each run from that operator's own BotFather token,
// through which the operator manages their own customers — listing
// them, adding them, cutting them off, extending them — without
// opening the panel.
//
// It is a second, separate bot family beside the sales bots
// (internal/shopbot). A sales bot talks to strangers: anyone may open
// it and buy. A management bot talks to exactly one person — its
// operator, named by their Telegram id in the config — and refuses
// everyone else, because what it can do is what the operator can do,
// and an operator's reach over their customers is not a thing to hand
// to whoever finds the bot's username. The two families share the
// panel's data layer (internal/shop for scope and links, the customer
// service for the work itself) and nothing else: not a token, not a
// config row, not a running process.
package adminbot

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/service"
	"github.com/abolfazl/w-ui/internal/shop"
)

// Manager runs every operator's management bot and holds their store.
// Bots are independent on purpose: one operator's dead token must cost
// that operator their bot and nobody else's, so each bot polls in its
// own goroutine and retries on its own backoff.
type Manager struct {
	db      *gorm.DB
	clients *service.Clients
	shop    *shop.Shop
	log     *slog.Logger

	mu   sync.Mutex
	ctx  context.Context // the root the bots' contexts hang from; set by Start
	bots map[uint]*bot
}

// New builds the manager over the panel's database, its customer
// service and the shop's store — the shop because the scope a bot acts
// under and the origin subscription links are built from are the
// shop's to give, the same ones a sale is delivered under. Nothing
// runs until Start.
func New(db *gorm.DB, clients *service.Clients, s *shop.Shop, log *slog.Logger) *Manager {
	return &Manager{
		db:      db,
		clients: clients,
		shop:    s,
		log:     log,
		bots:    map[uint]*bot{},
	}
}

// Start boots every enabled bot and keeps them running until ctx ends.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	m.ctx = ctx
	m.mu.Unlock()

	cfgs, err := m.configs(ctx)
	if err != nil {
		m.log.Warn("management bots could not read their configs", "error", err)
		return
	}
	for i := range cfgs {
		if cfgs[i].Enabled && cfgs[i].Token != "" {
			m.startBot(cfgs[i])
		}
	}
}

// Reload brings one operator's bot in line with its stored config after
// a save: a bot that should not run is stopped, one that should is
// started, and a token change restarts it — a token is the bot's whole
// identity, so there is nothing to patch in place. Other changes (the
// admin chat, above all) are picked up in place; the bot reads them
// from the stored config each time it acts.
func (m *Manager) Reload(operatorID uint) {
	ctx := m.rootCtx()
	if ctx == nil {
		// Start has not run: when it does, it boots from the stored
		// configs, which are already what this save wrote.
		return
	}
	cfg, err := m.ConfigFor(ctx, operatorID)
	if err != nil {
		m.log.Warn("management bot reload could not read the config", "operator", operatorID, "error", err)
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

func (m *Manager) rootCtx() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ctx
}

// startBot launches one operator's bot. A bot already running under the
// same operator is replaced.
func (m *Manager) startBot(cfg Config) {
	ctx := m.rootCtx()
	if ctx == nil {
		return
	}
	m.stopBot(cfg.OperatorID)

	bctx, cancel := context.WithCancel(ctx)
	b := &bot{
		mgr:    m,
		api:    newTGClient(cfg.Token),
		log:    m.log.With("adminbot", cfg.OperatorID),
		cancel: cancel,
		done:   make(chan struct{}),
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

// bot is one operator's running bot. What other goroutines read — the
// config, the username — goes through atomics; the conversation itself
// runs on the bot's own goroutine.
//
// The surface the conversation (flow.go) builds on: api is the bot's
// Telegram client; cfg() is its live config; clients and shop are the
// manager's services; opCtx, publicBase and subLink below are the three
// questions every management action asks — under whose scope, and where
// do links live.
type bot struct {
	mgr *Manager
	api *tgClient
	log *slog.Logger

	cancel context.CancelFunc
	done   chan struct{}

	cfgA     atomic.Value // Config
	username atomic.Value // string
	lastErr  atomic.Value // string
}

func (b *bot) cfg() Config {
	c, _ := b.cfgA.Load().(Config)
	return c
}

func (b *bot) setConfig(cfg Config) { b.cfgA.Store(cfg) }

func (b *bot) token() string { return b.cfg().Token }

// clients is the panel's customer service, the hands every management
// action is done with.
func (b *bot) clients() *service.Clients { return b.mgr.clients }

// opCtx builds the context this bot's operator acts under — the scope
// the shop builds from the operator's row, the same one their sales
// are delivered under — so a customer the bot lists, extends or cuts
// off is fenced exactly as if the operator had done it in the panel.
func (b *bot) opCtx(ctx context.Context) (context.Context, error) {
	return b.mgr.shop.OperatorCtx(ctx, b.cfg().OperatorID)
}

// publicBase reads where this panel's subscription links live, or
// empty when it has not been set: a customer the bot shows has a link
// to hand over only when the panel has one.
func (b *bot) publicBase(ctx context.Context) (string, error) {
	return b.mgr.shop.PublicBase(ctx)
}

// subLink is a customer's subscription link, the shop's own joining of
// the public base and the customer's token; empty when either half is
// missing.
func (b *bot) subLink(ctx context.Context, client *model.Client) string {
	return b.mgr.shop.SubLink(ctx, client)
}

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
		b.log.Warn("management bot cannot reach Telegram yet", "error", err)
		if !sleepCtx(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, 60*time.Second)
	}
	// Best effort: a leftover webhook means polling hears nothing.
	if err := b.api.deleteWebhook(ctx); err != nil && ctx.Err() == nil {
		b.log.Warn("management bot could not clear its webhook", "error", err)
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
			b.log.Warn("management bot polling failed", "error", err)
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
