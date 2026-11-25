package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/service"
)

// The activity record read by somebody who is not the owner: their own
// lines, and only theirs. The record names every operator and the
// addresses they work from, so the narrowing is the feature -- and it is
// proved here against requests that ask, in every way the request can
// ask, for somebody else's.

// activityFixture is a server with a record in it: two resellers' work,
// their sign-ins (kept under the name tried, with no id, as the login
// handlers keep them), a machine token's work, and the owner's.
type activityFixture struct {
	srv   *Server
	owner *model.Admin
	reza  *model.Admin
	sara  *model.Admin
}

func newActivityFixture(t *testing.T) *activityFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&model.ActivityEntry{}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	f := &activityFixture{
		srv:   &Server{log: log, activity: service.NewActivity(db, log)},
		owner: &model.Admin{ID: 1, Username: "owner", Role: model.RoleOwner},
		reza:  &model.Admin{ID: 2, Username: "reza", Role: model.RoleReseller},
		sara:  &model.Admin{ID: 3, Username: "sara", Role: model.RoleReseller},
	}
	seed := []model.ActivityEntry{
		{Actor: "reza", ActorID: 2, Role: "reseller", Action: "POST /api/clients", Status: 201, IP: "203.0.113.1"},
		{Actor: "reza", Action: "POST /api/auth/login", Status: 200, IP: "203.0.113.1"},
		{Actor: "reza", Action: "POST /api/auth/login", Status: 401, IP: "198.51.100.9"},
		{Actor: "sara", ActorID: 3, Role: "reseller", Action: "DELETE /api/clients/9", Status: 200, IP: "203.0.113.2"},
		{Actor: "sara", Action: "POST /api/auth/login", Status: 200, IP: "203.0.113.2"},
		{Actor: "token", Action: "POST /api/clients", Status: 200, IP: "192.0.2.1"},
		{Actor: "owner", ActorID: 1, Role: "owner", Action: "PUT /api/settings", Status: 200, IP: "203.0.113.3"},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *activityFixture) read(t *testing.T, who *model.Admin, path string) []activityView {
	t.Helper()
	rec := httptest.NewRecorder()
	f.srv.handleActivity(rec, asAdmin(who, http.MethodGet, path, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("%s reading %s: status %d, body %s", who.Username, path, rec.Code, rec.Body)
	}
	var out struct {
		Items []activityView `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Items
}

func actorsOf(items []activityView) map[string]int {
	out := map[string]int{}
	for _, it := range items {
		out[it.Actor]++
	}
	return out
}

func TestAResellerReadsOnlyTheirOwnActivity(t *testing.T) {
	f := newActivityFixture(t)

	// Reza's own work and the sign-ins under their name -- including the
	// failed one from an address that is not theirs, which is theirs to
	// know about. Nothing of sara's, the token's or the owner's.
	items := f.read(t, f.reza, "/api/activity")
	if got := actorsOf(items); len(items) != 3 || got["reza"] != 3 {
		t.Fatalf("reza reads %d entries %v, want their own 3", len(items), got)
	}

	// Asking for somebody else's, by every name the request offers,
	// changes nothing: the narrowing is the caller's id, not the query.
	for _, path := range []string{
		"/api/activity?actor=sara",
		"/api/activity?actorId=3",
		"/api/activity?actor=sara&actorId=3&search=sara&target=sara",
	} {
		items := f.read(t, f.reza, path)
		if got := actorsOf(items); len(items) != 3 || got["reza"] != 3 {
			t.Errorf("%s: reza reads %d entries %v, want their own 3", path, len(items), got)
		}
	}

	// Sara likewise, and the two never overlap.
	items = f.read(t, f.sara, "/api/activity")
	if got := actorsOf(items); len(items) != 2 || got["sara"] != 2 {
		t.Fatalf("sara reads %d entries %v, want her own 2", len(items), got)
	}

	// Paging stays inside the caller's own lines.
	page := f.read(t, f.reza, "/api/activity?limit=2")
	if len(page) != 2 {
		t.Fatalf("reza's first page is %d entries, want 2", len(page))
	}
	rest := f.read(t, f.reza, "/api/activity?limit=2&before="+strconv.FormatUint(uint64(page[len(page)-1].ID), 10))
	if len(rest) != 1 || rest[0].Actor != "reza" {
		t.Fatalf("reza's second page is %+v, want the one remaining entry of theirs", rest)
	}
}

func TestTheOwnerStillReadsTheWholeActivityRecord(t *testing.T) {
	f := newActivityFixture(t)
	items := f.read(t, f.owner, "/api/activity")
	if len(items) != 7 {
		t.Fatalf("the owner reads %d entries, want the whole record of 7", len(items))
	}
}
