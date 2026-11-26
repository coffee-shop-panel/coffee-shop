package nodes

import (
	"context"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/service"
)

func TestWithEgress(t *testing.T) {
	var cfg map[string]any
	_ = json.Unmarshal([]byte(`{"log":{"loglevel":"warning"},
		"outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"blocked","protocol":"blackhole"},
			{"tag":"wui-out-0b0b0b","protocol":"vless"}],
		"routing":{"domainStrategy":"AsIs","rules":[
			{"type":"field","inboundTag":["api"],"outboundTag":"api"},
			{"type":"field","user":["regexp:^wui-0b0b0b-[0-9]+$"],"outboundTag":"wui-out-0b0b0b"},
			{"type":"field","ip":["geoip:private"],"outboundTag":"blocked"}]}}`), &cfg)
	route := egressRoute("a1b2c3", []string{"zed", "alice"})
	egress := map[string]any{"protocol": "vless"}

	with := withEgress(cfg, egress, route)
	rules := with["routing"].(map[string]any)["rules"].([]any)
	if len(rules) != 4 || rules[1].(map[string]any)["outboundTag"] != route.tag {
		t.Fatalf("rules %v", rules)
	}
	if rules[2].(map[string]any)["outboundTag"] != "wui-out-0b0b0b" || rules[3].(map[string]any)["outboundTag"] != "blocked" {
		t.Fatal("another panel's rule, or the operator's own, was lost")
	}
	if u := rules[1].(map[string]any)["user"].([]any); len(u) != 3 || u[1] != "alice" || u[2] != "zed" {
		t.Fatalf("the rule matches %v; want the pattern, then the users brought in, in order", u)
	}
	obs := with["outbounds"].([]any)
	if len(obs) != 4 || obs[0].(map[string]any)["tag"] != "direct" || obs[3].(map[string]any)["tag"] != route.tag {
		t.Fatalf("outbounds %v: the first, the panel's own default, must stay first", obs)
	}
	if with["log"] == nil || with["routing"].(map[string]any)["domainStrategy"] != "AsIs" {
		t.Fatal("the rest of the config was not kept")
	}

	// Again: the same, not a second copy.
	if again := withEgress(with, egress, route); !reflect.DeepEqual(normalJSON(again), normalJSON(with)) {
		t.Fatal("applying twice changed it")
	}
	// Direct: ours goes, the rest stays exactly as it was.
	if back := withEgress(with, nil, route); !reflect.DeepEqual(normalJSON(back), normalJSON(cfg)) {
		t.Fatalf("taking it away left %v", normalJSON(back))
	}
}

// The rule is what sends customers out, so it is tested against the names
// clients are really given, not against itself: this panel's match, another
// panel's and the operator's own do not.
func TestTheEgressRuleMatchesThisPanelsClientsAndNoOneElse(t *testing.T) {
	route := egressRoute("a1b2c3", nil)
	pattern := regexp.MustCompile(strings.TrimPrefix(route.users[0], "regexp:"))
	acc := &model.Account{ID: 42}
	for name, want := range map[string]bool{
		service.XUIEmail("a1b2c3", acc): true,
		service.XUIEmail("ffffff", acc): false, // another W-UI panel's
		"wui-42":                        false, // before panels had tags
		"alice":                         false,
		"wui-a1b2c3-42x":                false,
	} {
		if got := pattern.MatchString(name); got != want {
			t.Errorf("%q matched %v, want %v", name, got, want)
		}
	}
}

// The outbound at the top is OpenVPN: xray cannot carry it, so the node's
// customers leave from the node itself, and that is the intended state —
// not an error the round fails on forever.
func TestEgressForOpenVPNLeavesDirectly(t *testing.T) {
	db := testDB(t)
	if err := db.Create(&model.Outbound{Tag: "hop", Kind: model.OutboundOpenVPN, Position: 0}).Error; err != nil {
		t.Fatal(err)
	}
	egress, tag, err := egressFor(context.Background(), db)
	if err != nil {
		t.Fatalf("egressFor: %v", err)
	}
	if egress != nil || tag != "hop" {
		t.Fatalf("egressFor = %v, %q, want nothing to put there under hop", egress, tag)
	}
}

// The outbound an older panel left, untagged, is taken away with ours.
func TestTheUntaggedOutboundIsTakenAway(t *testing.T) {
	cfg := map[string]any{
		"outbounds": []any{map[string]any{"tag": "direct"}, map[string]any{"tag": xuiEgressLegacyTag}},
		"routing":   map[string]any{"rules": []any{map[string]any{"user": []any{"regexp:^wui-[0-9]+$"}, "outboundTag": xuiEgressLegacyTag}}},
	}
	got := normalJSON(withEgress(cfg, nil, egressRoute("a1b2c3", nil))).(map[string]any)
	if obs := got["outbounds"].([]any); len(obs) != 1 {
		t.Fatalf("outbounds %v", obs)
	}
	if rules, _ := got["routing"].(map[string]any)["rules"].([]any); len(rules) != 0 {
		t.Fatalf("rules %v", rules)
	}
}

// A node whose xray will not run with this panel's outbound, and whose
// settings then will not go back either, is said: before, the round
// reported the settings put back as they were while they were not.
func TestAXrayThatWillNotRunAndWillNotGoBackIsSaid(t *testing.T) {
	defer func(d time.Duration) { xuiSettle = d }(xuiSettle)
	xuiSettle = time.Millisecond
	f := newFakeXUI(t, "3.10.2")
	f.addInbound("vless-in", "vless", 31005, map[string]any{"decryption": "none"}, nil)
	f.xrayFails = func(map[string]any) bool { return true }
	f.xrayUpdateFailsFrom = 2 // the save goes in; the put-back does not
	r := newRig(t, xuiNode(f, true, false))
	hop := model.Outbound{Tag: "de-hop", Kind: model.OutboundVLESS, Enabled: true, Position: -1,
		Config: `{"protocol":"vless","settings":{"vnext":[{"address":"de.example","port":443,"users":[{"id":"x"}]}]}}`}
	if err := r.db.Create(&hop).Error; err != nil {
		t.Fatal(err)
	}
	api, err := newXUIAPI(r.ctx, r.node)
	if err != nil {
		t.Fatal(err)
	}
	err = r.s.xuiEgress(r.ctx, api, r.node, egressRoute(r.tag(), nil))
	if err == nil || !strings.Contains(err.Error(), "could not be put back") {
		t.Fatalf("xuiEgress said %v", err)
	}
}
