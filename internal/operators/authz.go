package operators

import (
	"context"
	"errors"
	"fmt"
	"math"

	"gorm.io/gorm"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/fault"
)

// Who a request to manage operators is being served for, and what that lets
// them touch.
//
// The owner manages every operator; an admin manages only the resellers they
// created, and only as resellers -- never another admin, never the owner,
// never themselves, and never with a ceiling larger than their own. The
// customers table has the database narrow itself (internal/scope); the
// operators table is small and managed from one file, so the same separation
// is enforced here by hand, at the top of each method, before the work.

type callerKey struct{}

// WithCaller attaches the operator a management request is being served for.
// The API sets it from the signed-in operator; left unset -- an internal
// tool, the CLI, a test -- the request is unrestricted, as the owner's is.
func WithCaller(ctx context.Context, admin *model.Admin) context.Context {
	return context.WithValue(ctx, callerKey{}, admin)
}

// CallerOf reads it back, or nil when none was set.
func CallerOf(ctx context.Context) *model.Admin {
	a, _ := ctx.Value(callerKey{}).(*model.Admin)
	return a
}

// scopedTo is the admin a request is held to, or nil when it is unrestricted.
// An admin is held to their own resellers; the owner and an unset caller are
// not held to anyone.
func scopedTo(ctx context.Context) *model.Admin {
	c := CallerOf(ctx)
	if c != nil && c.Role == model.RoleAdmin {
		return c
	}
	return nil
}

// ownedReseller loads an operator and, for a scoped admin, refuses it unless
// it is one of their own resellers -- as not-found, so guessing an id tells
// the admin nothing about who else is on the panel. The owner reaches any.
func (s *Admins) ownedReseller(ctx context.Context, id uint) (*model.Admin, error) {
	var a model.Admin
	if err := s.db.WithContext(ctx).First(&a, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fault.ErrNotFound
		}
		return nil, err
	}
	if caller := scopedTo(ctx); caller != nil {
		if a.Role != model.RoleReseller || a.ParentAdminID != caller.ID {
			return nil, fault.ErrNotFound
		}
	}
	return &a, nil
}

// checkHandDown refuses a reseller an admin is creating or changing that
// would reach past the admin's own ceiling: a tunnel the admin cannot sell,
// more traffic than the admin has left, or more users than the admin's
// tree has room for. The owner hands down without limit. interfaceIDs is
// nil on an update that leaves the set alone, and clientLimit is nil on
// one that leaves the limit alone; on a create it is the limit the
// reseller ends up with, where nil in the input has already been read as
// the zero that means no limit. resellerID is the reseller being changed,
// or zero for a new one.
func (s *Admins) checkHandDown(ctx context.Context, interfaceIDs []uint, quota *uint64, clientLimit *int, resellerID uint) error {
	caller := scopedTo(ctx)
	if caller == nil {
		return nil
	}
	allowed, err := s.AllowedInterfaces(ctx, caller)
	if err != nil {
		return err
	}
	if allowed != nil { // nil means the admin may sell every tunnel
		for _, id := range interfaceIDs {
			if !allowed[id] {
				return fault.Field("interfaceIds", "that server is not one of yours to hand down")
			}
		}
	}
	// A reseller cannot be given more than the admin has left. An admin with
	// no limit of their own hands down without one. Zero is no limit, not
	// nothing: without the check below a capped admin could hand a reseller
	// unlimited traffic, since zero is never more than what is left.
	//
	// What is left is the allowance less what is already spent and what is
	// already promised: a reseller's quota is a promise of traffic, and
	// without subtracting the unspent part of every sibling's promise the
	// same traffic could be handed down twice over -- each new reseller
	// measured against an allowance that never shrinks.
	if quota != nil && caller.QuotaBytes > 0 {
		if *quota <= 0 {
			return fault.Field("quotaBytes", "a reseller under you needs a traffic limit: yours is limited")
		}
		held, spent, err := s.treeQuota(ctx, caller, resellerID)
		if err != nil {
			return err
		}
		// The quota is measured as the headroom it leaves the reseller:
		// what they have already spent is spent, and the unspent part of
		// the quota they hold now is theirs already, not a new charge on
		// the admin's pool.
		var fresh uint64
		if *quota > spent {
			fresh = *quota - spent
		}
		left := caller.RemainingBytes()
		if fresh > left || held > left-fresh {
			return fault.Field("quotaBytes", "that is more traffic than you have left to hand down")
		}
	}
	// The same for users. The limit counts users, not customers -- the
	// device limits of the customers in the admin's tree added up, as the
	// ceiling on one operator is counted -- so a reseller with no limit,
	// or one past what the tree has left, would grow the tree past the
	// ceiling the owner sold the admin, one customer at a time and never
	// refused anywhere.
	if clientLimit != nil && caller.ClientLimit > 0 {
		if *clientLimit <= 0 {
			return fault.Field("clientLimit", "a reseller under you needs a user limit: yours is %d users", caller.ClientLimit)
		}
		held, err := s.treeUsers(ctx, caller, resellerID)
		if err != nil {
			return err
		}
		if int64(*clientLimit) > int64(caller.ClientLimit)-held {
			return fault.Field("clientLimit",
				"that is more users than you have left to hand down: %d of your %d are already sold",
				held, caller.ClientLimit)
		}
	}
	return nil
}

// treeQuota is the traffic already promised to the resellers under an
// admin and not yet used: each one's quota less what they have spent,
// floored at nothing. The reseller being handed a quota is left out of
// the sum, and what they have spent returned beside it: their promise is
// what the new quota replaces, so the new quota is measured against the
// pool less the siblings' promises and their own spent traffic, not
// against a pool that still charges them for a promise they no longer
// hold.
func (s *Admins) treeQuota(ctx context.Context, caller *model.Admin, except uint) (held, spent uint64, err error) {
	var rows []struct {
		ID         uint
		QuotaBytes uint64
		UsedBytes  uint64
	}
	if err := s.db.WithContext(ctx).Model(&model.Admin{}).
		Where("parent_admin_id = ? AND role = ?", caller.ID, model.RoleReseller).
		Select("id, quota_bytes, used_bytes").Find(&rows).Error; err != nil {
		return 0, 0, fmt.Errorf("service: count the traffic promised in your tree: %w", err)
	}
	for _, r := range rows {
		if r.ID == except {
			spent = r.UsedBytes
			continue
		}
		if r.QuotaBytes > r.UsedBytes {
			if d := r.QuotaBytes - r.UsedBytes; held > math.MaxUint64-d {
				held = math.MaxUint64
			} else {
				held += d
			}
		}
	}
	return held, spent, nil
}

// treeUsers is how many users the customers in an admin's tree account
// for: the device limits of every customer the admin or one of their
// resellers holds, added up -- the count the ceiling holds one operator
// to, over the whole tree. The reseller being handed a limit is left
// out: their customers are what the new limit is measured against, not
// a charge on top of it.
func (s *Admins) treeUsers(ctx context.Context, caller *model.Admin, except uint) (int64, error) {
	resellers, err := s.ResellerIDs(ctx, caller.ID)
	if err != nil {
		return 0, err
	}
	owners := append([]uint{caller.ID}, resellers...)
	q := s.db.WithContext(ctx).Model(&model.Client{}).
		Where("owner_id IN ?", owners)
	if except != 0 {
		q = q.Where("owner_id <> ?", except)
	}
	var held *int64
	if err := q.Select("SUM(device_limit)").Scan(&held).Error; err != nil {
		return 0, fmt.Errorf("service: count the users in your tree: %w", err)
	}
	if held == nil {
		return 0, nil
	}
	return *held, nil
}
