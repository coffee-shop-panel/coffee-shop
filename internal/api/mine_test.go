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

	"github.com/abolfazl/w-ui/internal/backend"
	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/fleet"
	"github.com/abolfazl/w-ui/internal/ipam"
	"github.com/abolfazl/w-ui/internal/operators"
	"github.com/abolfazl/w-ui/internal/scope"
	"github.com/abolfazl/w-ui/internal/service"
)

// The reseller dashboard, served: every part of the answer is the
// caller's -- their figures, their tunnels and no others, their activity
// and no one else's. Two resellers with disjoint books and disjoint
// tunnels ask in turn, and each is audited for the other's leavings.

type mineFixture struct {
	srv     *Server
	clients *service.Clients
	reza    *model.Admin
	sara    *model.Admin
	wgA     model.Interface // reza's, on a remote node that is answering
	wgB     model.Interface // sara's, on this panel
	wgC     model.Interface // nobody's
}

func newMineFixture(t *testing.T) *mineFixture {
	t.Helper()
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

	local := model.Node{Name: "local", Kind: model.KindLocal, Reachable: true}
	remote := model.Node{Name: "remote", Kind: model.KindRemote, Reachable: true}
	for _, n := range []*model.Node{&local, &remote} {
		if err := db.Create(n).Error; err != nil {
			t.Fatal(err)
		}
	}
	pools := ipam.NewPools()
	f := &mineFixture{}
	mkIface := func(name string, nodeID uint, subnet string, port int) model.Interface {
		t.Helper()
		iface := model.Interface{
			Name: name, Protocol: model.ProtocolWireGuard, Enabled: true,
			ListenPort: port, Subnet: subnet, EndpointHost: "vpn.example.com", NodeID: nodeID,
		}
		if err := db.Create(&iface).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := pools.Add(iface.ID, subnet); err != nil {
			t.Fatal(err)
		}
		return iface
	}
	f.wgA = mkIface("wg-a", remote.ID, "10.66.0.0/24", 51820)
	f.wgB = mkIface("wg-b", local.ID, "10.67.0.0/24", 51821)
	f.wgC = mkIface("wg-c", remote.ID, "10.68.0.0/24", 51822)

	mkAdmin := func(username string, role model.AdminRole, limit int) *model.Admin {
		t.Helper()
		a := &model.Admin{Username: username, PasswordHash: "x", Role: role, Enabled: true, ClientLimit: limit}
		if err := db.Create(a).Error; err != nil {
			t.Fatal(err)
		}
		return a
	}
	mkAdmin("owner", model.RoleOwner, 0)
	f.reza = mkAdmin("reza", model.RoleReseller, 500)
	f.sara = mkAdmin("sara", model.RoleReseller, 0)
	for admin, iface := range map[*model.Admin]model.Interface{f.reza: f.wgA, f.sara: f.wgB} {
		if err := db.Create(&model.AdminInterface{AdminID: admin.ID, InterfaceID: iface.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}

	f.clients = service.NewClients(db, pools, log)
	f.srv = &Server{
		log:      log,
		clients:  f.clients,
		ifaces:   service.NewInterfaces(db, pools, log),
		nodes:    fleet.NewNodes(db, log),
		pool:     backend.NewPool(log),
		admins:   operators.NewAdmins(db, log),
		activity: service.NewActivity(db, log),
	}

	// One customer each, reza's about to run out.
	f.create(t, f.reza, "reza-expiring", f.wgA.ID, time.Now().UTC().Add(48*time.Hour))
	f.create(t, f.sara, "sara-plain", f.wgB.ID, time.Time{})

	// And a line of the record each.
	for _, e := range []model.ActivityEntry{
		{Actor: "reza", ActorID: f.reza.ID, Role: "reseller", Action: "POST /api/clients", Status: 201},
		{Actor: "sara", ActorID: f.sara.ID, Role: "reseller", Action: "POST /api/clients", Status: 201},
	} {
		if err := db.Create(&e).Error; err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// ctxFor is the context a request from that operator is served under:
// themselves, and the scope the panel's own scopeFor works out for them.
func (f *mineFixture) ctxFor(t *testing.T, admin *model.Admin) context.Context {
	t.Helper()
	base := context.WithValue(context.Background(), ctxAdmin, admin)
	sc, err := f.srv.scopeFor(base, admin)
	if err != nil {
		t.Fatal(err)
	}
	return service.WithScope(base, sc)
}

func (f *mineFixture) create(t *testing.T, admin *model.Admin, name string, ifaceID uint, expires time.Time) {
	t.Helper()
	in := service.CreateInput{Name: name, InterfaceIDs: []uint{ifaceID}, DeviceLimit: 2}
	if !expires.IsZero() {
		in.ExpiresAt = &expires
	}
	if _, err := f.clients.Create(f.ctxFor(t, admin), in); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
}

type mineAnswer struct {
	Clients      int64 `json:"clients"`
	ExpiringSoon int64 `json:"expiringSoon"`
	Expiring     []struct {
		Name string `json:"name"`
	} `json:"expiring"`
	Tunnels []struct {
		ID   uint   `json:"id"`
		Name string `json:"name"`
		Up   bool   `json:"up"`
	} `json:"tunnels"`
	Activity []activityView `json:"activity"`
}

func (f *mineFixture) ask(t *testing.T, admin *model.Admin) mineAnswer {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/overview/mine", nil).
		WithContext(f.ctxFor(t, admin))
	rec := httptest.NewRecorder()
	f.srv.handleOverviewMine(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s's dashboard: status %d, body %s", admin.Username, rec.Code, rec.Body)
	}
	var out mineAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestTheResellerDashboardIsTheCallersOwn(t *testing.T) {
	f := newMineFixture(t)

	got := f.ask(t, f.reza)
	if got.Clients != 1 || got.ExpiringSoon != 1 {
		t.Errorf("reza's dashboard counts %d customers, %d expiring; want their own 1 and 1",
			got.Clients, got.ExpiringSoon)
	}
	if len(got.Expiring) != 1 || got.Expiring[0].Name != "reza-expiring" {
		t.Errorf("reza's expiring list is %+v, want only reza-expiring", got.Expiring)
	}
	// Their one tunnel, up: the node behind it is answering and it is on.
	// Neither sara's tunnel nor the unassigned one is in the answer.
	if len(got.Tunnels) != 1 || got.Tunnels[0].ID != f.wgA.ID {
		t.Fatalf("reza's tunnels are %+v, want only their own wg-a", got.Tunnels)
	}
	if !got.Tunnels[0].Up {
		t.Error("reza's tunnel reads down, but its node is answering and it is enabled")
	}
	if len(got.Activity) != 1 || got.Activity[0].Actor != "reza" {
		t.Errorf("reza's activity is %+v, want only their own line", got.Activity)
	}

	got = f.ask(t, f.sara)
	if got.Clients != 1 || got.ExpiringSoon != 0 {
		t.Errorf("sara's dashboard counts %d customers, %d expiring; want her own 1 and 0",
			got.Clients, got.ExpiringSoon)
	}
	if len(got.Tunnels) != 1 || got.Tunnels[0].ID != f.wgB.ID {
		t.Errorf("sara's tunnels are %+v, want only her own wg-b", got.Tunnels)
	}
	if len(got.Activity) != 1 || got.Activity[0].Actor != "sara" {
		t.Errorf("sara's activity is %+v, want only her own line", got.Activity)
	}
}
