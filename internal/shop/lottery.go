package shop

import (
	"context"
	"fmt"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/fault"
)

// The lottery's two questions, kept in the shop because both are
// about the shop's own records: who is in the pool, and what the
// prize is worth. The draw itself -- picking the name and saying
// the words -- is the bot's.

// LotteryPool is who may win an operator's lottery: the distinct
// buyers holding at least one delivered sale -- a new plan or a
// renewal, the orders that are business -- placed at or after
// since. A top-up is money moving, not a sale, and never enters
// anybody; an unpaid or refused order is not a sale either.
func (s *Shop) LotteryPool(ctx context.Context, operatorID uint, since time.Time) ([]int64, error) {
	var ids []int64
	if err := s.db.WithContext(ctx).Model(&model.ShopOrder{}).
		Where("operator_id = ? AND status = ? AND kind IN ? AND created_at >= ?",
			operatorID, StatusFulfilled, []string{KindNew, KindRenew}, since).
		Distinct().Pluck("buyer_tg_id", &ids).Error; err != nil {
		return nil, fmt.Errorf("shop: read the lottery pool: %w", err)
	}
	return ids, nil
}

// CheapestPlanPrice is the lottery's prize, in Toman: the price of
// the cheapest plan the shop currently sells, which the winner
// receives as wallet credit. A shop selling nothing has no prize
// to draw for, and says so.
func (s *Shop) CheapestPlanPrice(ctx context.Context, operatorID uint) (int64, error) {
	plans, err := s.PlansFor(ctx, operatorID, true)
	if err != nil {
		return 0, err
	}
	if len(plans) == 0 {
		return 0, fmt.Errorf("%w: the shop has no plan to price a prize from", fault.ErrNotFound)
	}
	cheapest := plans[0].PriceToman
	for i := range plans {
		if plans[i].PriceToman < cheapest {
			cheapest = plans[i].PriceToman
		}
	}
	return cheapest, nil
}
