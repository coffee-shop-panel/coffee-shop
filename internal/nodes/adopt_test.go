package nodes

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
)

func TestOnlyNamesAPanelMadeAreLeftOutOfAnImport(t *testing.T) {
	for name, made := range map[string]bool{
		"wui-12":           true,
		"wui-a47ec8-12":    true,
		"wui_a47ec8_190":   true,
		"wuikeep_30296bfa": true,
		"wui_test1":        false,
		"wui-john":         false,
		"alice":            false,
		"wui-":             false,
	} {
		if got := madeByAPanel(name); got != made {
			t.Errorf("%q: made by a panel %v, want %v", name, got, made)
		}
	}
}

func TestALinkIsFoundByTheCredentialItCarries(t *testing.T) {
	id := "3f1c2b8e-0d4a-4e6b-9c1d-7a2e5f8b9c0d"
	// A vmess link is base64 JSON, which may have a '/' in it.
	vmess := "vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"v":"2","ps":"a/b??","add":"1.2.3.4","port":"443","id":"`+id+`","path":"/x/y"}`))
	ss := "ss://" + base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:"+"s3cr3t/p+w")) + "@1.2.3.4:8388#me"
	for _, c := range []struct {
		link, cred string
		want       bool
	}{
		{"vless://" + id + "@1.2.3.4:443?type=tcp#a", id, true},
		{vmess, id, true},
		{vmess, "3f1c2b8e-0000-4e6b-9c1d-7a2e5f8b9c0d", false},
		{ss, "s3cr3t/p+w", true},
		{"trojan://pass%2Fword@1.2.3.4:443#t", "pass/word", true},
		{"vless://" + id + "@1.2.3.4:443", "", false},
	} {
		if got := linkCarries(c.link, c.cred); got != c.want {
			t.Errorf("%s carries %q: %v, want %v", c.link, c.cred, got, c.want)
		}
	}
}

// A client on two inbounds (3x-ui 3.1 and later) is one customer to bring
// in, and both copies count: the import takes the larger quota and limit,
// the later expiry, and the traffic of both.
func TestAClientOnTwoInboundsImportsOnceWithBothCopiesCounted(t *testing.T) {
	f := newFakeXUI(t, "3.10.2")
	one := f.addInbound("vless-one", "vless", 31001, map[string]any{"decryption": "none"}, nil)
	two := f.addInbound("vless-two", "vless", 31002, map[string]any{"decryption": "none"}, nil)
	early := time.Now().Add(24 * time.Hour)
	late := time.Now().Add(48 * time.Hour)
	f.addOwnClient(one.ID, map[string]any{"id": "uuid-1", "email": "dup@example.com", "enable": true, "subId": "sub-1", "totalGB": 10, "expiryTime": early.UnixMilli(), "limitIp": 1})
	f.addOwnClient(two.ID, map[string]any{"id": "uuid-1", "email": "dup@example.com", "enable": false, "subId": "sub-1", "totalGB": 50, "expiryTime": late.UnixMilli(), "limitIp": 3})
	f.count(one.ID, "dup@example.com", 100, 200)
	f.count(two.ID, "dup@example.com", 300, 400)
	tunnels := []model.Interface{
		{ID: 1, XUI: model.JSON(model.XUIInbound{RemoteID: one.ID, Customers: true})},
		{ID: 2, XUI: model.JSON(model.XUIInbound{RemoteID: two.ID, Customers: true})},
	}

	got, err := xuiCandidates(context.Background(), func() model.Node {
		n := xuiNode(f, true, false)
		n.AllowPrivateAddress = true
		return n
	}(), tunnels)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(got), got)
	}
	cand := got[0]
	if cand.UsedBytes != 1000 {
		t.Errorf("used %d, want 1000 (both copies' traffic)", cand.UsedBytes)
	}
	if cand.QuotaBytes != 50 {
		t.Errorf("quota %d, want 50 (the larger copy's)", cand.QuotaBytes)
	}
	if cand.DeviceLimit != 3 {
		t.Errorf("device limit %d, want 3 (the larger copy's)", cand.DeviceLimit)
	}
	if !cand.Enabled {
		t.Error("disabled, want on (the first copy is on)")
	}
	if cand.ExpiresAt == nil || cand.ExpiresAt.Before(late.Add(-time.Second)) || cand.ExpiresAt.After(late.Add(time.Second)) {
		t.Errorf("expiry %v, want the later copy's %v", cand.ExpiresAt, late)
	}
	if len(cand.Inbounds) != 2 {
		t.Errorf("inbounds %v, want both", cand.Inbounds)
	}
	if len(cand.Files) != 1 {
		t.Errorf("files %d, want 1 (one customer, one file)", len(cand.Files))
	}
}
