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

// Presets and the shop brand: an operator's own, read and written by
// nobody else. Two resellers work side by side here, and each is audited
// for the other's leavings -- a list that showed both, a delete that
// reached across, a brand written onto the wrong row.

func presetServer(t *testing.T) (*Server, *gorm.DB) {
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
		log:       log,
		throttle:  newThrottle(),
		jwtSecret: []byte("a test session key, 32 bytes!!!"),
		admins:    operators.NewAdmins(db, log),
		presets:   operators.NewClientPresets(db, log),
	}, db
}

func presetList(t *testing.T, s *Server, who *model.Admin) []model.ClientPreset {
	t.Helper()
	w := httptest.NewRecorder()
	s.handlePresetList(w, asAdmin(who, http.MethodGet, "/api/presets", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list for %s: %d %s", who.Username, w.Code, w.Body)
	}
	var rows []model.ClientPreset
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestPresetsBelongToTheirOperator(t *testing.T) {
	s, db := presetServer(t)
	reza := admin(t, db, model.Admin{Username: "reza", Role: model.RoleReseller}, "a long password")
	sara := admin(t, db, model.Admin{Username: "sara", Role: model.RoleReseller}, "a long password")

	// Reza saves the shape of the plan he sells.
	w := httptest.NewRecorder()
	s.handlePresetCreate(w, asAdmin(reza, http.MethodPost, "/api/presets", map[string]any{
		"name": "50 GB monthly", "interfaceIds": []uint{2, 1, 2},
		"quotaBytes": 53687091200, "days": 30, "deviceLimit": 2,
	}))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var made model.ClientPreset
	if err := json.Unmarshal(w.Body.Bytes(), &made); err != nil {
		t.Fatal(err)
	}
	if made.AdminID != reza.ID || made.Name != "50 GB monthly" ||
		made.QuotaBytes != 53687091200 || made.Days != 30 || made.DeviceLimit != 2 {
		t.Errorf("the saved preset is not what was shaped: %+v", made)
	}
	if got := made.InterfaceIDs.V; len(got) != 2 || got[0] != 2 || got[1] != 1 {
		t.Errorf("interfaceIds = %v, want the tunnels once each, in order", got)
	}

	// Reza's list holds it; Sara's is empty.
	if rows := presetList(t, s, reza); len(rows) != 1 || rows[0].ID != made.ID {
		t.Errorf("reza's list = %+v, want his one preset", rows)
	}
	if rows := presetList(t, s, sara); len(rows) != 0 {
		t.Errorf("sara's list = %+v, want none of reza's", rows)
	}

	// Sara cannot delete what is not hers: the answer is the one a
	// missing preset gets, and Reza's preset is still there.
	r := asAdmin(sara, http.MethodDelete, "/api/presets/"+strconv.Itoa(int(made.ID)), nil)
	r.SetPathValue("id", strconv.Itoa(int(made.ID)))
	w = httptest.NewRecorder()
	s.handlePresetDelete(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("sara deleting reza's preset: %d, want 404", w.Code)
	}
	if rows := presetList(t, s, reza); len(rows) != 1 {
		t.Fatalf("reza's preset did not survive sara's delete: %+v", rows)
	}

	// Reza deletes his own, and it is gone.
	r = asAdmin(reza, http.MethodDelete, "/api/presets/"+strconv.Itoa(int(made.ID)), nil)
	r.SetPathValue("id", strconv.Itoa(int(made.ID)))
	w = httptest.NewRecorder()
	s.handlePresetDelete(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("reza deleting his own preset: %d %s", w.Code, w.Body)
	}
	if rows := presetList(t, s, reza); len(rows) != 0 {
		t.Errorf("reza's list after his delete = %+v, want empty", rows)
	}
}

func TestPresetCreateRefusesANamelessShape(t *testing.T) {
	s, db := presetServer(t)
	reza := admin(t, db, model.Admin{Username: "reza", Role: model.RoleReseller}, "a long password")
	w := httptest.NewRecorder()
	s.handlePresetCreate(w, asAdmin(reza, http.MethodPost, "/api/presets", map[string]any{
		"name": "  ", "interfaceIds": []uint{1}, "days": 30,
	}))
	if w.Code != http.StatusBadRequest {
		t.Errorf("a nameless preset: %d, want 400", w.Code)
	}
	if rows := presetList(t, s, reza); len(rows) != 0 {
		t.Errorf("the refused preset was stored anyway: %+v", rows)
	}
}

// The shop brand is written through /me, which takes the operator from
// the session: Reza setting his name and contact writes Reza's row, and
// Sara's stays exactly as it was.
func TestShopBrandIsWrittenOnlyOnTheCallersRow(t *testing.T) {
	s, db := presetServer(t)
	reza := admin(t, db, model.Admin{Username: "reza", Role: model.RoleReseller}, "a long password")
	sara := admin(t, db, model.Admin{Username: "sara", Role: model.RoleReseller}, "a long password")

	w := httptest.NewRecorder()
	s.handleUpdateMe(w, asAdmin(reza, http.MethodPatch, "/api/auth/me", map[string]any{
		"shopName": "Reza VPN", "supportContact": "@reza_support",
	}))
	if w.Code != http.StatusOK {
		t.Fatalf("reza's brand update: %d %s", w.Code, w.Body)
	}
	var view struct {
		ShopName       string `json:"shopName"`
		SupportContact string `json:"supportContact"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ShopName != "Reza VPN" || view.SupportContact != "@reza_support" {
		t.Errorf("the answer carries (%q, %q), want reza's brand", view.ShopName, view.SupportContact)
	}

	var rezaRow, saraRow model.Admin
	if err := db.First(&rezaRow, reza.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&saraRow, sara.ID).Error; err != nil {
		t.Fatal(err)
	}
	if rezaRow.ShopName != "Reza VPN" || rezaRow.SupportContact != "@reza_support" {
		t.Errorf("reza's row = (%q, %q), want his brand stored", rezaRow.ShopName, rezaRow.SupportContact)
	}
	if saraRow.ShopName != "" || saraRow.SupportContact != "" {
		t.Errorf("sara's row = (%q, %q), want it untouched", saraRow.ShopName, saraRow.SupportContact)
	}

	// And /me reads each operator their own: Sara is offered no brand
	// but the empty one she has.
	w = httptest.NewRecorder()
	s.handleMe(w, asAdmin(sara, http.MethodGet, "/api/auth/me", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("sara's /me: %d %s", w.Code, w.Body)
	}
	view.ShopName, view.SupportContact = "unset", "unset"
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.ShopName != "" || view.SupportContact != "" {
		t.Errorf("sara's /me carries (%q, %q), want her own empty brand", view.ShopName, view.SupportContact)
	}
}
