package fleet

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/handover"
)

// testDB is a fresh database with the panel's schema, in memory.
func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// A node's address is a panel's address, and nothing else: a path, a query,
// a fragment or credentials pasted on it would ride into every call the
// syncer builds from it, and the panel would answer 404 to all of them.
func TestANodeAddressCarriesNoPath(t *testing.T) {
	s := NewNodes(testDB(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, addr := range []string{
		"https://host:2096/login",
		"https://host:2096/?x=1",
		"https://host:2096/#frag",
		"https://user:pass@host:2096",
	} {
		if _, err := s.Create(context.Background(), NodeInput{Name: "n", Address: addr, Token: "wui_x"}); err == nil {
			t.Errorf("%q was accepted", addr)
		}
	}
	if _, err := s.Create(context.Background(), NodeInput{Name: "ok", Address: "https://host:2096/", Token: "wui_x"}); err != nil {
		t.Errorf("a trailing slash was refused: %v", err)
	}
}

// A tunnel of a protocol no driver serves, or an OpenVPN tunnel without its
// certificate, is refused at the door: stored, it would fail every tick
// while the panel that sent it hears success.
func TestApplyRefusesWhatNoDriverCanServe(t *testing.T) {
	s := NewNodeSync(testDB(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	for _, state := range []NodeState{
		{Interface: NodeInterface{OriginID: 1, Name: "bogus", Protocol: "bogus", Enabled: true}},
		{Interface: NodeInterface{OriginID: 2, Name: "ovpn", Protocol: model.ProtocolOpenVPN, Enabled: true}},
	} {
		if err := s.Apply(ctx, 1, state); err == nil {
			t.Errorf("%s was accepted", state.Interface.Name)
		}
	}
	if err := s.Apply(ctx, 1, NodeState{Interface: NodeInterface{
		OriginID: 3, Name: "ovpn", Protocol: model.ProtocolOpenVPN, Enabled: true,
		OpenVPN: &model.OpenVPNParams{CACert: "ca"},
	}}); err != nil {
		t.Errorf("an OpenVPN tunnel with its certificate was refused: %v", err)
	}
}

// A handover names only what is there to hand over, and still this panel's
// own: a customer or a file that does not exist — or one another panel
// already manages — is not handed over by being named.
func TestClaimRefusesWhatIsNotThisPanelsOwn(t *testing.T) {
	db := testDB(t)
	tunnel := model.Interface{NodeID: 1, Name: "wg0", Protocol: model.ProtocolWireGuard, ListenPort: 51820, Subnet: "10.66.0.0/16", EndpointHost: "1.2.3.4"}
	if err := db.Create(&tunnel).Error; err != nil {
		t.Fatal(err)
	}
	client := model.Client{Name: "alice"}
	if err := db.Create(&client).Error; err != nil {
		t.Fatal(err)
	}
	acc := model.Account{ClientID: client.ID, InterfaceID: tunnel.ID, NodeID: 1, DeviceName: "alice-1", IP: "10.66.0.2"}
	if err := db.Create(&acc).Error; err != nil {
		t.Fatal(err)
	}
	s := NewNodeSync(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	good := handover.Claim{
		Tunnels:  []handover.IDPair{{LocalID: tunnel.ID, OriginID: 100}},
		Clients:  []handover.IDPair{{LocalID: client.ID, OriginID: 200}},
		Accounts: []handover.IDPair{{LocalID: acc.ID, OriginID: 300}},
	}
	bad := good
	bad.Clients = []handover.IDPair{{LocalID: 999, OriginID: 200}}
	if err := s.Claim(ctx, 1, bad); err == nil {
		t.Error("a customer that does not exist was claimed")
	}
	bad = good
	bad.Accounts = []handover.IDPair{{LocalID: 999, OriginID: 300}}
	if err := s.Claim(ctx, 1, bad); err == nil {
		t.Error("a file that does not exist was claimed")
	}
	if err := s.Claim(ctx, 1, good); err != nil {
		t.Fatalf("a real handover was refused: %v", err)
	}
	// And once handed over, it cannot be handed over again.
	if err := s.Claim(ctx, 1, good); err == nil {
		t.Error("an already managed customer was claimed again")
	}
}
