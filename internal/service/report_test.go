package service

import (
	"context"
	"testing"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// The monthly report counts the caller's own month, and only theirs:
// two resellers with disjoint books, customers and samples standing on
// both sides of the month's edges, and renewal orders of every kind and
// state. A figure that leaked the other reseller's month -- or counted
// a sale as a renewal, or last month's traffic as this month's -- would
// be a lie told in the caller's name.
func TestMonthReportCountsOnlyTheCallersMonth(t *testing.T) {
	db, clients, admins := resellerDB(t)
	reza := newReseller(t, admins, "reza", 0)
	sara := newReseller(t, admins, "sara", 0)

	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)

	mk := func(ctx context.Context, name string, quota uint64, created time.Time) *model.Client {
		t.Helper()
		c, err := clients.Create(ctx, CreateInput{
			Name: name, InterfaceIDs: []uint{1}, DeviceLimit: 1, QuotaBytes: quota,
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := db.Model(&model.Client{}).Where("id = ?", c.ID).
			Update("created_at", created).Error; err != nil {
			t.Fatal(err)
		}
		return c
	}

	// Reza's: one made just inside the month, one just before it (last
	// month's sale, this month's traffic), one just inside its end.
	early := mk(asReseller(reza, 1), "reza-early", 100, start.Add(time.Hour))
	before := mk(asReseller(reza, 1), "reza-before", 999, start.Add(-time.Hour))
	late := mk(asReseller(reza, 1), "reza-late", 300, end.Add(-time.Minute))
	// Sara's and the owner's, made inside the month: never reza's.
	saras := mk(asReseller(sara, 1), "sara-in", 555, start.Add(2*time.Hour))
	mk(context.Background(), "owner-in", 777, start.Add(3*time.Hour))

	sample := func(clientID uint, at time.Time, rx, tx uint64) {
		t.Helper()
		if err := db.Create(&model.TrafficSample{
			ClientID: clientID, BucketTS: at, Granularity: model.GranularityFine,
			RX: rx, TX: tx,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	sample(early.ID, start.Add(2*time.Hour), 1000, 500) // day 1: 1500
	sample(early.ID, start.Add(26*time.Hour), 2000, 0)  // day 2: 2000
	sample(before.ID, start.Add(3*time.Hour), 7000, 0)  // day 1: the heaviest
	sample(before.ID, start.Add(-time.Hour), 999999, 0) // last month: not this one's
	sample(saras.ID, start.Add(4*time.Hour), 50000, 0)  // sara's, never reza's

	order := func(operatorID, clientID uint, kind, status string, at time.Time, token string) {
		t.Helper()
		o := &model.ShopOrder{
			OperatorID: operatorID, BuyerTGID: 42, PlanName: "plan",
			Kind: kind, Status: status, ClientID: clientID, PublicToken: token,
		}
		if err := db.Create(o).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.ShopOrder{}).Where("id = ?", o.ID).
			Update("updated_at", at).Error; err != nil {
			t.Fatal(err)
		}
	}
	// Reza's renewals: two distinct customers inside the month (one of
	// them renewed twice), one just before it, and the near-misses --
	// a new sale and an undelivered renewal are not renewals.
	order(reza.ID, early.ID, "renew", "fulfilled", start.Add(5*time.Hour), "tok-r1")
	order(reza.ID, early.ID, "renew", "fulfilled", start.Add(6*time.Hour), "tok-r2")
	order(reza.ID, before.ID, "renew", "fulfilled", start.Add(7*time.Hour), "tok-r3")
	order(reza.ID, before.ID, "renew", "fulfilled", start.Add(-time.Hour), "tok-r4")
	order(reza.ID, late.ID, "new", "fulfilled", start.Add(8*time.Hour), "tok-r5")
	order(reza.ID, late.ID, "renew", "paid", start.Add(9*time.Hour), "tok-r6")
	order(sara.ID, saras.ID, "renew", "fulfilled", start.Add(10*time.Hour), "tok-s1")

	rep, err := clients.MonthReport(asReseller(reza, 1), start)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Month != start.Format("2006-01") {
		t.Errorf("the report names %q, want %q", rep.Month, start.Format("2006-01"))
	}
	if rep.Created != 2 {
		t.Errorf("reza made %d customers in the month, want their own 2 (early, late)", rep.Created)
	}
	if rep.CreatedQuotaBytes != 400 {
		t.Errorf("reza's made customers promised %d bytes, want 100+300 = 400", rep.CreatedQuotaBytes)
	}
	if rep.Renewals != 2 {
		t.Errorf("reza renewed %d distinct customers, want 2 (early, before)", rep.Renewals)
	}
	if rep.UsedBytes != 10500 {
		t.Errorf("reza's customers carried %d bytes in the month, want 1500+2000+7000 = 10500", rep.UsedBytes)
	}
	daysInMonth := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if len(rep.Days) != daysInMonth {
		t.Fatalf("the report has %d days, want the month's %d", len(rep.Days), daysInMonth)
	}
	if rep.Days[0].Bytes != 8500 || rep.Days[1].Bytes != 2000 || rep.Days[2].Bytes != 0 {
		t.Errorf("the days read %d, %d, %d, want 8500, 2000, 0",
			rep.Days[0].Bytes, rep.Days[1].Bytes, rep.Days[2].Bytes)
	}
	if len(rep.Top) != 2 || rep.Top[0].Name != "reza-before" || rep.Top[0].Bytes != 7000 ||
		rep.Top[1].Name != "reza-early" || rep.Top[1].Bytes != 3500 {
		t.Errorf("the heaviest read %+v, want reza-before 7000 then reza-early 3500", rep.Top)
	}

	// Sara's month is her own: one made, promising 555, one renewal,
	// 50000 carried -- and nothing of reza's anywhere in it.
	other, err := clients.MonthReport(asReseller(sara, 1), start)
	if err != nil {
		t.Fatal(err)
	}
	if other.Created != 1 || other.CreatedQuotaBytes != 555 || other.Renewals != 1 ||
		other.UsedBytes != 50000 {
		t.Errorf("sara's month reads %+v, want 1 made promising 555, 1 renewal, 50000 carried", other)
	}
	if len(other.Top) != 1 || other.Top[0].Name != "sara-in" {
		t.Errorf("sara's heaviest read %+v, want only sara-in", other.Top)
	}

	// Last month, asked for plainly: the customer made just before this
	// month began is that month's, with its traffic and its renewal --
	// the edges cut both ways.
	prev, err := clients.MonthReport(asReseller(reza, 1), start.AddDate(0, -1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if prev.Created != 1 || prev.CreatedQuotaBytes != 999 || prev.Renewals != 1 ||
		prev.UsedBytes != 999999 {
		t.Errorf("reza's previous month reads %+v, want 1 made promising 999, 1 renewal, 999999 carried", prev)
	}
	if last := prev.Days[len(prev.Days)-1]; last.Bytes != 999999 {
		t.Errorf("the previous month's last day reads %d bytes, want the 999999 that fell in it", last.Bytes)
	}
}
