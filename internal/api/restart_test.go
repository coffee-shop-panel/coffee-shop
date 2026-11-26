package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abolfazl/w-ui/internal/backend"
	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/ipam"
	"github.com/abolfazl/w-ui/internal/scope"
	"github.com/abolfazl/w-ui/internal/service"
)

// Restarting a tunnel reopens the driver on this machine, and only this
// machine: a tunnel another node runs has no driver here to reopen, and
// opening one would build a device nobody dials while reporting a restart
// that never reached the server that runs it.
func TestRestartingATunnelOnAnotherNodeOpensNothingHere(t *testing.T) {
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
	local := model.Node{Name: "local", Kind: model.KindLocal}
	remote := model.Node{Name: "remote", Kind: model.KindRemote}
	for _, n := range []*model.Node{&local, &remote} {
		if err := db.Create(n).Error; err != nil {
			t.Fatal(err)
		}
	}
	pools := ipam.NewPools()
	mkIface := func(name string, nodeID uint, subnet string, port int) model.Interface {
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
	away := mkIface("wg-away", remote.ID, "10.66.0.0/24", 51820)
	pool := backend.NewPool(log)
	s := &Server{
		log: log, ifaces: service.NewInterfaces(db, pools, log),
		pool: pool, localNodeID: local.ID,
	}

	req := httptest.NewRequest(http.MethodPost, "/api/interfaces/"+strconv.Itoa(int(away.ID))+"/restart", nil)
	req.SetPathValue("id", strconv.Itoa(int(away.ID)))
	rec := httptest.NewRecorder()
	s.handleRestartInterface(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 with the refusal in the body (%s)", rec.Code, rec.Body)
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.OK {
		t.Error("restarting a tunnel on another node was reported as done")
	}
	if !strings.Contains(out.Error, "another server") {
		t.Errorf("the refusal says %q, want it to say the tunnel runs on another server", out.Error)
	}
	if _, open := pool.Get(away.ID); open {
		t.Error("a driver was opened here for a tunnel another node runs")
	}
}

// Without a pool there is no driver to reopen: the request is refused,
// not a panic.
func TestRestartingATunnelWithoutAPoolIsRefused(t *testing.T) {
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
	node := model.Node{Name: "local", Kind: model.KindLocal}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	pools := ipam.NewPools()
	iface := model.Interface{
		Name: "wg0", Protocol: model.ProtocolWireGuard, Enabled: true,
		ListenPort: 51820, Subnet: "10.66.0.0/24", EndpointHost: "vpn.example.com", NodeID: node.ID,
	}
	if err := db.Create(&iface).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := pools.Add(iface.ID, iface.Subnet); err != nil {
		t.Fatal(err)
	}
	s := &Server{
		log: log, ifaces: service.NewInterfaces(db, pools, log),
		localNodeID: node.ID,
	}

	req := httptest.NewRequest(http.MethodPost, "/api/interfaces/"+strconv.Itoa(int(iface.ID))+"/restart", nil)
	req.SetPathValue("id", strconv.Itoa(int(iface.ID)))
	rec := httptest.NewRecorder()
	s.handleRestartInterface(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 with the refusal in the body (%s)", rec.Code, rec.Body)
	}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.OK {
		t.Error("a restart with no driver pool was reported as done")
	}
}
