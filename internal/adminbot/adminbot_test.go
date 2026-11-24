package adminbot

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// The manager is tested against the real store on a real database: the
// rules these tests exist for are that a config comes back as it was
// written (the token above all — a bot whose token is lost at a rewrite
// is a bot that dies at its next restart) and that the fleet machinery
// treats a switched-off bot as the nothing it is.

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// An operator with no row reads as the zero config — disabled, no
// token, their id on it — and a saved config reads back whole, the
// token included: the manager is an internal caller, and the token is
// the bot.
func TestConfigRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	m := New(db, nil, nil, quietLog())

	cfg, err := m.ConfigFor(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OperatorID != 7 || cfg.Enabled || cfg.Token != "" || cfg.AdminChatID != 0 {
		t.Fatalf("absent config read as %+v, want the zero config for operator 7", cfg)
	}

	want := &Config{OperatorID: 7, Enabled: true, Token: "123456:TEST-TOKEN", AdminChatID: 4242}
	if err := m.SaveConfig(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := m.ConfigFor(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if *got != *want {
		t.Fatalf("config read back as %+v, want %+v", got, want)
	}

	// A second save replaces the row rather than adding another: one
	// operator, one bot.
	got.AdminChatID = 5151
	got.Token = "123456:NEW-TOKEN"
	if err := m.SaveConfig(ctx, got); err != nil {
		t.Fatal(err)
	}
	var rows int64
	if err := db.Model(&model.AdminBot{}).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("%d config rows after two saves, want 1", rows)
	}
	again, err := m.ConfigFor(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if again.AdminChatID != 5151 || again.Token != "123456:NEW-TOKEN" {
		t.Fatalf("config after the second save is %+v", again)
	}

	// And one operator's save is not another's config.
	other, err := m.ConfigFor(ctx, 8)
	if err != nil {
		t.Fatal(err)
	}
	if other.Enabled || other.Token != "" {
		t.Fatalf("operator 8 read operator 7's config: %+v", other)
	}

	if err := m.SaveConfig(ctx, &Config{}); err == nil {
		t.Fatal("a config for no operator was saved")
	}
}

func TestBotInfoWithNoBots(t *testing.T) {
	m := New(testDB(t), nil, nil, quietLog())
	if name, running := m.BotInfo(7); name != "" || running {
		t.Fatalf("BotInfo = (%q, %v), want (\"\", false)", name, running)
	}
}

// A disabled bot is not started, not restarted and not missed: Reload
// over one — before Start, and after it — is a no-op, and the manager
// keeps reporting it as not running.
func TestReloadOnADisabledConfigIsANoOp(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	m := New(db, nil, nil, quietLog())
	if err := m.SaveConfig(ctx, &Config{
		OperatorID: 7, Enabled: false, Token: "123456:TEST-TOKEN", AdminChatID: 4242,
	}); err != nil {
		t.Fatal(err)
	}

	// Before Start there is no fleet to bring in line; the save is
	// already what Start will boot from.
	m.Reload(7)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	m.Start(runCtx)
	m.Reload(7)

	if name, running := m.BotInfo(7); name != "" || running {
		t.Fatalf("a disabled bot is running: (%q, %v)", name, running)
	}
}
