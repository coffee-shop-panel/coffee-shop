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

	MethodCard     = "card"
	MethodZarinpal = "zarinpal"
)

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
		InterfaceIDs:     string(raw),
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
	QuotaBytes  uint64
	Days        int
	PriceToman  int64
	DeviceLimit int
	Enabled     bool
	Sort        int
}

func planFromRow(r *model.ShopPlan) *Plan {
	return &Plan{
		ID: r.ID, OperatorID: r.OperatorID, Name: r.Name,
		QuotaBytes: r.QuotaBytes, Days: r.Days, PriceToman: r.PriceToman,
		DeviceLimit: r.DeviceLimit, Enabled: r.Enabled, Sort: r.Sort,
	}
}

func (p *Plan) row() *model.ShopPlan {
	return &model.ShopPlan{
		ID: p.ID, OperatorID: p.OperatorID, Name: p.Name,
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
	QuotaBytes  uint64
	Days        int
	PriceToman  int64
	DeviceLimit int

	Kind     string
	ClientID uint

	Method string
	Status string

	ReceiptFileID string
	Authority     string
	RefID         string

	PublicToken string

	CreatedAt time.Time
	UpdatedAt time.Time
}

func orderFromRow(r *model.ShopOrder) *Order {
	return &Order{
		ID: r.ID, OperatorID: r.OperatorID,
		BuyerTGID: r.BuyerTGID, BuyerName: r.BuyerName,
		PlanID: r.PlanID, PlanName: r.PlanName, QuotaBytes: r.QuotaBytes,
		Days: r.Days, PriceToman: r.PriceToman, DeviceLimit: r.DeviceLimit,
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
		PlanID: o.PlanID, PlanName: o.PlanName, QuotaBytes: o.QuotaBytes,
		Days: o.Days, PriceToman: o.PriceToman, DeviceLimit: o.DeviceLimit,
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
	return orderFromRow(&row), nil
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
	for i := range rows {
		out = append(out, *orderFromRow(&rows[i]))
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
