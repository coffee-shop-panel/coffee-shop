package service

import (
	"context"
	"fmt"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// One operator's month, counted the way the dashboard counts (mine.go):
// every figure narrowed by hand to the caller's owner set, because the
// automatic narrowing stands down for an operator who may read every
// customer, and their report is still their own tree, not the panel's.

// MonthReport is one month of one operator's business.
type MonthReport struct {
	// Month is the month the figures describe, "2006-01".
	Month string `json:"month"`

	// Created is how many of the caller's customers were made in it, and
	// CreatedQuotaBytes the allowance those sales committed in all --
	// the volume the operator promised, whether or not it gets used.
	Created           int64  `json:"created"`
	CreatedQuotaBytes uint64 `json:"createdQuotaBytes"`

	// Renewals is how many distinct customers were renewed in the month
	// through the sales bot: the fulfilled orders of the "renew" kind
	// (shop.KindRenew, shop.StatusFulfilled) name the customer each was
	// delivered as, so this count is exact. Renewals by hand leave no
	// such record -- the activity log's line for an extend names the
	// request and its maker but never the customers it touched, and the
	// management bot extends below the API entirely -- so they are not
	// counted here rather than guessed at. The page labels the figure
	// with its source.
	Renewals int64 `json:"renewals"`

	// UsedBytes is what the caller's customers carried in the month,
	// from the same samples the dashboard's month figure sums, so for
	// the current month the two pages agree to the byte. Days is that
	// traffic by day, every day of the month present, quiet days at
	// zero, so the chart is the whole month and not only its busy part.
	UsedBytes uint64      `json:"usedBytes"`
	Days      []ReportDay `json:"days"`

	// Top is the month's heaviest of the caller's customers, heaviest
	// first: the ones the month's traffic is mostly owed to.
	Top []ReportTopClient `json:"top"`
}

// ReportDay is one day of the month and what the caller's customers
// carried in it.
type ReportDay struct {
	Date  string `json:"date"` // "2006-01-02"
	Bytes uint64 `json:"bytes"`
}

// ReportTopClient is one heavy customer in the month's list.
type ReportTopClient struct {
	ID    uint   `json:"id"`
	Name  string `json:"name"`
	Bytes uint64 `json:"bytes"`
}

// reportTopList is how many heavy customers the report names. The rest
// are in the month's total; these are the ones worth a look.
const reportTopList = 5

// MonthReport returns the caller's figures for the month that starts at
// start (the first of a month, UTC; the handler parses and aligns it).
func (s *Clients) MonthReport(ctx context.Context, start time.Time) (*MonthReport, error) {
	db := s.db.WithContext(ctx)
	owners := ScopeOf(ctx).OwnerList()
	end := start.AddDate(0, 1, 0)
	rep := &MonthReport{Month: start.Format("2006-01")}

	// Made this month, and what those sales promised.
	var made struct {
		N     int64
		Quota *uint64
	}
	if err := db.Model(&model.Client{}).
		Select("COUNT(*) AS n, SUM(quota_bytes) AS quota").
		Where("owner_id IN ?", owners).
		Where("created_at >= ? AND created_at < ?", start, end).
		Scan(&made).Error; err != nil {
		return nil, fmt.Errorf("service: count the month's new customers: %w", err)
	}
	rep.Created = made.N
	if made.Quota != nil {
		rep.CreatedQuotaBytes = *made.Quota
	}

	// Renewed this month through the sales bot. The order's UpdatedAt is
	// when it was delivered: fulfillment is the last thing that happens
	// to a renew order, and it stamps the row as it lands.
	var renewed *int64
	if err := db.Model(&model.ShopOrder{}).
		Select("COUNT(DISTINCT client_id)").
		Where("operator_id IN ?", owners).
		Where("kind = ? AND status = ? AND client_id > 0", "renew", "fulfilled").
		Where("updated_at >= ? AND updated_at < ?", start, end).
		Scan(&renewed).Error; err != nil {
		return nil, fmt.Errorf("service: count the month's renewals: %w", err)
	}
	if renewed != nil {
		rep.Renewals = *renewed
	}

	// The month's traffic. traffic_samples is keyed by customer with no
	// owner on it (see mine.go), so the customers are named through the
	// same subquery. Buckets are five-minute marks the reconciler
	// truncates to, shared by every customer, so grouping by the bucket
	// collapses the month to at most a row per bucket; the days are
	// folded here, which keeps the query off any one database's date
	// functions.
	own := "client_id IN (SELECT id FROM clients WHERE owner_id IN ?)"
	var buckets []struct {
		BucketTS time.Time
		Bytes    uint64
	}
	if err := db.Model(&model.TrafficSample{}).
		Select("bucket_ts, SUM(rx + tx) AS bytes").
		Where("bucket_ts >= ? AND bucket_ts < ?", start, end).
		Where(own, owners).
		Group("bucket_ts").
		Scan(&buckets).Error; err != nil {
		return nil, fmt.Errorf("service: sum the month's traffic by bucket: %w", err)
	}
	rep.Days = make([]ReportDay, 0, end.Sub(start)/(24*time.Hour))
	byDay := map[string]int{}
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		byDay[d.Format("2006-01-02")] = len(rep.Days)
		rep.Days = append(rep.Days, ReportDay{Date: d.Format("2006-01-02")})
	}
	for _, b := range buckets {
		i, ok := byDay[b.BucketTS.UTC().Format("2006-01-02")]
		if !ok {
			continue // a bucket on the month's edge in another zone's reading
		}
		rep.Days[i].Bytes += b.Bytes
		rep.UsedBytes += b.Bytes
	}

	// The heaviest of the month, by customer.
	var heavy []struct {
		ClientID uint
		Bytes    uint64
	}
	if err := db.Model(&model.TrafficSample{}).
		Select("client_id, SUM(rx + tx) AS bytes").
		Where("bucket_ts >= ? AND bucket_ts < ?", start, end).
		Where(own, owners).
		Group("client_id").
		Order("bytes DESC").
		Limit(reportTopList).
		Scan(&heavy).Error; err != nil {
		return nil, fmt.Errorf("service: rank the month's customers: %w", err)
	}
	rep.Top = make([]ReportTopClient, 0, len(heavy))
	if len(heavy) > 0 {
		ids := make([]uint, 0, len(heavy))
		for _, h := range heavy {
			ids = append(ids, h.ClientID)
		}
		var named []model.Client
		if err := db.Model(&model.Client{}).
			Select("id, name").
			Where("id IN ?", ids).
			Find(&named).Error; err != nil {
			return nil, fmt.Errorf("service: name the month's heaviest: %w", err)
		}
		names := make(map[uint]string, len(named))
		for _, c := range named {
			names[c.ID] = c.Name
		}
		for _, h := range heavy {
			rep.Top = append(rep.Top, ReportTopClient{
				ID: h.ClientID, Name: names[h.ClientID], Bytes: h.Bytes,
			})
		}
	}
	return rep, nil
}
