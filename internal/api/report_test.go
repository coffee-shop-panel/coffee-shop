package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/ipam"
	"github.com/abolfazl/w-ui/internal/operators"
	"github.com/abolfazl/w-ui/internal/scope"
	"github.com/abolfazl/w-ui/internal/service"
)

// The monthly report, served: each operator is handed their own month
// and no one else's, the month asked for is the month answered, and a
// month that does not parse is refused rather than quietly replaced
// with this one.

type reportAnswer struct {
	Month             string `json:"month"`
	Created           int64  `json:"created"`
	CreatedQuotaBytes uint64 `json:"createdQuotaBytes"`
	Renewals          int64  `json:"renewals"`
	UsedBytes         uint64 `json:"usedBytes"`
	Days              []struct {
		Date  string `json:"date"`
		Bytes uint64 `json:"bytes"`
	} `json:"days"`
	Top []struct {
		Name  string `json:"name"`
		Bytes uint64 `json:"bytes"`
	} `json:"top"`
}

func TestTheMonthlyReportIsTheCallersOwn(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	if err := scope.Register(db); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := &Server{
		log:     log,
		clients: service.NewClients(db, ipam.NewPools(), log),
		admins:  operators.NewAdmins(db, log),
	}

	mkAdmin := func(username string) *model.Admin {
		t.Helper()
		a := &model.Admin{Username: username, PasswordHash: "x", Role: model.RoleReseller, Enabled: true}
		if err := db.Create(a).Error; err != nil {
			t.Fatal(err)
		}
		return a
	}
	reza := mkAdmin("reza")
	sara := mkAdmin("sara")

	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	mkClient := func(owner uint, name string, quota uint64, created time.Time) *model.Client {
		t.Helper()
		c := &model.Client{Name: name, OwnerID: owner, QuotaBytes: quota, Status: model.StatusActive}
		if err := db.Create(c).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&model.Client{}).Where("id = ?", c.ID).
			Update("created_at", created).Error; err != nil {
			t.Fatal(err)
		}
		return c
	}
	own := mkClient(reza.ID, "reza-one", 100, start.Add(time.Hour))
	mkClient(sara.ID, "sara-one", 200, start.Add(time.Hour))

	if err := db.Create(&model.TrafficSample{
		ClientID: own.ID, BucketTS: start.Add(2 * time.Hour),
		Granularity: model.GranularityFine, RX: 3000, TX: 500,
	}).Error; err != nil {
		t.Fatal(err)
	}
	order := &model.ShopOrder{
		OperatorID: reza.ID, BuyerTGID: 7, PlanName: "plan",
		Kind: "renew", Status: "fulfilled", ClientID: own.ID, PublicToken: "tok-api-1",
	}
	if err := db.Create(order).Error; err != nil {
		t.Fatal(err)
	}

	ctxFor := func(admin *model.Admin) context.Context {
		t.Helper()
		base := context.WithValue(context.Background(), ctxAdmin, admin)
		sc, err := srv.scopeFor(base, admin)
		if err != nil {
			t.Fatal(err)
		}
		return service.WithScope(base, sc)
	}
	ask := func(admin *model.Admin, target string) (*httptest.ResponseRecorder, reportAnswer) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctxFor(admin))
		rec := httptest.NewRecorder()
		srv.handleOverviewReport(rec, req)
		var out reportAnswer
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
		}
		return rec, out
	}

	rec, got := ask(reza, "/api/overview/report")
	if rec.Code != http.StatusOK {
		t.Fatalf("reza's report: status %d, body %s", rec.Code, rec.Body)
	}
	if got.Month != start.Format("2006-01") || got.Created != 1 || got.CreatedQuotaBytes != 100 ||
		got.Renewals != 1 || got.UsedBytes != 3500 {
		t.Errorf("reza's month reads %+v, want this month, 1 made promising 100, 1 renewal, 3500 carried", got)
	}
	if len(got.Top) != 1 || got.Top[0].Name != "reza-one" {
		t.Errorf("reza's heaviest read %+v, want only reza-one", got.Top)
	}

	_, other := ask(sara, "/api/overview/report")
	if other.Created != 1 || other.CreatedQuotaBytes != 200 || other.Renewals != 0 || other.UsedBytes != 0 {
		t.Errorf("sara's month reads %+v, want her own 1 made promising 200 and nothing of reza's", other)
	}

	// The month asked for is the month answered: last month is empty for
	// both of them, and says so.
	prev := start.AddDate(0, -1, 0).Format("2006-01")
	_, last := ask(reza, "/api/overview/report?month="+prev)
	if last.Month != prev || last.Created != 0 || last.UsedBytes != 0 || len(last.Days) == 0 {
		t.Errorf("last month reads %+v, want %s with zeros and its days present", last, prev)
	}

	// A month that does not parse is refused, not replaced.
	rec, _ = ask(reza, "/api/overview/report?month=banana")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a nonsense month is answered %d, want 400", rec.Code)
	}

	// And a stranger is refused outright.
	req := httptest.NewRequest(http.MethodGet, "/api/overview/report", nil)
	rec = httptest.NewRecorder()
	srv.handleOverviewReport(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a stranger is answered %d, want 403", rec.Code)
	}
}
