package service

import (
	"context"
	"testing"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// Expiring soon is a plan about to end, not a status: an active customer
// whose date falls inside the window, including one already past it whose
// row has not been swept yet. Anybody else in the list is a renewal offered
// to the wrong person.
func TestExpiringSoonBucket(t *testing.T) {
	db := testDB(t)
	svc := NewClients(db, nil, quietLog())

	now := time.Now().UTC()
	inTwoDays := now.Add(48 * time.Hour)
	yesterday := now.Add(-24 * time.Hour)
	inTenDays := now.Add(10 * 24 * time.Hour)

	seed := []model.Client{
		{Name: "ends-in-two-days", Protocol: model.ProtocolWireGuard, Status: model.StatusActive,
			ExpiresAt: &inTwoDays},
		// Past the date and still marked active: the sweep has not run, and
		// this is the customer who most needs the renewal offer.
		{Name: "past-but-still-active", Protocol: model.ProtocolWireGuard, Status: model.StatusActive,
			ExpiresAt: &yesterday},
		{Name: "ends-in-ten-days", Protocol: model.ProtocolWireGuard, Status: model.StatusActive,
			ExpiresAt: &inTenDays},
		{Name: "never-ends", Protocol: model.ProtocolWireGuard, Status: model.StatusActive},
		{Name: "off-and-ending", Protocol: model.ProtocolWireGuard, Status: model.StatusDisabled,
			ExpiresAt: &inTwoDays},
		{Name: "already-expired", Protocol: model.ProtocolWireGuard, Status: model.StatusExpired,
			ExpiresAt: &yesterday},
	}
	for i := range seed {
		if err := db.Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed %s: %v", seed[i].Name, err)
		}
	}

	page, err := svc.List(context.Background(), ListFilter{Buckets: []string{"expiring"}, PerPage: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[string]bool{}
	for _, c := range page.Items {
		got[c.Name] = true
	}
	for _, want := range []string{"ends-in-two-days", "past-but-still-active"} {
		if !got[want] {
			t.Errorf("%s missing from the expiring list (%v)", want, got)
		}
	}
	if len(got) != 2 {
		t.Errorf("expiring list holds %v, want exactly the two ending soon", got)
	}
}

// Emptying a server moves everybody on it: their devices keep their names,
// their link keeps working, and only the server changes.
func TestMoveInterfaceMovesEveryCustomer(t *testing.T) {
	db := testDB(t)
	svc, ifaces, made := seedServers(t, db, 3)
	from, to := ifaces[0].ID, ifaces[1].ID

	tokens := map[uint]string{}
	for _, c := range made {
		tokens[c.ID] = c.SubToken
	}

	moved, failed, err := svc.MoveInterface(context.Background(), from, to)
	if err != nil {
		t.Fatalf("MoveInterface: %v", err)
	}
	if moved != 3 || failed != 0 {
		t.Fatalf("moved %d, failed %d; want 3 and 0", moved, failed)
	}

	for _, c := range made {
		got, err := svc.Get(context.Background(), c.ID)
		if err != nil {
			t.Fatalf("reload %s: %v", c.Name, err)
		}
		if servers := clientInterfaces(got.Accounts); len(servers) != 1 || servers[0] != to {
			t.Errorf("%s is on %v, want only the new server", c.Name, servers)
		}
		if names := deviceNames(got.Accounts); len(names) != 2 {
			t.Errorf("%s holds devices %v, want the two it was sold", c.Name, names)
		}
		if got.SubToken != tokens[c.ID] {
			t.Errorf("%s has a new subscription link; a move must not change it", c.Name)
		}
	}
}

// The refusals are about the pair of servers, and are made before anybody
// is moved: a move refused halfway would be worse than one refused whole.
func TestMoveInterfaceRefusals(t *testing.T) {
	db := testDB(t)
	svc, ifaces, made := seedServers(t, db, 1)
	from, to := ifaces[0].ID, ifaces[1].ID
	ctx := context.Background()

	if _, _, err := svc.MoveInterface(ctx, from, from); err == nil {
		t.Error("moving a server onto itself was allowed")
	}
	if _, _, err := svc.MoveInterface(ctx, from, 9999); err == nil {
		t.Error("moving onto a server that does not exist was allowed")
	}

	// A tunnel of another protocol has its accounts somewhere else -- an
	// OpenVPN authority, a 3x-ui panel -- and there is nothing here to move.
	ovpn := &model.Interface{Name: "ovpn0", Protocol: model.ProtocolOpenVPN, Enabled: true,
		ListenPort: 1194, Subnet: "10.68.0.0/24", EndpointHost: "vpn3.example.com", NodeID: 1}
	if err := db.Create(ovpn).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.MoveInterface(ctx, from, ovpn.ID); err == nil {
		t.Error("moving onto an OpenVPN tunnel was allowed")
	}
	if _, _, err := svc.MoveInterface(ctx, ovpn.ID, to); err == nil {
		t.Error("moving off an OpenVPN tunnel was allowed")
	}

	// A switched-off target would take the customers nowhere: their files
	// would point at a server that serves nobody.
	if err := db.Model(&model.Interface{}).Where("id = ?", to).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.MoveInterface(ctx, from, to); err == nil {
		t.Error("moving onto a switched-off server was allowed")
	}

	// Nobody went anywhere through any of that.
	if got := serversOf(t, db, made[0].ID); len(got) != 1 || got[0] != from {
		t.Errorf("the customer is on %v after the refusals, want the old server alone", got)
	}
}

// A reseller emptying a server moves their own customers and nobody else's:
// the list of who is on the server is read through the same narrowing as
// every other customer read.
func TestMoveInterfaceMovesOnlyTheCallersOwnCustomers(t *testing.T) {
	db, svc, admins := resellerDB(t)
	reza := newReseller(t, admins, "reza", 100)
	ctx := asReseller(reza, 1, 2)
	// The seeded tunnels are made without the switch set; say it outright,
	// since a move onto a switched-off server is refused.
	if err := db.Model(&model.Interface{}).Where("id = ?", 2).Update("enabled", true).Error; err != nil {
		t.Fatal(err)
	}

	expires := time.Now().Add(48 * time.Hour).UTC()
	own, err := svc.Create(ctx, CreateInput{
		Name: "rezas", InterfaceIDs: []uint{1}, QuotaBytes: 10 << 30,
		ExpiresAt: &expires, DeviceLimit: 1,
	})
	if err != nil {
		t.Fatalf("create the reseller's customer: %v", err)
	}
	other, err := svc.Create(context.Background(), CreateInput{
		Name: "the owners", InterfaceIDs: []uint{1}, QuotaBytes: 10 << 30,
		ExpiresAt: &expires, DeviceLimit: 1,
	})
	if err != nil {
		t.Fatalf("create the owner's customer: %v", err)
	}

	moved, failed, err := svc.MoveInterface(ctx, 1, 2)
	if err != nil {
		t.Fatalf("MoveInterface: %v", err)
	}
	if moved != 1 || failed != 0 {
		t.Fatalf("moved %d, failed %d; want the reseller's one customer only", moved, failed)
	}
	if got := serversOf(t, db, own.ID); len(got) != 1 || got[0] != 2 {
		t.Errorf("the reseller's customer is on %v, want the new server", got)
	}
	if got := serversOf(t, db, other.ID); len(got) != 1 || got[0] != 1 {
		t.Errorf("the owner's customer is on %v, want untouched on the old server", got)
	}
}
