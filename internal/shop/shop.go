// Package shop is the data and delivery layer of the per-operator sales
// bots.
//
// Every operator on the panel -- the owner, an admin, a reseller -- may run
// a Telegram shop of their own, under their own BotFather token, selling
// their own plans. A sale made by Hussain's bot becomes Hussain's customer:
// it is created under his identity, counts against his ceiling, and the
// money trail (his card, his gateway merchant) is his. This package holds
// what the bots share -- the configurations, the plans, the orders, and
// the one careful act of turning a paid order into a customer -- while the
// Telegram conversation itself and the panel's pages live elsewhere and
// call in through here.
//
// Nothing in this package talks to Telegram. An order arrives already
// written down, and delivery is a service call, so the whole life of a
// sale can be tested without a bot on the other end of it.
package shop

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/abolfazl/w-ui/internal/database"
	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/fault"
	"github.com/abolfazl/w-ui/internal/operators"
	"github.com/abolfazl/w-ui/internal/service"
)

// Order statuses: where a sale stands. They are strings rather than an
// enum type because the order row is the record other parts of the panel
// read directly, and a status that prints as itself in the database is one
// nobody has to look up.
const (
	StatusUnpaid          = "unpaid"           // chosen, not yet paid for
	StatusAwaitingReceipt = "awaiting_receipt" // card chosen, receipt not sent
	StatusPendingReview   = "pending_review"   // receipt sent, operator to approve
	StatusPaid            = "paid"             // money confirmed, not yet delivered
	StatusFulfilled       = "fulfilled"        // delivered: the customer exists
	StatusRejected        = "rejected"         // the operator refused the payment
	StatusExpired         = "expired"          // never paid for in time
	StatusFailed          = "failed"           // delivery was attempted and could not be made
)

// Order kinds and payment methods, as stored on the order.
const (
	KindNew   = "new"
	KindRenew = "renew"
	KindTopup = "topup"

	MethodCard     = "card"
	MethodZarinpal = "zarinpal"
	MethodWallet   = "wallet"
)

// Code kinds, as stored on a code: what the code does at the till.
const (
	CodePercent = "percent" // Value percent off a price
	CodeFixed   = "fixed"   // Value Toman off a price
	CodeGift    = "gift"    // Value Toman into the redeemer's wallet
)

// The shop's two sections. A buyer chooses one before they see a plan --
// WireGuard for the tunnels this panel runs itself, V2ray for the 3x-ui
// inbounds -- because the two are different products to the person
// holding the phone: different apps, different configs, and a customer
// who bought one and was handed the other has been sold the wrong thing.
const (
	SectionWireGuard = "wireguard"
	SectionV2ray     = "v2ray"
)

// NormalizeSection is a stored section read back: the one true spelling
// of each, and anything that is not V2ray is WireGuard -- plans and
// orders written before sections existed carry nothing, and what they
// always sold was WireGuard.
func NormalizeSection(s string) string {
	if s == SectionV2ray {
		return SectionV2ray
	}
	return SectionWireGuard
}

// publicBaseKey is the settings key holding where this panel's
// subscription links live, panel-wide: the bots are per-operator, but the
// link a buyer is handed points at the one subscription listener.
const publicBaseKey = "shop.publicBase"

// Shop is the store and the delivery hand.
type Shop struct {
	db      *gorm.DB
	clients *service.Clients
	admins  *operators.Admins
	log     *slog.Logger
}

// New builds a Shop over the panel's database and customer service.
func New(db *gorm.DB, clients *service.Clients, log *slog.Logger) *Shop {
	return &Shop{db: db, clients: clients, admins: operators.NewAdmins(db, log), log: log}
}

// Config is one operator's bot as the rest of the panel works with it: the
// model row, with the tunnel list as the list it is rather than the JSON
// text it is stored as.
type Config struct {
	OperatorID       uint
	Enabled          bool
	Token            string
	AdminChatID      int64
	CardNumber       string
	CardHolder       string
	ZarinpalMerchant string
	Welcome          string
	InterfaceIDs     []uint

	// The shop's teaching words, offered by the bot when written:
	// how to connect, and the answers to the questions every buyer
	// asks. Empty is not offered.
	TutorialText string
	FaqText      string

	// The shop's growth settings: the trial it offers, how its
	// customers are named, and the cashback it pays referrers.
	TrialEnabled    bool
	TrialBytes      uint64
	TrialDays       int
	TrialSection    string
	NameFormat      string
	CashbackPercent int
	WheelEnabled    bool
}

func configFromRow(r *model.ShopBot) *Config {
	c := &Config{
		OperatorID:       r.OperatorID,
		Enabled:          r.Enabled,
		Token:            r.Token,
		AdminChatID:      r.AdminChatID,
		CardNumber:       r.CardNumber,
		CardHolder:       r.CardHolder,
		ZarinpalMerchant: r.ZarinpalMerchant,
		Welcome:          r.Welcome,
		TutorialText:     r.TutorialText,
		FaqText:          r.FaqText,
		TrialEnabled:     r.TrialEnabled,
		TrialBytes:       r.TrialBytes,
		TrialDays:        r.TrialDays,
		TrialSection:     r.TrialSection,
		NameFormat:       r.NameFormat,
		CashbackPercent:  r.CashbackPercent,
		WheelEnabled:     r.WheelEnabled,
	}
	if r.InterfaceIDs != "" {
		// A list that does not parse is a list of nothing, not an error the
		// bot should die of: the row was written by this package, and if it
		// is ever damaged the operator's page is where that gets fixed.
		_ = json.Unmarshal([]byte(r.InterfaceIDs), &c.InterfaceIDs)
	}
	return c
}

func (c *Config) row() (*model.ShopBot, error) {
	raw, err := json.Marshal(c.InterfaceIDs)
	if err != nil {
		return nil, fmt.Errorf("shop: encode the tunnel list: %w", err)
	}
	return &model.ShopBot{
		OperatorID:       c.OperatorID,
		Enabled:          c.Enabled,
		Token:            c.Token,
		AdminChatID:      c.AdminChatID,
		CardNumber:       c.CardNumber,
		CardHolder:       c.CardHolder,
		ZarinpalMerchant: c.ZarinpalMerchant,
		Welcome:          c.Welcome,
		TutorialText:     c.TutorialText,
		FaqText:          c.FaqText,
		InterfaceIDs:     string(raw),
		TrialEnabled:     c.TrialEnabled,
		TrialBytes:       c.TrialBytes,
		TrialDays:        c.TrialDays,
		TrialSection:     c.TrialSection,
		NameFormat:       c.NameFormat,
		CashbackPercent:  c.CashbackPercent,
		WheelEnabled:     c.WheelEnabled,
	}, nil
}

// ConfigFor reads one operator's bot configuration. An operator who has
// never set one up gets the zero Config -- disabled, no token -- rather
// than an error: "no shop yet" is the ordinary state of most operators,
// and every caller would only translate the error back into that.
func (s *Shop) ConfigFor(ctx context.Context, operatorID uint) (*Config, error) {
	var row model.ShopBot
	err := s.db.WithContext(ctx).Where("operator_id = ?", operatorID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return &Config{OperatorID: operatorID}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("shop: read bot config: %w", err)
	}
	return configFromRow(&row), nil
}

// SaveConfig writes an operator's bot configuration, creating the row the
// first time and replacing it after that. One row per operator is the
// table's own constraint; this is the upsert that honours it.
func (s *Shop) SaveConfig(ctx context.Context, c *Config) error {
	if c.OperatorID == 0 {
		return fault.Field("operatorId", "a bot belongs to an operator")
	}
	row, err := c.row()
	if err != nil {
		return err
	}
	db := s.db.WithContext(ctx)
	var existing model.ShopBot
	err = db.Where("operator_id = ?", c.OperatorID).First(&existing).Error
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		if err := db.Create(row).Error; err != nil {
			return fmt.Errorf("shop: save bot config: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("shop: read bot config: %w", err)
	}
	row.ID = existing.ID
	row.CreatedAt = existing.CreatedAt
	if err := db.Save(row).Error; err != nil {
		return fmt.Errorf("shop: save bot config: %w", err)
	}
	return nil
}

// Configs reads every operator's bot configuration, for the bot manager
// starting the fleet: which bots exist and are switched on is a question
// about all of them at once.
func (s *Shop) Configs(ctx context.Context) ([]Config, error) {
	var rows []model.ShopBot
	if err := s.db.WithContext(ctx).Order("operator_id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("shop: read bot configs: %w", err)
	}
	out := make([]Config, 0, len(rows))
	for i := range rows {
		out = append(out, *configFromRow(&rows[i]))
	}
	return out, nil
}

// Plan is one sellable plan of one operator's shop.
type Plan struct {
	ID          uint
	OperatorID  uint
	Name        string
	Section     string
	QuotaBytes  uint64
	Days        int
	PriceToman  int64
	DeviceLimit int
	Enabled     bool
	Sort        int
}

func planFromRow(r *model.ShopPlan) *Plan {
	return &Plan{
		ID: r.ID, OperatorID: r.OperatorID, Name: r.Name, Section: r.Section,
		QuotaBytes: r.QuotaBytes, Days: r.Days, PriceToman: r.PriceToman,
		DeviceLimit: r.DeviceLimit, Enabled: r.Enabled, Sort: r.Sort,
	}
}

func (p *Plan) row() *model.ShopPlan {
	return &model.ShopPlan{
		ID: p.ID, OperatorID: p.OperatorID, Name: p.Name, Section: p.Section,
		QuotaBytes: p.QuotaBytes, Days: p.Days, PriceToman: p.PriceToman,
		DeviceLimit: p.DeviceLimit, Enabled: p.Enabled, Sort: p.Sort,
	}
}

// PlansFor lists an operator's plans in the order the bot offers them.
// enabledOnly is the bot's view; the operator's own page wants them all,
// switched-off ones included, to switch them back on.
func (s *Shop) PlansFor(ctx context.Context, operatorID uint, enabledOnly bool) ([]Plan, error) {
	q := s.db.WithContext(ctx).Where("operator_id = ?", operatorID)
	if enabledOnly {
		q = q.Where("enabled = ?", true)
	}
	var rows []model.ShopPlan
	if err := q.Order("sort, id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("shop: read plans: %w", err)
	}
	out := make([]Plan, 0, len(rows))
	for i := range rows {
		out = append(out, *planFromRow(&rows[i]))
	}
	return out, nil
}

// SavePlan writes a plan: a new one when its ID is zero, the operator's own
// existing one otherwise. A plan id belonging to another operator is
// refused rather than quietly reassigned -- an id is the one thing a
// caller could get wrong, or be made to send, across the line between
// sellers, and the write is where that has to stop.
func (s *Shop) SavePlan(ctx context.Context, p *Plan) error {
	if p.OperatorID == 0 {
		return fault.Field("operatorId", "a plan belongs to an operator")
	}
	if strings.TrimSpace(p.Name) == "" {
		return fault.Field("name", "name is required")
	}
	// A section the shop does not have is a plan no buyer can ever be
	// offered -- the bot shows a section's plans under its own button --
	// so it is refused here rather than saved to sit unsold forever. An
	// empty one is the plans saved before sections existed: WireGuard.
	switch p.Section {
	case "", SectionWireGuard, SectionV2ray:
		p.Section = NormalizeSection(p.Section)
	default:
		return fault.Field("section", "the section must be wireguard or v2ray")
	}
	if p.PriceToman < 0 {
		return fault.Field("priceToman", "a price cannot be negative")
	}
	if p.Days < 0 {
		return fault.Field("days", "a plan's days cannot be negative")
	}
	if p.DeviceLimit < 0 {
		return fault.Field("deviceLimit", "a device limit cannot be negative")
	}
	db := s.db.WithContext(ctx)
	if p.ID == 0 {
		row := p.row()
		if err := db.Create(row).Error; err != nil {
			return fmt.Errorf("shop: save plan: %w", err)
		}
		p.ID = row.ID
		return nil
	}
	var existing model.ShopPlan
	if err := db.First(&existing, p.ID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: plan %d", fault.ErrNotFound, p.ID)
		}
		return fmt.Errorf("shop: read plan: %w", err)
	}
	if existing.OperatorID != p.OperatorID {
		return fault.Field("operatorId", "that plan belongs to another operator")
	}
	row := p.row()
	row.CreatedAt = existing.CreatedAt
	if err := db.Save(row).Error; err != nil {
		return fmt.Errorf("shop: save plan: %w", err)
	}
	return nil
}

// DeletePlan removes one of the operator's plans. Orders already taken
// keep their copy of its terms, so deleting a plan a buyer is mid-payment
// on does not strand the sale.
func (s *Shop) DeletePlan(ctx context.Context, operatorID, planID uint) error {
	res := s.db.WithContext(ctx).
		Where("id = ? AND operator_id = ?", planID, operatorID).
		Delete(&model.ShopPlan{})
	if res.Error != nil {
		return fmt.Errorf("shop: delete plan: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("%w: plan %d", fault.ErrNotFound, planID)
	}
	return nil
}

// Order is one sale, mirroring its row.
type Order struct {
	ID         uint
	OperatorID uint

	BuyerTGID int64
	BuyerName string

	PlanID      uint
	PlanName    string
	Section     string
	QuotaBytes  uint64
	Days        int
	PriceToman  int64
	DeviceLimit int

	// Quantity is how many of the plan this order buys; Code is the
	// code the buyer named at checkout and DiscountToman what it took
	// off, both copied onto the order as the plan's terms were.
	Quantity      int
	UnitsDone     int
	Code          string
	DiscountToman int64

	Kind     string
	ClientID uint

	Method string
	Status string

	ReceiptFileID string
	Authority     string
	RefID         string

	// HasReceipt says the buyer's receipt picture is kept in the panel
	// (SaveReceipt), apart from the file id above that names it on
	// Telegram. It is worked out from the receipts table when an order
	// is read, never stored on the order: two records of one fact would
	// disagree the first time a receipt is replaced.
	HasReceipt bool

	PublicToken string

	CreatedAt time.Time
	UpdatedAt time.Time
}

func orderFromRow(r *model.ShopOrder) *Order {
	return &Order{
		ID: r.ID, OperatorID: r.OperatorID,
		BuyerTGID: r.BuyerTGID, BuyerName: r.BuyerName,
		PlanID: r.PlanID, PlanName: r.PlanName, Section: r.Section,
		QuotaBytes: r.QuotaBytes,
		Days:       r.Days, PriceToman: r.PriceToman, DeviceLimit: r.DeviceLimit,
		Quantity: r.Quantity, UnitsDone: r.UnitsDone, Code: r.Code, DiscountToman: r.DiscountToman,
		Kind: r.Kind, ClientID: r.ClientID,
		Method: r.Method, Status: r.Status,
		ReceiptFileID: r.ReceiptFileID, Authority: r.Authority, RefID: r.RefID,
		PublicToken: r.PublicToken,
		CreatedAt:   r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

func (o *Order) row() *model.ShopOrder {
	return &model.ShopOrder{
		ID: o.ID, OperatorID: o.OperatorID,
		BuyerTGID: o.BuyerTGID, BuyerName: o.BuyerName,
		PlanID: o.PlanID, PlanName: o.PlanName, Section: o.Section,
		QuotaBytes: o.QuotaBytes,
		Days:       o.Days, PriceToman: o.PriceToman, DeviceLimit: o.DeviceLimit,
		Quantity: o.Quantity, UnitsDone: o.UnitsDone, Code: o.Code, DiscountToman: o.DiscountToman,
		Kind: o.Kind, ClientID: o.ClientID,
		Method: o.Method, Status: o.Status,
		ReceiptFileID: o.ReceiptFileID, Authority: o.Authority, RefID: o.RefID,
		PublicToken: o.PublicToken,
		CreatedAt:   o.CreatedAt, UpdatedAt: o.UpdatedAt,
	}
}

// newPublicToken draws the name an order goes by in public. Random rather
// than the row's id, so the one link a sale exposes -- the payment
// callback -- cannot be walked to the next order by counting.
func newPublicToken() (string, error) {
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("shop: draw an order token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// CreateOrder writes a new order down, drawing its public token when the
// caller did not bring one.
func (s *Shop) CreateOrder(ctx context.Context, o *Order) error {
	if o.OperatorID == 0 {
		return fault.Field("operatorId", "an order belongs to an operator")
	}
	// An order that does not say how many it buys buys one: the column
	// has the same default, and the delivery reads the order rather
	// than the row it became, so the two are kept saying the same thing.
	if o.Quantity <= 0 {
		o.Quantity = 1
	}
	if o.PublicToken == "" {
		token, err := newPublicToken()
		if err != nil {
			return err
		}
		o.PublicToken = token
	}
	row := o.row()
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return fmt.Errorf("shop: create order: %w", err)
	}
	*o = *orderFromRow(row)
	return nil
}

// OrderByID reads one order.
func (s *Shop) OrderByID(ctx context.Context, id uint) (*Order, error) {
	var row model.ShopOrder
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: order %d", fault.ErrNotFound, id)
		}
		return nil, fmt.Errorf("shop: read order: %w", err)
	}
	order := orderFromRow(&row)
	if err := s.markReceipts(ctx, order); err != nil {
		return nil, err
	}
	return order, nil
}

// markReceipts fills HasReceipt on each order from the receipts table:
// which of these orders has its buyer's picture kept in the panel is one
// question about the table, asked once for the whole list rather than
// once per order on a page of a hundred.
func (s *Shop) markReceipts(ctx context.Context, orders ...*Order) error {
	ids := make([]uint, 0, len(orders))
	for _, o := range orders {
		o.HasReceipt = false
		ids = append(ids, o.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	var kept []uint
	if err := s.db.WithContext(ctx).Model(&model.ShopReceipt{}).
		Where("order_id IN ?", ids).Pluck("order_id", &kept).Error; err != nil {
		return fmt.Errorf("shop: read receipts: %w", err)
	}
	has := make(map[uint]bool, len(kept))
	for _, id := range kept {
		has[id] = true
	}
	for _, o := range orders {
		o.HasReceipt = has[o.ID]
	}
	return nil
}

// OrderByToken reads the order a public link names -- the payment
// gateway's callback carries the token and nothing else it can be trusted
// with.
func (s *Shop) OrderByToken(ctx context.Context, publicToken string) (*Order, error) {
	var row model.ShopOrder
	err := s.db.WithContext(ctx).Where("public_token = ?", publicToken).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: order for that token", fault.ErrNotFound)
		}
		return nil, fmt.Errorf("shop: read order: %w", err)
	}
	return orderFromRow(&row), nil
}

// UpdateOrder writes an order back: the status it has reached, the receipt
// or gateway trail it picked up, the customer it became.
func (s *Shop) UpdateOrder(ctx context.Context, o *Order) error {
	row := o.row()
	if err := s.db.WithContext(ctx).Save(row).Error; err != nil {
		return fmt.Errorf("shop: save order: %w", err)
	}
	o.UpdatedAt = row.UpdatedAt
	return nil
}

// OrdersFor lists an operator's orders, newest first. A status narrows the
// list to one stage of the pipeline -- the pending ones are the operator's
// to-do list -- and an empty status is all of them. A limit of zero or
// less is a sane page, not everything ever: this list is read by pages and
// bots, and an unbounded answer helps neither.
func (s *Shop) OrdersFor(ctx context.Context, operatorID uint, status string, limit int) ([]Order, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := s.db.WithContext(ctx).Where("operator_id = ?", operatorID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var rows []model.ShopOrder
	if err := q.Order("id desc").Limit(limit).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("shop: read orders: %w", err)
	}
	out := make([]Order, 0, len(rows))
	ptrs := make([]*Order, 0, len(rows))
	for i := range rows {
		o := orderFromRow(&rows[i])
		out = append(out, *o)
		ptrs = append(ptrs, &out[len(out)-1])
	}
	if err := s.markReceipts(ctx, ptrs...); err != nil {
		return nil, err
	}
	return out, nil
}

// Stats is an operator's shop at a glance, for their page in the panel.
type Stats struct {
	// TodaySales and TotalSales count delivered orders -- a sale is a
	// sale when the customer has it, not when it was asked for.
	TodaySales int
	TotalSales int
	// RevenueToman is what the delivered orders were sold for.
	RevenueToman int64
	// Pending counts orders still in the pipeline: unpaid, waiting for a
	// receipt, waiting for review, or paid and not yet delivered.
	Pending int
	// Buyers is how many distinct Telegram accounts hold a delivered
	// order from this operator.
	Buyers int
}

// StatsFor works an operator's numbers out of their orders.
func (s *Shop) StatsFor(ctx context.Context, operatorID uint) (Stats, error) {
	var st Stats
	// A fresh chain per number: a gorm statement that has answered one
	// aggregate carries its clauses into the next, and these five questions
	// are each about the operator's orders and nothing left over from the
	// one before.
	base := func() *gorm.DB {
		return s.db.WithContext(ctx).Model(&model.ShopOrder{}).
			Where("operator_id = ?", operatorID)
	}

	var total int64
	if err := base().Where("status = ?", StatusFulfilled).Count(&total).Error; err != nil {
		return st, fmt.Errorf("shop: count sales: %w", err)
	}
	st.TotalSales = int(total)

	// Today by the panel's clock, which is UTC everywhere in this
	// database: an operator's midnight may differ, and a number that
	// changes meaning with the reader's timezone is worse than a plain
	// one everybody can reproduce.
	dayStart := time.Now().UTC().Truncate(24 * time.Hour)
	var today int64
	if err := base().Where("status = ? AND updated_at >= ?", StatusFulfilled, dayStart).
		Count(&today).Error; err != nil {
		return st, fmt.Errorf("shop: count today's sales: %w", err)
	}
	st.TodaySales = int(today)

	var revenue struct{ Sum int64 }
	if err := base().Select("COALESCE(SUM(price_toman), 0) AS sum").
		Where("status = ?", StatusFulfilled).Scan(&revenue).Error; err != nil {
		return st, fmt.Errorf("shop: sum revenue: %w", err)
	}
	st.RevenueToman = revenue.Sum

	var pending int64
	if err := base().Where("status IN ?",
		[]string{StatusUnpaid, StatusAwaitingReceipt, StatusPendingReview, StatusPaid}).
		Count(&pending).Error; err != nil {
		return st, fmt.Errorf("shop: count pending orders: %w", err)
	}
	st.Pending = int(pending)

	var buyers int64
	if err := base().Where("status = ?", StatusFulfilled).
		Distinct("buyer_tg_id").Count(&buyers).Error; err != nil {
		return st, fmt.Errorf("shop: count buyers: %w", err)
	}
	st.Buyers = int(buyers)
	return st, nil
}

// PublicBase reads where this panel's subscription links live -- the
// origin of the subscription listener, such as https://host:12489 -- or
// empty when it has not been set. Until it is set a delivered sale has no
// link to hand over, so the bots ask before they sell.
func (s *Shop) PublicBase(ctx context.Context) (string, error) {
	value, _, err := database.GetSetting(s.db.WithContext(ctx), publicBaseKey)
	if err != nil {
		return "", err
	}
	return value, nil
}

// SetPublicBase writes the public origin, trimmed of the trailing slash
// the links are joined against it without.
func (s *Shop) SetPublicBase(ctx context.Context, base string) error {
	return database.PutSetting(s.db.WithContext(ctx), publicBaseKey, strings.TrimRight(strings.TrimSpace(base), "/"))
}

// SectionInterfaces is the tunnels of the operator's list that belong to
// one section: the WireGuard tunnels for a WireGuard sale, the 3x-ui
// inbounds for a V2ray one. The config's own order is kept, because the
// first tunnel is the one a new customer's row is labelled by. A tunnel
// on the list that has since been deleted is skipped rather than failed
// over: the list is the operator's to fix on their page, and one stale
// id should not stop every sale the shop makes.
func (s *Shop) SectionInterfaces(ctx context.Context, cfg *Config, section string) ([]uint, error) {
	if cfg == nil || len(cfg.InterfaceIDs) == 0 {
		return nil, nil
	}
	want := model.ProtocolWireGuard
	if NormalizeSection(section) == SectionV2ray {
		want = model.ProtocolXray
	}
	var rows []model.Interface
	if err := s.db.WithContext(ctx).Where("id IN ?", cfg.InterfaceIDs).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("shop: read the shop's tunnels: %w", err)
	}
	protocol := make(map[uint]model.Protocol, len(rows))
	for _, r := range rows {
		protocol[r.ID] = r.Protocol
	}
	var out []uint
	for _, id := range cfg.InterfaceIDs {
		if p, ok := protocol[id]; ok && p == want {
			out = append(out, id)
		}
	}
	return out, nil
}

// ClientSections says which of the shop's sections a customer already
// holds -- whether any of their accounts sits on a WireGuard tunnel, and
// whether any sits on a 3x-ui inbound. The bot asks before it offers a
// renewal, and the answer is read off the accounts the customer was
// loaded with: a customer handed in without accounts holds nothing.
func (s *Shop) ClientSections(ctx context.Context, client *model.Client) (wireguard, xray bool, err error) {
	if client == nil {
		return false, false, nil
	}
	seen := map[uint]bool{}
	var ids []uint
	for _, acc := range client.Accounts {
		if !seen[acc.InterfaceID] {
			seen[acc.InterfaceID] = true
			ids = append(ids, acc.InterfaceID)
		}
	}
	if len(ids) == 0 {
		return false, false, nil
	}
	var rows []model.Interface
	if err := s.db.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return false, false, fmt.Errorf("shop: read the customer's tunnels: %w", err)
	}
	for _, r := range rows {
		switch r.Protocol {
		case model.ProtocolWireGuard:
			wireguard = true
		case model.ProtocolXray:
			xray = true
		}
	}
	return wireguard, xray, nil
}

// maxReceiptBytes is the largest receipt the panel keeps: a Telegram
// photo is a few megabytes at most, and a store that takes anything
// takes a database of somebody's holiday videos one upload at a time.
const maxReceiptBytes = 8 << 20

// SaveReceipt keeps a buyer's receipt picture in the panel, replacing
// the one already kept for the order: a buyer who photographed the
// transfer badly sends a better picture, and the operator approves
// against the last one sent, not the first.
func (s *Shop) SaveReceipt(ctx context.Context, orderID uint, contentType string, data []byte) error {
	if len(data) > maxReceiptBytes {
		return fault.Field("receipt", "a receipt may be at most 8 MiB")
	}
	row := &model.ShopReceipt{
		OrderID: orderID, ContentType: contentType, Data: data,
		CreatedAt: time.Now().UTC(),
	}
	// Save on a primary key is the upsert: the row is written when there
	// is none and replaced when there is.
	if err := s.db.WithContext(ctx).Save(row).Error; err != nil {
		return fmt.Errorf("shop: keep the receipt: %w", err)
	}
	return nil
}

// ReceiptFor reads back the receipt kept for an order, for the operator
// to look at in the panel before they approve. An order with none -- a
// gateway sale, or a card sale the buyer paid some other way -- is
// ErrNotFound, not an empty picture.
func (s *Shop) ReceiptFor(ctx context.Context, orderID uint) (string, []byte, error) {
	var row model.ShopReceipt
	if err := s.db.WithContext(ctx).First(&row, "order_id = ?", orderID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", nil, fmt.Errorf("%w: order %d has no receipt kept", fault.ErrNotFound, orderID)
		}
		return "", nil, fmt.Errorf("shop: read the receipt: %w", err)
	}
	return row.ContentType, row.Data, nil
}

// Buyer is one person who has bought from an operator, as the panel's
// buyer list shows them: the customer they are now -- name, standing,
// what is left of their plan -- with how much business they have done.
type Buyer struct {
	ClientID    uint
	Name        string
	TelegramID  int64
	Status      string
	ExpiresAt   *time.Time
	UsedBytes   uint64
	QuotaBytes  uint64
	Orders      int
	LastOrderAt time.Time
}

// BuyersFor lists an operator's buyers, newest business first. A buyer
// is a Telegram account with a delivered order from this operator; the
// standing shown is their customer's, read under the operator's own
// scope, so the list is exactly the customers the sales made. A buyer
// whose customer has since been deleted is not listed: there is nobody
// left to show, and a row that opens onto nothing is worse than none.
func (s *Shop) BuyersFor(ctx context.Context, operatorID uint) ([]Buyer, error) {
	opCtx, err := s.OperatorCtx(ctx, operatorID)
	if err != nil {
		return nil, err
	}
	// The business first: how many delivered orders each Telegram
	// account holds from this operator, and which order was the latest.
	// The latest is named by its id -- ids grow as orders are placed --
	// and its placed-at time is then read off the row itself: an
	// aggregate MAX over the timestamp column comes back as text on
	// SQLite and a timestamp on PostgreSQL, and a list that only builds
	// on one database is a list that breaks on the other.
	type business struct {
		BuyerTGID int64 `gorm:"column:buyer_tg_id"`
		Orders    int   `gorm:"column:orders"`
		LastID    uint  `gorm:"column:last_id"`
	}
	var rows []business
	if err := s.db.WithContext(ctx).Model(&model.ShopOrder{}).
		Select("buyer_tg_id, COUNT(*) AS orders, MAX(id) AS last_id").
		Where("operator_id = ? AND status = ?", operatorID, StatusFulfilled).
		Group("buyer_tg_id").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("shop: read the buyers: %w", err)
	}
	lastIDs := make([]uint, 0, len(rows))
	for _, r := range rows {
		lastIDs = append(lastIDs, r.LastID)
	}
	placedAt := make(map[uint]time.Time, len(rows))
	if len(lastIDs) > 0 {
		var latest []model.ShopOrder
		if err := s.db.WithContext(ctx).Where("id IN ?", lastIDs).Find(&latest).Error; err != nil {
			return nil, fmt.Errorf("shop: read the buyers' latest orders: %w", err)
		}
		for i := range latest {
			placedAt[latest[i].ID] = latest[i].CreatedAt
		}
	}
	out := make([]Buyer, 0, len(rows))
	for _, r := range rows {
		found, err := s.clients.ByTelegramID(opCtx, r.BuyerTGID)
		if err != nil {
			return nil, err
		}
		if len(found) == 0 {
			continue
		}
		client := &found[0]
		out = append(out, Buyer{
			ClientID: client.ID, Name: client.Name,
			TelegramID: client.TelegramID, Status: string(client.Status),
			ExpiresAt: client.ExpiresAt,
			UsedBytes: client.UsedBytes, QuotaBytes: client.QuotaBytes,
			Orders: r.Orders, LastOrderAt: placedAt[r.LastID],
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].LastOrderAt.After(out[j].LastOrderAt)
	})
	return out, nil
}
