package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abolfazl/w-ui/internal/adminbot"
	"github.com/abolfazl/w-ui/internal/database/model"
)

// The management-bot handlers over the real manager and store: the
// rules these tests exist for are the shop page's rules again — one
// operator's token never read back, and one operator's bot never
// visible to another — because a token leaked to the wrong seller is
// that seller's customers handed over.

type adminBotFixture struct {
	srv    *Server
	mgr    *adminbot.Manager
	owner  *model.Admin
	seller *model.Admin
	other  *model.Admin
}

func newAdminBotFixture(t *testing.T, withMgr bool) *adminBotFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mk := func(username string, role model.AdminRole, parent uint) *model.Admin {
		a := &model.Admin{Username: username, PasswordHash: "x", Role: role,
			ParentAdminID: parent, Enabled: true}
		if err := db.Create(a).Error; err != nil {
			t.Fatalf("operator %s: %v", username, err)
		}
		return a
	}
	f := &adminBotFixture{}
	f.owner = mk("owner", model.RoleOwner, 0)
	f.seller = mk("hussain", model.RoleReseller, f.owner.ID)
	f.other = mk("other", model.RoleReseller, f.owner.ID)
	f.srv = &Server{log: log}
	if withMgr {
		f.mgr = adminbot.New(db, nil, nil, log)
		f.srv.adminBot = f.mgr
	}
	return f
}

type adminBotView struct {
	Enabled     bool   `json:"enabled"`
	TokenSet    bool   `json:"tokenSet"`
	AdminChatID int64  `json:"adminChatId"`
	BotRunning  bool   `json:"botRunning"`
	BotUsername string `json:"botUsername"`
}

func adminBotDo(s *Server, h http.HandlerFunc, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func readAdminBotView(t *testing.T, rec *httptest.ResponseRecorder) adminBotView {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var v adminBotView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %v: %s", err, rec.Body)
	}
	return v
}

// A caller who never set a bot up is told so, plainly: nothing set,
// nothing running.
func TestAdminBotConfigStartsEmpty(t *testing.T) {
	f := newAdminBotFixture(t, true)
	rec := adminBotDo(f.srv, f.srv.handleAdminBotConfig,
		asAdmin(f.seller, http.MethodGet, "/api/admin-bot/config", nil))
	v := readAdminBotView(t, rec)
	if v.Enabled || v.TokenSet || v.AdminChatID != 0 || v.BotRunning || v.BotUsername != "" {
		t.Fatalf("a fresh config read as %+v", v)
	}
}

// The token is written, kept and replaced by the rules the shop's page
// set: sending one stores it (and never echoes it back), sending none —
// or an empty one — keeps the one on file, and sending a new one
// replaces it.
func TestAdminBotConfigSaveTokenRules(t *testing.T) {
	f := newAdminBotFixture(t, true)
	ctx := context.Background()
	put := func(body any) *httptest.ResponseRecorder {
		return adminBotDo(f.srv, f.srv.handleAdminBotConfigSave,
			asAdmin(f.seller, http.MethodPut, "/api/admin-bot/config", body))
	}
	stored := func() *adminbot.Config {
		cfg, err := f.mgr.ConfigFor(ctx, f.seller.ID)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}

	rec := put(map[string]any{"enabled": true, "token": "111:SECRET-ONE", "adminChatId": 555})
	if strings.Contains(rec.Body.String(), "SECRET-ONE") {
		t.Fatalf("the token itself is in the answer: %s", rec.Body)
	}
	if v := readAdminBotView(t, rec); !v.TokenSet || !v.Enabled || v.AdminChatID != 555 {
		t.Fatalf("after the save the view is %+v", v)
	}
	if cfg := stored(); cfg.Token != "111:SECRET-ONE" || cfg.AdminChatID != 555 || !cfg.Enabled {
		t.Fatalf("stored config is %+v", cfg)
	}

	// No token in the body: the one on file is kept, the rest moves.
	rec = put(map[string]any{"enabled": true, "adminChatId": 777})
	_ = readAdminBotView(t, rec)
	if cfg := stored(); cfg.Token != "111:SECRET-ONE" || cfg.AdminChatID != 777 {
		t.Fatalf("a tokenless save changed the token: %+v", cfg)
	}

	// An empty token is also "no change" — it is what a page that never
	// held the value sends.
	rec = put(map[string]any{"enabled": true, "token": "", "adminChatId": 777})
	_ = readAdminBotView(t, rec)
	if cfg := stored(); cfg.Token != "111:SECRET-ONE" {
		t.Fatalf("an empty token cleared the stored one: %+v", cfg)
	}

	// A new token replaces the old.
	rec = put(map[string]any{"enabled": true, "token": "222:SECRET-TWO", "adminChatId": 777})
	if strings.Contains(rec.Body.String(), "SECRET-TWO") {
		t.Fatalf("the new token itself is in the answer: %s", rec.Body)
	}
	_ = readAdminBotView(t, rec)
	if cfg := stored(); cfg.Token != "222:SECRET-TWO" {
		t.Fatalf("the token was not replaced: %+v", cfg)
	}
}

// Each caller sees their own bot and only their own: a config one
// operator saved is invisible to the next, who reads their own empty
// one instead.
func TestAdminBotConfigIsTheCallersOwn(t *testing.T) {
	f := newAdminBotFixture(t, true)
	rec := adminBotDo(f.srv, f.srv.handleAdminBotConfigSave,
		asAdmin(f.seller, http.MethodPut, "/api/admin-bot/config",
			map[string]any{"enabled": true, "token": "111:SECRET-ONE", "adminChatId": 555}))
	_ = readAdminBotView(t, rec)

	for _, who := range []*model.Admin{f.other, f.owner} {
		rec := adminBotDo(f.srv, f.srv.handleAdminBotConfig,
			asAdmin(who, http.MethodGet, "/api/admin-bot/config", nil))
		if v := readAdminBotView(t, rec); v.TokenSet || v.Enabled || v.AdminChatID != 0 {
			t.Fatalf("%s read the seller's bot as %+v", who.Username, v)
		}
	}
	rec = adminBotDo(f.srv, f.srv.handleAdminBotConfig,
		asAdmin(f.seller, http.MethodGet, "/api/admin-bot/config", nil))
	if v := readAdminBotView(t, rec); !v.TokenSet || v.AdminChatID != 555 {
		t.Fatalf("the seller's own config read as %+v", v)
	}
}

// A panel built without the manager has no management bot to show:
// both handlers answer 404, as the shop's do without the shop.
func TestAdminBotHandlersWithoutAManager(t *testing.T) {
	f := newAdminBotFixture(t, false)
	rec := adminBotDo(f.srv, f.srv.handleAdminBotConfig,
		asAdmin(f.seller, http.MethodGet, "/api/admin-bot/config", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET without a manager: status %d, body %s", rec.Code, rec.Body)
	}
	rec = adminBotDo(f.srv, f.srv.handleAdminBotConfigSave,
		asAdmin(f.seller, http.MethodPut, "/api/admin-bot/config",
			map[string]any{"enabled": true, "token": "111:SECRET-ONE", "adminChatId": 555}))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PUT without a manager: status %d, body %s", rec.Code, rec.Body)
	}
}
