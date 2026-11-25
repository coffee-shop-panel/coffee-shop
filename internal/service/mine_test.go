package service

import (
	"context"
	"testing"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// The reseller dashboard counts the caller's own customers, and only
// those: two resellers with disjoint books, an owner's customer beside
// them, and traffic samples on both sides of the month's edge and both
// sides of the separation. If any figure here were the panel's, or the
// other reseller's, the dashboard would be a window into somebody else's
// business wearing the caller's name.
func TestMineCountsOnlyWhatTheOperatorHolds(t *testing.T) {
	db, clients, admins := resellerDB(t)
	reza := newReseller(t, admins, "reza", 500)
	sara := newReseller(t, admins, "sara", 0)
	now := time.Now().UTC()
	in2Days := now.Add(48 * time.Hour)
	in1Day := now.Add(24 * time.Hour)
	in10Days := now.Add(10 * 24 * time.Hour)

	mk := func(ctx context.Context, name string, limit int, expires *time.Time) *model.Client {
		t.Helper()
		c, err := clients.Create(ctx, CreateInput{
			Name: name, InterfaceIDs: []uint{1}, DeviceLimit: limit, ExpiresAt: expires,
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return c
	}

	rezaActive := mk(asReseller(reza, 1), "reza-active", 2, nil)
	mk(asReseller(reza, 1), "reza-expiring", 3, &in2Days)
	rezaExpired := mk(asReseller(reza, 1), "reza-expired", 4, nil)
	if err := db.Model(&model.Client{}).Where("id = ?", rezaExpired.ID).
		Update("status", model.StatusExpired).Error; err != nil {
		t.Fatal(err)
	}
	saraExpiring := mk(asReseller(sara, 1), "sara-expiring", 5, &in1Day)
	mk(asReseller(sara, 1), "sara-later", 1, &in10Days)
	mk(context.Background(), "owner-one", 7, nil)

	// Traffic: reza's customer carried 3,000 bytes this month and a pile
	// last month that is not this month's; sara's carried 10,000 this
	// month, none of which is reza's.
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	sample := func(clientID uint, at time.Time, rx, tx uint64) {
		t.Helper()
		if err := db.Create(&model.TrafficSample{
			ClientID: clientID, BucketTS: at, Granularity: model.GranularityFine,
			RX: rx, TX: tx,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	sample(rezaActive.ID, now, 1000, 2000)
	sample(rezaActive.ID, monthStart.Add(-time.Hour), 999999, 999999)
	sample(saraExpiring.ID, now, 5000, 5000)

	mine, err := clients.Mine(asReseller(reza, 1))
	if err != nil {
		t.Fatal(err)
	}
	if mine.Clients != 3 || mine.Active != 2 || mine.Expired != 1 {
		t.Errorf("reza's dashboard counts %d customers (%d active, %d expired), want their own 3 (2, 1)",
			mine.Clients, mine.Active, mine.Expired)
	}
	if mine.ExpiringSoon != 1 {
		t.Errorf("reza is told %d customers are expiring soon, want their own 1", mine.ExpiringSoon)
	}
	if len(mine.Expiring) != 1 || mine.Expiring[0].Name != "reza-expiring" {
		t.Errorf("reza's expiring list is %+v, want only reza-expiring", mine.Expiring)
	}
	// The ceiling counts users -- what each plan is for -- over every
	// customer they hold, the expired one included, as checkCeiling does.
	if mine.CeilingUsed != 9 {
		t.Errorf("reza's ceiling reads %d users used, want 2+3+4 = 9", mine.CeilingUsed)
	}
	if mine.CeilingLimit != 500 {
		t.Errorf("reza's ceiling limit is %d, want the 500 they were sold", mine.CeilingLimit)
	}
	if mine.MonthUsedBytes != 3000 {
		t.Errorf("reza's month reads %d bytes, want their own 3000 from this month alone",
			mine.MonthUsedBytes)
	}

	other, err := clients.Mine(asReseller(sara, 1))
	if err != nil {
		t.Fatal(err)
	}
	if other.Clients != 2 || other.ExpiringSoon != 1 || other.MonthUsedBytes != 10000 {
		t.Errorf("sara's dashboard reads %+v, want her own 2 customers, 1 expiring, 10000 bytes",
			other)
	}
	if len(other.Expiring) != 1 || other.Expiring[0].Name != "sara-expiring" {
		t.Errorf("sara's expiring list is %+v, want only sara-expiring", other.Expiring)
	}
	if other.CeilingLimit != 0 {
		t.Errorf("sara's ceiling limit is %d, want 0 (no limit)", other.CeilingLimit)
	}

	// The owner asking is answered about the owner's own customers: the
	// set is their id alone, and a reseller's book is not in it.
	owners, err := clients.Mine(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if owners.Clients != 1 {
		t.Errorf("the owner's own dashboard counts %d customers, want only their own 1", owners.Clients)
	}
}
