package service

import (
	"context"
	"fmt"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// The dashboard an operator who is not the owner lands on: their own
// corner of the panel, counted over their own customers alone.
//
// Every figure here is narrowed by hand to the caller's owner set, the
// same set the database's own narrowing matches against. The automatic
// narrowing cannot be relied on for this: it stands down for an operator
// who may read every customer (an admin with that grant is not
// "restricted"), and their dashboard is still their own tree, not the
// panel's. So each query below names the set itself, and a caller whose
// set is the owner's own id alone -- the owner asking -- is answered
// about the owner's own customers, never about a reseller's.

// MineOverview is the reseller dashboard's figures.
type MineOverview struct {
	Clients      int64 `json:"clients"`
	Active       int64 `json:"active"`
	Expired      int64 `json:"expired"`
	ExpiringSoon int64 `json:"expiringSoon"`

	// The ceiling, in the units it is enforced in: users sold (the sum
	// of what each customer's plan is for) against the limit the scope
	// carries. A limit of zero is no limit, as everywhere else.
	CeilingUsed  int64 `json:"ceilingUsed"`
	CeilingLimit int   `json:"ceilingLimit"`

	// MonthUsedBytes is what the caller's customers have carried since
	// the first of this month, from the per-customer samples the
	// reconciler keeps. The samples are deltas in five-minute buckets
	// and nothing in the panel rolls them into coarser ones, so the sum
	// over the month is the month's traffic, neither doubled nor missing
	// a tier.
	MonthUsedBytes uint64 `json:"monthUsedBytes"`

	// Expiring is the soonest-ending of the expiring set, first things
	// first: the customers a renewal offered today would still reach.
	Expiring []MineExpiring `json:"expiring"`
}

// MineExpiring is one customer about to run out, as the dashboard's list
// shows them: enough to recognise and to open.
type MineExpiring struct {
	ID        uint       `json:"id"`
	Name      string     `json:"name"`
	ExpiresAt *time.Time `json:"expiresAt"`
}

// mineExpiringList is how many of the expiring set the dashboard names.
// The count beside it says how many there are in all; the list is the
// ones to act on first.
const mineExpiringList = 5

// Mine returns the caller's own dashboard figures.
func (s *Clients) Mine(ctx context.Context) (*MineOverview, error) {
	db := s.db.WithContext(ctx)
	owners := ScopeOf(ctx).OwnerList()
	now := time.Now().UTC()
	o := &MineOverview{CeilingLimit: ScopeOf(ctx).ClientLimit}

	count := func(dst *int64, where ...any) error {
		q := db.Model(&model.Client{}).Where("owner_id IN ?", owners)
		if len(where) > 0 {
			q = q.Where(where[0], where[1:]...)
		}
		return q.Count(dst).Error
	}
	if err := count(&o.Clients); err != nil {
		return nil, err
	}
	if err := count(&o.Active, "status = ?", model.StatusActive); err != nil {
		return nil, err
	}
	if err := count(&o.Expired, "status = ?", model.StatusExpired); err != nil {
		return nil, err
	}
	// The expiring set is the customer list's own "expiring" narrowing,
	// so the tile and the list it opens never disagree about who is in it.
	if err := count(&o.ExpiringSoon,
		"status = ? AND expires_at IS NOT NULL AND expires_at <= ?",
		model.StatusActive, now.Add(expiringWithin)); err != nil {
		return nil, err
	}

	// The ceiling is counted the way checkCeiling counts it -- the users
	// each plan is for, over the operator's own tree -- so the dashboard
	// and the refusal a form gets are the same number.
	var held *int64
	if err := db.Model(&model.Client{}).
		Select("SUM(device_limit)").
		Where("owner_id IN ?", owners).
		Scan(&held).Error; err != nil {
		return nil, fmt.Errorf("service: count your users: %w", err)
	}
	if held != nil {
		o.CeilingUsed = *held
	}

	// This month's traffic. traffic_samples is not a table the narrowing
	// covers -- it is keyed by customer, with no owner on it -- so the
	// customers are named through the same subquery the overview's
	// device count uses.
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	var month *uint64
	if err := db.Model(&model.TrafficSample{}).
		Select("SUM(rx + tx)").
		Where("bucket_ts >= ?", monthStart).
		Where("client_id IN (SELECT id FROM clients WHERE owner_id IN ?)", owners).
		Scan(&month).Error; err != nil {
		return nil, fmt.Errorf("service: sum this month's traffic: %w", err)
	}
	if month != nil {
		o.MonthUsedBytes = *month
	}

	var soon []model.Client
	if err := db.Model(&model.Client{}).
		Where("owner_id IN ?", owners).
		Where("status = ? AND expires_at IS NOT NULL AND expires_at <= ?",
			model.StatusActive, now.Add(expiringWithin)).
		Order("expires_at ASC").
		Limit(mineExpiringList).
		Find(&soon).Error; err != nil {
		return nil, err
	}
	o.Expiring = make([]MineExpiring, 0, len(soon))
	for i := range soon {
		o.Expiring = append(o.Expiring, MineExpiring{
			ID: soon[i].ID, Name: soon[i].Name, ExpiresAt: soon[i].ExpiresAt,
		})
	}
	return o, nil
}
