package shop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/fault"
	"github.com/abolfazl/w-ui/internal/operators"
	"github.com/abolfazl/w-ui/internal/service"
)

// OperatorCtx builds the context an operator's own requests are served
// under, from their row alone: the caller the API layer would attach, and
// the scope the service layer narrows by. Everything done under it -- a
// customer created for a sale -- is stamped and fenced as that operator's,
// exactly as if they had done it in the panel with their own hands.
//
// An operator who does not exist, or whom the owner has switched off, has
// no context: their bot should not be selling, and a sale already paid for
// is left standing rather than delivered in a dead operator's name.
func (s *Shop) OperatorCtx(ctx context.Context, operatorID uint) (context.Context, error) {
	var admin model.Admin
	if err := s.db.WithContext(ctx).First(&admin, operatorID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: operator %d", fault.ErrNotFound, operatorID)
		}
		return nil, fmt.Errorf("shop: load operator %d: %w", operatorID, err)
	}
	if !admin.Enabled {
		return nil, fmt.Errorf("%w: operator %d is switched off", fault.ErrInvalid, operatorID)
	}
	ctx = operators.WithCaller(ctx, &admin)
	sc := service.ScopeFor(&admin)
	if sc.Restricted {
		// ScopeFor alone says whose name a sale is stamped with. What the
		// operator may actually sell -- which tunnels, how many customers,
		// whether they are barred right now -- is the half the API layer
		// adds to every request it serves (scopeFor in internal/api), and
		// a bot selling without it would sell straight past the owner's
		// fences: a reseller's bot could fill another tunnel, or a
		// thousandth customer, in his name. Mirror it exactly, so the bot
		// sells with precisely the rights its operator has in the panel.
		allowed, err := s.admins.AllowedInterfaces(ctx, &admin)
		if err != nil {
			return nil, fmt.Errorf("shop: operator %d tunnels: %w", operatorID, err)
		}
		sc.Interfaces = allowed
		sc.ClientLimit = admin.ClientLimit
		sc.Barred = model.PauseMessage(admin.PauseReason(time.Now().UTC()))
		if admin.Role == model.RoleAdmin {
			resellers, err := s.admins.ResellerIDs(ctx, admin.ID)
			if err != nil {
				return nil, fmt.Errorf("shop: operator %d resellers: %w", operatorID, err)
			}
			sc.Owners = append(sc.Owners, resellers...)
		}
	}
	ctx = service.WithScope(ctx, sc)
	return ctx, nil
}

// SubLink is the subscription link to hand a buyer: the panel's public
// origin and the customer's token. Empty when either half is missing --
// no public origin set yet, or a customer without a token -- so a caller
// can tell "delivered, here is the link" from "delivered, but the link
// cannot be written down yet" without guessing.
func (s *Shop) SubLink(ctx context.Context, client *model.Client) string {
	if client == nil || client.SubToken == "" {
		return ""
	}
	base, err := s.PublicBase(ctx)
	if err != nil || base == "" {
		return ""
	}
	return base + "/subscribe/" + client.SubToken
}

// Fulfill delivers a paid order: it makes the customer the buyer paid for,
// or renews the one they already hold from this seller, and marks the
// order delivered with that customer on it.
//
// A buyer is one customer per seller. The buyer's existing customer under
// this operator -- found by their Telegram id, through the operator's own
// scope, so another seller's customer with the same id is never touched --
// is renewed whatever the order calls itself: two customers for one
// Telegram account under one seller would split the person's allowance
// and their link across two rows, and the second purchase would look like
// a stranger's. Only a buyer with no customer yet gets a new one.
//
// Delivery is idempotent. An order already fulfilled hands back its
// customer and changes nothing, because the approve that triggers this is
// a button a person can tap twice, and a double tap must not become a
// double sale. A delivery that fails leaves the order paid, where the
// operator can see it and it can be tried again; nothing is marked failed
// by a machine that cannot know whether the money was real.
//
// Two kinds of order deliver something other than the one customer. A
// top-up delivers money into the buyer's wallet and no customer at all.
// A bulk order (a quantity above one) delivers its first unit the way
// any order does -- a renewal, or a first customer -- and each extra
// unit as an additional new customer of the same terms: the buyer
// bought that many services, and a unit is a service, not a bigger
// number on the first one. What Fulfill hands back is the first
// customer either way; the extras stand beside it under the buyer's
// Telegram id, where the bot lists a buyer's customers from.
func (s *Shop) Fulfill(ctx context.Context, orderID uint) (*model.Client, error) {
	order, err := s.OrderByID(ctx, orderID)
	if err != nil {
		return nil, err
	}

	opCtx, err := s.OperatorCtx(ctx, order.OperatorID)
	if err != nil {
		return nil, err
	}

	if order.Status == StatusFulfilled {
		if order.Kind == KindTopup {
			// A top-up delivered is money in a wallet; there is no
			// customer to hand back, and nothing to do again.
			return nil, nil
		}
		client, err := s.clients.Get(opCtx, order.ClientID)
		if err != nil {
			return nil, fmt.Errorf("shop: reload the delivered customer: %w", err)
		}
		return client, nil
	}
	if order.Status != StatusPaid && order.Status != StatusPendingReview {
		return nil, fmt.Errorf("%w: order %d is %s; only a paid order can be delivered",
			fault.ErrInvalid, order.ID, order.Status)
	}

	if order.Kind == KindTopup {
		return s.fulfillTopup(ctx, order)
	}

	cfg, err := s.ConfigFor(ctx, order.OperatorID)
	if err != nil {
		return nil, err
	}

	// What is delivered is the order's section of the seller's tunnels,
	// not the whole list: a V2ray sale must not hand the buyer the
	// WireGuard tunnels too (they did not pay for them, and the seller
	// may sell those separately), nor a WireGuard sale the inbounds. A
	// seller with no tunnel of the section at all cannot deliver what
	// was sold, so the order is left paid and the reason says so.
	sectionIDs, err := s.SectionInterfaces(ctx, cfg, order.Section)
	if err != nil {
		return nil, err
	}
	if len(sectionIDs) == 0 {
		return nil, fmt.Errorf("%w: the seller's shop has no %s tunnel to deliver this order on",
			fault.ErrInvalid, NormalizeSection(order.Section))
	}

	existing, err := s.clients.ByTelegramID(opCtx, order.BuyerTGID)
	if err != nil {
		return nil, err
	}

	// How many units this order delivers, and where the next customer
	// made stands in the operator's book (the name format's {n}): both
	// are read once, at the start, so every unit of the one order is
	// numbered from the same moment.
	qty := order.Quantity
	if qty < 1 {
		qty = 1
	}
	counter := s.clientCounter(ctx, opCtx)

	var client *model.Client
	if len(existing) > 0 {
		client = &existing[0]
		// The section's tunnels first, the quota and days after:
		// attaching is idempotent and renewing is not, so a delivery
		// that fails between the two and is tried again must find the
		// attach already done and the renewal still owed -- the other
		// way round, a retry would sell the same quota twice.
		if err := s.attachSection(opCtx, client, order, sectionIDs); err != nil {
			return nil, err
		}
		if err := s.renew(opCtx, client, order); err != nil {
			return nil, err
		}
		// Read back what the renewal wrote, so what is returned -- and
		// linked for the buyer -- is the customer as they now stand.
		client, err = s.clients.Get(opCtx, client.ID)
		if err != nil {
			return nil, fmt.Errorf("shop: reload the renewed customer: %w", err)
		}
	} else {
		client, err = s.createForOrder(opCtx, cfg, order, sectionIDs, &counter, 1)
		if err != nil {
			return nil, err
		}
	}

	// The extra units of a bulk order, each its own new customer of
	// the order's terms. They are made before the order is marked
	// delivered, like the first unit: a failure here leaves the order
	// paid and the delivery retriable, rather than delivered and
	// silently short.
	for unit := 2; unit <= qty; unit++ {
		extra, err := s.createForOrder(opCtx, cfg, order, sectionIDs, &counter, unit)
		if err != nil {
			return nil, err
		}
		s.log.Info("shop order extra unit delivered",
			"order", order.ID, "operator", order.OperatorID,
			"client", extra.ID, "unit", unit)
	}

	order.Status = StatusFulfilled
	order.ClientID = client.ID
	if err := s.UpdateOrder(ctx, order); err != nil {
		return nil, err
	}
	s.settle(ctx, cfg, order)
	s.log.Info("shop order delivered",
		"order", order.ID, "operator", order.OperatorID,
		"client", client.ID, "kind", order.Kind)
	return client, nil
}

// createForOrder makes one customer of an order's terms: the unit-th
// unit of the order, named by the shop's name rule. With a format set,
// the name is the format worked on the buyer, and the counter moves
// with every customer made, so an order's units stand apart in {n}
// and {rand} both. Without one, the name is the buyer's, and the
// units after the first are numbered after it -- "Buyer (2)" -- so a
// bulk buyer can tell their own services apart in a list.
func (s *Shop) createForOrder(ctx context.Context, cfg *Config, order *Order, sectionIDs []uint, counter *int, unit int) (*model.Client, error) {
	name := formatClientName(cfg, order.BuyerName, order.BuyerTGID, *counter)
	(*counter)++
	if strings.TrimSpace(cfg.NameFormat) == "" && unit > 1 {
		name = capClientName(fmt.Sprintf("%s (%d)", name, unit))
	}
	in := service.CreateInput{
		Name:         name,
		InterfaceIDs: sectionIDs,
		QuotaBytes:   order.QuotaBytes,
		DeviceLimit:  order.DeviceLimit,
		TelegramID:   order.BuyerTGID,
	}
	// The shop starts the clock at purchase: the date is written
	// outright rather than left to first connection, so a plan bought
	// today ends on its day whether or not it is used tonight. A plan
	// of no days has no date at all, as in the panel.
	if order.Days > 0 {
		expires := time.Now().UTC().Add(time.Duration(order.Days) * 24 * time.Hour)
		in.ExpiresAt = &expires
	}
	return s.clients.Create(ctx, in)
}

// fulfillTopup delivers a top-up: the order's price lands in the
// buyer's wallet, and there is no customer to hand back.
//
// The order is marked delivered first and the money second, in the
// one call that performs the flip -- a second call finds the order
// fulfilled and credits nothing. The order of the two acts is the
// same choice as the rest of Fulfill's: a failure between them leaves
// a record an operator can see and put right (a delivered top-up
// whose credit is in the log as failed), never money conjured twice
// by a retry.
func (s *Shop) fulfillTopup(ctx context.Context, order *Order) (*model.Client, error) {
	if order.PriceToman <= 0 {
		return nil, fmt.Errorf("%w: a top-up of %d Toman delivers nothing",
			fault.ErrInvalid, order.PriceToman)
	}
	order.Status = StatusFulfilled
	order.ClientID = 0
	if err := s.UpdateOrder(ctx, order); err != nil {
		return nil, err
	}
	if err := s.CreditWallet(ctx, order.OperatorID, order.BuyerTGID, order.PriceToman); err != nil {
		s.log.Error("shop top-up delivered but its credit failed",
			"order", order.ID, "operator", order.OperatorID,
			"buyer", order.BuyerTGID, "amount", order.PriceToman, "error", err)
		return nil, err
	}
	s.log.Info("shop top-up delivered",
		"order", order.ID, "operator", order.OperatorID,
		"buyer", order.BuyerTGID, "amount", order.PriceToman)
	return nil, nil
}

// settle is a delivered order's bookkeeping, run by the one Fulfill
// call that turned the order fulfilled -- and only that call, which
// is what makes each of its acts happen once: the code the buyer
// named is written down as used, and the referrer's cashback lands.
// Both trail a delivery that has already happened, so a failure in
// either is logged rather than allowed to un-deliver a customer.
func (s *Shop) settle(ctx context.Context, cfg *Config, order *Order) {
	if order.Code != "" {
		code, err := s.CodeFor(ctx, order.OperatorID, order.Code)
		switch {
		case err != nil:
			// A code deleted since checkout has nothing left to
			// record against; the sale it discounted stands either
			// way, and the trail of it is the order's own copy.
			s.log.Warn("shop: the order's code is gone at delivery",
				"order", order.ID, "code", order.Code, "error", err)
		default:
			if err := s.RecordCodeUse(ctx, code.ID, order.BuyerTGID, order.ID); err != nil {
				s.log.Warn("shop: record the order's code use",
					"order", order.ID, "error", err)
			}
		}
	}
	s.payCashback(ctx, cfg, order)
}

// attachSection puts an existing customer on the tunnels of the section
// they have just bought and do not already hold: a WireGuard customer
// buying a V2ray plan gains the inbounds, and the other way round, so
// the one customer ends up holding both halves of the shop -- under the
// one link, which is the point of the customer being one row. A failure
// to attach fails the delivery, leaving the order paid: the buyer paid
// for that section, and marking the sale delivered without it would be
// the one outcome nobody can undo from the panel.
func (s *Shop) attachSection(ctx context.Context, client *model.Client, order *Order, sectionIDs []uint) error {
	res, err := s.clients.AttachServers(ctx, []uint{client.ID}, sectionIDs)
	if err != nil {
		return err
	}
	if res != nil {
		for _, why := range res.Failures {
			return fmt.Errorf("%w: the %s tunnels could not be added to the customer: %s",
				fault.ErrInvalid, NormalizeSection(order.Section), why)
		}
	}
	return nil
}

// renew gives an existing customer what a renewal order sold them, by the
// panel's own rules for adding to a plan:
//
//   - The allowance grows by the plan's quota -- it is added, not
//     replaced, so traffic a customer had not spent is still theirs. An
//     unlimited plan makes the customer unlimited; a limited one adds
//     nothing to a customer who already has no limit.
//   - The end date moves by the plan's days from the later of now and
//     their current end, so time already paid for is never taken back and
//     a customer who lapsed restarts from today. A customer with no end
//     date keeps having none.
//   - A plan still waiting for its first connection takes the days onto
//     its length instead, and still starts when they connect.
//   - Running out is undone by what restores it: more time revives the
//     expired, more allowance the exhausted, through the same Adjust the
//     panel's own renewals go through. A customer the operator switched
//     off stays off -- that word is the operator's, not the shop's.
//
// Clients.Adjust is the panel's entry point for exactly this, but it sets
// the allowance rather than adding to it, so the sum is worked out here
// and handed over whole; the date it moves exactly as asked for, from the
// later of now and the customer's own end.
func (s *Shop) renew(ctx context.Context, client *model.Client, order *Order) error {
	if client.Waiting() && order.Days > 0 {
		// Onto the plan's length, to the day, and it still starts on
		// first connection: Extend with the waiting included is the
		// panel's one path that does this.
		_, err := s.clients.Extend(ctx, service.ExtendInput{
			IDs: []uint{client.ID}, Kind: service.ExtendTime,
			Days: float64(order.Days), IncludeWaiting: true,
		})
		if err != nil {
			return err
		}
	}

	in := service.AdjustInput{IDs: []uint{client.ID}}
	if order.Days > 0 && !client.Waiting() && client.ExpiresAt != nil {
		days := float64(order.Days)
		in.AddDays = &days
	}
	switch {
	case order.QuotaBytes == 0:
		// An unlimited plan: the customer becomes unlimited, unless they
		// already are, in which case there is nothing to write.
		if client.QuotaBytes != 0 {
			unlimited := uint64(0)
			in.QuotaBytes = &unlimited
		}
	case client.QuotaBytes != 0:
		grown := client.QuotaBytes + order.QuotaBytes
		in.QuotaBytes = &grown
	}
	if in.AddDays == nil && in.QuotaBytes == nil {
		return nil
	}
	if _, err := s.clients.Adjust(ctx, in); err != nil {
		return err
	}
	return nil
}

// Reject refuses an order whose payment the operator will not accept --
// the receipt that is not a transfer, the gateway payment that never
// settled. Only an order standing at the operator's own decision point
// can be rejected: one awaiting their review, or one already marked paid
// and not yet delivered. A delivered order is a customer now, and refusing
// it is not this function's to do.
func (s *Shop) Reject(ctx context.Context, orderID uint) error {
	order, err := s.OrderByID(ctx, orderID)
	if err != nil {
		return err
	}
	if order.Status != StatusPendingReview && order.Status != StatusPaid {
		return fmt.Errorf("%w: order %d is %s; only an order awaiting review or paid can be rejected",
			fault.ErrInvalid, order.ID, order.Status)
	}
	order.Status = StatusRejected
	return s.UpdateOrder(ctx, order)
}
