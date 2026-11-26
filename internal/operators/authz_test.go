package operators

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/fault"
)

func strptr(s string) *string { return &s }

func authzDB(t *testing.T) *Admins {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Node{Name: "local", Kind: model.KindLocal}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 3; i++ {
		iface := &model.Interface{
			Name: fmt.Sprintf("wg%d", i), Protocol: model.ProtocolWireGuard,
			ListenPort: 51820 + i, Subnet: fmt.Sprintf("10.8.%d.0/24", i),
			EndpointHost: "vpn.example.com", NodeID: 1,
		}
		if err := db.Create(iface).Error; err != nil {
			t.Fatal(err)
		}
	}
	return NewAdmins(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func mkAdmin(t *testing.T, s *Admins, name string, quota uint64, ifaces []uint) *model.Admin {
	t.Helper()
	on := true
	a, err := s.Create(context.Background(), AdminInput{
		Username: name, Password: "correct-horse", Role: model.RoleAdmin,
		Enabled: &on, QuotaBytes: &quota, InterfaceIDs: ifaces,
	})
	if err != nil {
		t.Fatalf("owner create admin %s: %v", name, err)
	}
	return a
}

const gb = uint64(1) << 30

// A capped admin cannot hand a reseller unlimited traffic: zero is no
// limit, not nothing, and an omitted quota stores zero.
func TestACappedAdminCannotHandDownUnlimitedTraffic(t *testing.T) {
	s := authzDB(t)
	ali := mkAdmin(t, s, "ali", 100*gb, []uint{1, 2})
	asAli := WithCaller(context.Background(), ali)

	if _, err := s.Create(asAli, AdminInput{Username: "res-free", Password: "correct-horse",
		Role: model.RoleReseller, InterfaceIDs: []uint{1}}); !errors.Is(err, fault.ErrInvalid) {
		t.Errorf("a reseller with no quota at all was created under a capped admin: %v", err)
	}
	zero := uint64(0)
	if _, err := s.Create(asAli, AdminInput{Username: "res-zero", Password: "correct-horse",
		Role: model.RoleReseller, QuotaBytes: &zero, InterfaceIDs: []uint{1}}); !errors.Is(err, fault.ErrInvalid) {
		t.Errorf("a reseller with zero quota was created under a capped admin: %v", err)
	}
	// Within the allowance still works.
	q := 30 * gb
	if _, err := s.Create(asAli, AdminInput{Username: "res-ok", Password: "correct-horse",
		Role: model.RoleReseller, QuotaBytes: &q, InterfaceIDs: []uint{1}}); err != nil {
		t.Errorf("a reseller within the allowance was refused: %v", err)
	}
}

// An admin's allowance is a pool, not a per-reseller ceiling: traffic
// promised to one reseller is gone for the next, or the same allowance
// could be sold twice over and every reseller past the first left with a
// quota the admin can never honour.
func TestAnAdminCannotHandDownTheSameTrafficTwice(t *testing.T) {
	s := authzDB(t)
	ali := mkAdmin(t, s, "ali", 100*gb, []uint{1, 2})
	asAli := WithCaller(context.Background(), ali)

	// The whole allowance to one reseller leaves nothing for the next.
	whole := 100 * gb
	if _, err := s.Create(asAli, AdminInput{Username: "res1", Password: "correct-horse",
		Role: model.RoleReseller, QuotaBytes: &whole, InterfaceIDs: []uint{1}}); err != nil {
		t.Fatalf("first reseller: %v", err)
	}
	one := gb
	if _, err := s.Create(asAli, AdminInput{Username: "res2", Password: "correct-horse",
		Role: model.RoleReseller, QuotaBytes: &one, InterfaceIDs: []uint{1}}); !errors.Is(err, fault.ErrInvalid) {
		t.Errorf("a second reseller was promised traffic already promised: %v", err)
	}

	// Split across two, the second up to exactly what is left.
	beh := mkAdmin(t, s, "beh", 100*gb, []uint{1, 2})
	asBeh := WithCaller(context.Background(), beh)
	sixty := 60 * gb
	if _, err := s.Create(asBeh, AdminInput{Username: "beh1", Password: "correct-horse",
		Role: model.RoleReseller, QuotaBytes: &sixty, InterfaceIDs: []uint{1}}); err != nil {
		t.Fatalf("first reseller under beh: %v", err)
	}
	fortyOne := 41 * gb
	if _, err := s.Create(asBeh, AdminInput{Username: "beh2", Password: "correct-horse",
		Role: model.RoleReseller, QuotaBytes: &fortyOne, InterfaceIDs: []uint{1}}); !errors.Is(err, fault.ErrInvalid) {
		t.Errorf("a reseller was promised more than the allowance less the first: %v", err)
	}
	forty := 40 * gb
	if _, err := s.Create(asBeh, AdminInput{Username: "beh2", Password: "correct-horse",
		Role: model.RoleReseller, QuotaBytes: &forty, InterfaceIDs: []uint{1}}); err != nil {
		t.Errorf("a reseller within what was left was refused: %v", err)
	}

	// Raising a reseller who has spent some of their quota spends only
	// the new headroom, not the whole quota again.
	cid := mkAdmin(t, s, "cid", 100*gb, []uint{1, 2})
	r1, err := s.Create(WithCaller(context.Background(), cid), AdminInput{Username: "cid1",
		Password: "correct-horse", Role: model.RoleReseller, QuotaBytes: &sixty, InterfaceIDs: []uint{1}})
	if err != nil {
		t.Fatalf("first reseller under cid: %v", err)
	}
	// Half of it spent -- charged to the reseller and to the admin above
	// them, as the traffic flush does.
	if err := s.db.Model(&model.Admin{}).Where("id IN ?", []uint{cid.ID, r1.ID}).
		Update("used_bytes", 50*gb).Error; err != nil {
		t.Fatal(err)
	}
	cid, err = s.Get(context.Background(), cid.ID)
	if err != nil {
		t.Fatal(err)
	}
	asCid := WithCaller(context.Background(), cid)
	// 60 -> 80 leaves 30 of headroom against 50 still unspent: allowed.
	eighty := 80 * gb
	if _, err := s.Update(asCid, r1.ID, AdminInput{QuotaBytes: &eighty}); err != nil {
		t.Errorf("raising a reseller within the headroom was refused: %v", err)
	}
	// 80 -> 100 leaves 50, exactly the allowance left: allowed.
	hundred := 100 * gb
	if _, err := s.Update(asCid, r1.ID, AdminInput{QuotaBytes: &hundred}); err != nil {
		t.Errorf("raising a reseller to exactly the allowance left was refused: %v", err)
	}
	// Past it: refused.
	over := 101 * gb
	if _, err := s.Update(asCid, r1.ID, AdminInput{QuotaBytes: &over}); !errors.Is(err, fault.ErrInvalid) {
		t.Errorf("raising a reseller past the allowance left was allowed: %v", err)
	}
}

// Demoting an admin to reseller leaves them a reseller, and a reseller's
// customers have to be filed under something: the label is made here, as
// on creation, rather than leaving the reseller with none -- which would
// file their new customers under no label at all.
func TestDemotingAnAdminToResellerFilesTheirCustomers(t *testing.T) {
	s := authzDB(t)
	a := mkAdmin(t, s, "dem", 0, nil)
	if a.GroupName != "" {
		t.Fatalf("an admin has a label %q", a.GroupName)
	}
	got, err := s.Update(context.Background(), a.ID, AdminInput{Role: model.RoleReseller})
	if err != nil {
		t.Fatalf("demote: %v", err)
	}
	if got.GroupName != "reseller:dem" {
		t.Fatalf("demoted reseller's label is %q, want the default", got.GroupName)
	}
	var g model.Group
	if err := s.db.Where("owner_id = 0 AND name = ?", "reseller:dem").First(&g).Error; err != nil {
		t.Fatalf("the default label was not created: %v", err)
	}

	// A name given with the demotion is used instead.
	b := mkAdmin(t, s, "dem2", 0, nil)
	got, err = s.Update(context.Background(), b.ID, AdminInput{Role: model.RoleReseller, GroupName: strptr("vip-corner")})
	if err != nil {
		t.Fatalf("demote with a name: %v", err)
	}
	if got.GroupName != "vip-corner" {
		t.Fatalf("demoted reseller's label is %q, want vip-corner", got.GroupName)
	}
}

// An admin creates resellers only, under themselves, within their own
// ceiling, and never reaches anyone else's operators.
func TestAnAdminManagesOnlyTheirOwnResellers(t *testing.T) {
	s := authzDB(t)
	ali := mkAdmin(t, s, "ali", 100*gb, []uint{1, 2})
	beh := mkAdmin(t, s, "beh", 100*gb, []uint{1, 3})

	asAli := WithCaller(context.Background(), ali)
	q := 30 * gb

	// A reseller under ali: forced to the reseller role and to ali as parent.
	r1, err := s.Create(asAli, AdminInput{Username: "res1", Password: "correct-horse",
		Role: model.RoleReseller, QuotaBytes: &q, InterfaceIDs: []uint{1}})
	if err != nil {
		t.Fatalf("ali create reseller: %v", err)
	}
	if r1.Role != model.RoleReseller || r1.ParentAdminID != ali.ID {
		t.Fatalf("ali's reseller is role %q parent %d, want reseller under %d", r1.Role, r1.ParentAdminID, ali.ID)
	}

	// An admin cannot mint an admin.
	if _, err := s.Create(asAli, AdminInput{Username: "xadm", Password: "correct-horse", Role: model.RoleAdmin}); !errors.Is(err, fault.ErrInvalid) {
		t.Fatalf("ali created an admin: %v", err)
	}
	// Nor hand down a tunnel that is not theirs...
	if _, err := s.Create(asAli, AdminInput{Username: "ytun", Password: "correct-horse",
		Role: model.RoleReseller, InterfaceIDs: []uint{3}}); !errors.Is(err, fault.ErrInvalid) {
		t.Fatalf("ali handed down an unowned tunnel: %v", err)
	}
	// ...nor more traffic than they have left.
	tooMuch := 200 * gb
	if _, err := s.Create(asAli, AdminInput{Username: "zquota", Password: "correct-horse",
		Role: model.RoleReseller, QuotaBytes: &tooMuch, InterfaceIDs: []uint{1}}); !errors.Is(err, fault.ErrInvalid) {
		t.Fatalf("ali handed down more than their allowance: %v", err)
	}

	// beh's reseller, to prove isolation.
	bq := 10 * gb
	b1, err := s.Create(WithCaller(context.Background(), beh), AdminInput{Username: "beh1",
		Password: "correct-horse", Role: model.RoleReseller, QuotaBytes: &bq, InterfaceIDs: []uint{1}})
	if err != nil {
		t.Fatal(err)
	}

	// List, as ali, is only ali's own reseller -- no admin, no owner, no beh.
	list, err := s.List(asAli)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != r1.ID {
		t.Fatalf("ali's operator list is %+v, want only r1", list)
	}

	// ali cannot reach beh's reseller, nor beh, nor themselves, by id.
	for _, id := range []uint{b1.ID, beh.ID, ali.ID} {
		if _, err := s.Get(asAli, id); !errors.Is(err, fault.ErrNotFound) {
			t.Errorf("ali read operator %d: %v", id, err)
		}
		if _, err := s.Update(asAli, id, AdminInput{Note: strptr("nope")}); !errors.Is(err, fault.ErrNotFound) {
			t.Errorf("ali changed operator %d: %v", id, err)
		}
		if err := s.Delete(asAli, id, DeleteKeepClients, nil); !errors.Is(err, fault.ErrNotFound) {
			t.Errorf("ali deleted operator %d: %v", id, err)
		}
	}

	// ali may change their own reseller, but not past their allowance.
	if _, err := s.Update(asAli, r1.ID, AdminInput{Note: strptr("fine")}); err != nil {
		t.Errorf("ali could not change their own reseller: %v", err)
	}
	if _, err := s.Update(asAli, r1.ID, AdminInput{QuotaBytes: &tooMuch}); !errors.Is(err, fault.ErrInvalid) {
		t.Errorf("ali raised their reseller past their own allowance: %v", err)
	}
}

// An update that says nothing about the note leaves it alone; only a
// note actually sent changes it, and an empty one clears it. A plain
// string could not tell "not sent" from "sent empty", and an update that
// changed anything else wiped the note.
func TestAnUpdateThatOmitsTheNoteKeepsIt(t *testing.T) {
	s := authzDB(t)
	a, err := s.Create(context.Background(), AdminInput{
		Username: "noted", Password: "correct-horse", Role: model.RoleReseller,
		Note: strptr("met at the shop"),
	})
	if err != nil {
		t.Fatal(err)
	}
	on := true
	if _, err := s.Update(context.Background(), a.ID, AdminInput{Enabled: &on}); err != nil {
		t.Fatalf("update without a note: %v", err)
	}
	got, err := s.Get(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Note != "met at the shop" {
		t.Errorf("note = %q, want it kept", got.Note)
	}
	if _, err := s.Update(context.Background(), a.ID, AdminInput{Note: strptr("")}); err != nil {
		t.Fatalf("update with an empty note: %v", err)
	}
	got, err = s.Get(context.Background(), a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Note != "" {
		t.Errorf("note = %q, want it cleared", got.Note)
	}
}

// The owner grants an admin pages with a checkbox each: the default is every
// page, a list is taken as given, and the backend can say which an admin has.
func TestAnAdminsMenuGrants(t *testing.T) {
	s := authzDB(t)
	on := true

	// No list on create: the admin gets every grantable page.
	full, err := s.Create(context.Background(), AdminInput{Username: "full",
		Password: "correct-horse", Role: model.RoleAdmin, Enabled: &on})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.MenuIDs(context.Background(), full.ID); len(got) != len(model.GrantableMenus()) {
		t.Fatalf("a default admin has pages %v, want all", got)
	}

	// A list is taken as given.
	some, err := s.Create(context.Background(), AdminInput{Username: "some",
		Password: "correct-horse", Role: model.RoleAdmin, Enabled: &on,
		MenuIDs: []string{model.MenuGroups}})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.HasMenu(context.Background(), some.ID, model.MenuGroups); !ok {
		t.Error("the admin was not granted the groups page they were given")
	}
	if ok, _ := s.HasMenu(context.Background(), some.ID, model.MenuClients); ok {
		t.Error("the admin has the clients page they were never given")
	}

	// An unknown page is refused, not quietly dropped.
	if _, err := s.Create(context.Background(), AdminInput{Username: "bad",
		Password: "correct-horse", Role: model.RoleAdmin, Enabled: &on,
		MenuIDs: []string{"routing"}}); !errors.Is(err, fault.ErrInvalid) {
		t.Errorf("a bad page was accepted: %v", err)
	}

	// An update with an empty list is the owner unchecking every page.
	if _, err := s.Update(context.Background(), some.ID, AdminInput{MenuIDs: []string{}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.MenuIDs(context.Background(), some.ID); len(got) != 0 {
		t.Errorf("after unchecking all, the admin still has %v", got)
	}
}
