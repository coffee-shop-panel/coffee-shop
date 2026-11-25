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
	"github.com/abolfazl/w-ui/internal/operators"
)

// Announcements: the owner's word to the room, and each operator's
// own putting-away of it. Who may write is the gates' business and
// is pinned in the route table; what is audited here is the reading
// -- a dismissal that hid a banner for the wrong operator, or a
// delete that left one standing, is the failure these tests exist
// to catch.

func announcementServer(t *testing.T) (*Server, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &Server{
		log:           log,
		throttle:      newThrottle(),
		jwtSecret:     []byte("a test session key, 32 bytes!!!"),
		admins:        operators.NewAdmins(db, log),
		announcements: operators.NewAnnouncements(db, log),
	}, db
}

func publish(t *testing.T, s *Server, owner *model.Admin, text string) model.Announcement {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAnnouncementCreate(w, asAdmin(owner, http.MethodPost, "/api/announcements", map[string]any{
		"text": text,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("publish %q: %d %s", text, w.Code, w.Body)
	}
	var row model.Announcement
	if err := json.Unmarshal(w.Body.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	return row
}

func mineList(t *testing.T, s *Server, who *model.Admin) []model.Announcement {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleAnnouncementMine(w, asAdmin(who, http.MethodGet, "/api/announcements/mine", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("mine for %s: %d %s", who.Username, w.Code, w.Body)
	}
	var rows []model.Announcement
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func dismiss(t *testing.T, s *Server, who *model.Admin, id uint) int {
	t.Helper()
	r := asAdmin(who, http.MethodPost, "/api/announcements/"+strconv.Itoa(int(id))+"/dismiss", nil)
	r.SetPathValue("id", strconv.Itoa(int(id)))
	w := httptest.NewRecorder()
	s.handleAnnouncementDismiss(w, r)
	return w.Code
}

// A reseller sees an announcement until they dismiss it, then never
// again -- and their dismissal is theirs alone: another reseller
// still sees it, and the owner's history keeps it.
func TestAnAnnouncementStandsUntilTheReaderDismissesIt(t *testing.T) {
	s, db := announcementServer(t)
	owner := admin(t, db, model.Admin{Username: "owner", Role: model.RoleOwner}, "a long password")
	reza := admin(t, db, model.Admin{Username: "reza", Role: model.RoleReseller}, "a long password")
	sara := admin(t, db, model.Admin{Username: "sara", Role: model.RoleReseller}, "a long password")

	first := publish(t, s, owner, "The Turkey tunnel is down tonight, 02:00-03:00.")
	second := publish(t, s, owner, "Prices change on the first of next month.")
	if first.CreatedBy != owner.ID {
		t.Errorf("the announcement is stamped with creator %d, want the owner %d", first.CreatedBy, owner.ID)
	}

	// Both resellers see both, newest first.
	for _, who := range []*model.Admin{reza, sara} {
		rows := mineList(t, s, who)
		if len(rows) != 2 || rows[0].ID != second.ID || rows[1].ID != first.ID {
			t.Fatalf("%s's banners = %+v, want both, newest first", who.Username, rows)
		}
	}

	// Reza puts the first away. It leaves his list and only his.
	if code := dismiss(t, s, reza, first.ID); code != http.StatusOK {
		t.Fatalf("reza dismissing: %d, want 200", code)
	}
	if rows := mineList(t, s, reza); len(rows) != 1 || rows[0].ID != second.ID {
		t.Errorf("reza's banners after his dismissal = %+v, want only the second", rows)
	}
	if rows := mineList(t, s, sara); len(rows) != 2 {
		t.Errorf("sara's banners after reza's dismissal = %+v, want both still", rows)
	}

	// Dismissing again is the double-tap, not an error, and changes
	// nothing.
	if code := dismiss(t, s, reza, first.ID); code != http.StatusOK {
		t.Fatalf("reza dismissing twice: %d, want 200", code)
	}
	var reads int64
	if err := db.Model(&model.AnnouncementRead{}).
		Where("announcement_id = ? AND operator_id = ?", first.ID, reza.ID).Count(&reads).Error; err != nil {
		t.Fatal(err)
	}
	if reads != 1 {
		t.Errorf("reza's dismissal wrote %d read rows, want exactly 1", reads)
	}

	// The owner's history still holds both.
	w := httptest.NewRecorder()
	s.handleAnnouncementList(w, asAdmin(owner, http.MethodGet, "/api/announcements", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("owner's list: %d %s", w.Code, w.Body)
	}
	var all []model.Announcement
	if err := json.Unmarshal(w.Body.Bytes(), &all); err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != second.ID {
		t.Errorf("the owner's history = %+v, want both, newest first", all)
	}
}

// Deleting an announcement takes it back for everyone: it leaves
// every list, dismissed or not, and its dismissals go with it.
func TestDeletingAnAnnouncementTakesItBackForEveryone(t *testing.T) {
	s, db := announcementServer(t)
	owner := admin(t, db, model.Admin{Username: "owner", Role: model.RoleOwner}, "a long password")
	reza := admin(t, db, model.Admin{Username: "reza", Role: model.RoleReseller}, "a long password")
	sara := admin(t, db, model.Admin{Username: "sara", Role: model.RoleReseller}, "a long password")

	row := publish(t, s, owner, "Ignore the last message.")
	if code := dismiss(t, s, reza, row.ID); code != http.StatusOK {
		t.Fatalf("reza dismissing: %d, want 200", code)
	}

	r := asAdmin(owner, http.MethodDelete, "/api/announcements/"+strconv.Itoa(int(row.ID)), nil)
	r.SetPathValue("id", strconv.Itoa(int(row.ID)))
	w := httptest.NewRecorder()
	s.handleAnnouncementDelete(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("owner deleting: %d %s", w.Code, w.Body)
	}

	if rows := mineList(t, s, sara); len(rows) != 0 {
		t.Errorf("sara still sees the taken-back announcement: %+v", rows)
	}
	var reads int64
	if err := db.Model(&model.AnnouncementRead{}).Count(&reads).Error; err != nil {
		t.Fatal(err)
	}
	if reads != 0 {
		t.Errorf("the delete left %d dismissal rows behind, want none", reads)
	}

	// And a second delete is the answer a missing one gets.
	w = httptest.NewRecorder()
	s.handleAnnouncementDelete(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("deleting twice: %d, want 404", w.Code)
	}
}

// An announcement with no words is refused, and a dismissal of one
// that does not exist is refused the same way -- nothing is written
// either time.
func TestEmptyAndMissingAnnouncementsAreRefused(t *testing.T) {
	s, db := announcementServer(t)
	owner := admin(t, db, model.Admin{Username: "owner", Role: model.RoleOwner}, "a long password")
	reza := admin(t, db, model.Admin{Username: "reza", Role: model.RoleReseller}, "a long password")

	w := httptest.NewRecorder()
	s.handleAnnouncementCreate(w, asAdmin(owner, http.MethodPost, "/api/announcements", map[string]any{
		"text": "   ",
	}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("an empty announcement: %d, want 400", w.Code)
	}

	if code := dismiss(t, s, reza, 999); code != http.StatusNotFound {
		t.Errorf("dismissing a missing announcement: %d, want 404", code)
	}

	var rows int64
	if err := db.Model(&model.Announcement{}).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.AnnouncementRead{}).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("the refusals left %d rows behind, want none", rows)
	}
}
