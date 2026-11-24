package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/fleet"
	"github.com/abolfazl/w-ui/internal/ipam"
	"github.com/abolfazl/w-ui/internal/scope"
	"github.com/abolfazl/w-ui/internal/service"
)

// clientOpsServer is a server with the customer reads wired, over a real
// schema: the diagnostics handler joins three services, and a test over
// stubs would prove the join against whatever the stubs felt like saying.
func clientOpsServer(t *testing.T) (*Server, *gorm.DB, *service.Clients, []model.Interface) {
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
	if err := db.Create(&model.Node{Name: "local", Kind: model.KindLocal}).Error; err != nil {
		t.Fatal(err)
	}
	pools := ipam.NewPools()
	ifaces := []model.Interface{
		{Name: "wg0", Protocol: model.ProtocolWireGuard, Enabled: true, ListenPort: 51820,
			Subnet: "10.66.0.0/24", EndpointHost: "vpn1.example.com", NodeID: 1},
		{Name: "wg1", Protocol: model.ProtocolWireGuard, Enabled: true, ListenPort: 51821,
			Subnet: "10.67.0.0/24", EndpointHost: "vpn2.example.com", NodeID: 1},
	}
	for i := range ifaces {
		if err := db.Create(&ifaces[i]).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := pools.Add(ifaces[i].ID, ifaces[i].Subnet); err != nil {
			t.Fatal(err)
		}
	}
	clients := service.NewClients(db, pools, log)
	s := &Server{
		log:     log,
		clients: clients,
		ifaces:  service.NewInterfaces(db, pools, log),
		nodes:   fleet.NewNodes(db, log),
	}
	return s, db, clients, ifaces
}

func callHandler(t *testing.T, h http.HandlerFunc, method, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/", rdr)
	if id != "" {
		req.SetPathValue("id", id)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// The diagnosis carries the plan and, per device, the server it sits on and
// whether that server is answering -- the facts a support answer is made of.
func TestClientDiagnosticsShape(t *testing.T) {
	s, db, clients, ifaces := clientOpsServer(t)

	expires := time.Now().Add(48 * time.Hour).UTC()
	client, err := clients.Create(context.Background(), service.CreateInput{
		Name: "Roya", InterfaceIDs: []uint{ifaces[0].ID}, QuotaBytes: 10 << 30,
		ExpiresAt: &expires, DeviceLimit: 1,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// A second device record, on a 3x-ui inbound whose panel has not made
	// its link yet and whose node is not answering: the two facts about it
	// the diagnosis exists to say.
	xui := &model.Node{Name: "sanaei", Kind: model.KindXUI}
	if err := db.Create(xui).Error; err != nil {
		t.Fatal(err)
	}
	inbound := &model.Interface{Name: "vless-in", Protocol: model.ProtocolXray, Enabled: true,
		ListenPort: 443, Subnet: "10.68.0.0/24", EndpointHost: "cdn.example.com", NodeID: xui.ID}
	if err := db.Create(inbound).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Account{
		ClientID: client.ID, InterfaceID: inbound.ID, NodeID: xui.ID,
		DeviceName: "device-1", IP: "10.68.0.2", Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	rec := callHandler(t, s.handleClientDiagnostics, http.MethodGet, strconv.Itoa(int(client.ID)), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var out struct {
		Client struct {
			Name        string  `json:"name"`
			Status      string  `json:"status"`
			QuotaBytes  uint64  `json:"quotaBytes"`
			UsedBytes   uint64  `json:"usedBytes"`
			ExpiresAt   *string `json:"expiresAt"`
			DeviceLimit int     `json:"deviceLimit"`
			HasSubToken bool    `json:"hasSubToken"`
		} `json:"client"`
		Accounts []struct {
			DeviceName    string  `json:"deviceName"`
			InterfaceName string  `json:"interfaceName"`
			Protocol      string  `json:"protocol"`
			NodeName      string  `json:"nodeName"`
			NodeUp        bool    `json:"nodeUp"`
			Address       string  `json:"address"`
			LastHandshake *string `json:"lastHandshake"`
			HasLink       bool    `json:"hasLink"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body)
	}
	if out.Client.Name != "Roya" || out.Client.Status != "active" ||
		out.Client.QuotaBytes != 10<<30 || out.Client.DeviceLimit != 1 {
		t.Errorf("client half is %+v", out.Client)
	}
	if out.Client.ExpiresAt == nil {
		t.Error("expiresAt is null for a plan with a date")
	}
	if !out.Client.HasSubToken {
		t.Error("hasSubToken is false for a customer who was issued a link")
	}
	if len(out.Accounts) != 2 {
		t.Fatalf("accounts %d, want 2", len(out.Accounts))
	}
	byIface := map[string]int{}
	for i, a := range out.Accounts {
		byIface[a.InterfaceName] = i
	}
	wgIdx, ok := byIface["wg0"]
	if !ok {
		t.Fatalf("no account on wg0 in %+v", out.Accounts)
	}
	wg := out.Accounts[wgIdx]
	if wg.NodeName != "local" || !wg.NodeUp {
		t.Errorf("the local device reads node %q up=%v, want local and up", wg.NodeName, wg.NodeUp)
	}
	if wg.Protocol != "wireguard" || !wg.HasLink || wg.Address == "" {
		t.Errorf("the WireGuard device reads %+v", wg)
	}
	if wg.LastHandshake != nil {
		t.Errorf("lastHandshake %q for a device that has never connected", *wg.LastHandshake)
	}
	xr := out.Accounts[byIface["vless-in"]]
	if xr.Protocol != "xray" || xr.HasLink {
		t.Errorf("the 3x-ui device reads protocol %q hasLink %v, want xray and no link yet", xr.Protocol, xr.HasLink)
	}
	if xr.NodeName != "sanaei" || xr.NodeUp {
		t.Errorf("the 3x-ui device reads node %q up=%v, want sanaei and down", xr.NodeName, xr.NodeUp)
	}

	// A customer who is not there is a 404, not an empty diagnosis.
	rec = callHandler(t, s.handleClientDiagnostics, http.MethodGet, "9999", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing customer: status %d, want 404", rec.Code)
	}
}

// The move handler moves and reports the counts; a move onto the same
// server is the caller's mistake and is answered as one.
func TestMoveInterfaceClientsHandler(t *testing.T) {
	s, _, clients, ifaces := clientOpsServer(t)

	expires := time.Now().Add(48 * time.Hour).UTC()
	client, err := clients.Create(context.Background(), service.CreateInput{
		Name: "Sina", InterfaceIDs: []uint{ifaces[0].ID}, QuotaBytes: 10 << 30,
		ExpiresAt: &expires, DeviceLimit: 1,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	body := `{"toInterfaceId": ` + strconv.Itoa(int(ifaces[1].ID)) + `}`
	rec := callHandler(t, s.handleMoveInterfaceClients, http.MethodPost,
		strconv.Itoa(int(ifaces[0].ID)), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body)
	}
	var out struct {
		Moved  int `json:"moved"`
		Failed int `json:"failed"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Moved != 1 || out.Failed != 0 {
		t.Errorf("moved %d failed %d, want 1 and 0", out.Moved, out.Failed)
	}
	got, err := clients.Get(context.Background(), client.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Accounts) != 1 || got.Accounts[0].InterfaceID != ifaces[1].ID {
		t.Errorf("the customer is on %+v, want the new server alone", got.Accounts)
	}

	rec = callHandler(t, s.handleMoveInterfaceClients, http.MethodPost,
		strconv.Itoa(int(ifaces[1].ID)), body)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("moving a server onto itself: status %d, want 400 (body %s)", rec.Code, rec.Body)
	}
}
