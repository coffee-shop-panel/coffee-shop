package api

import (
	"testing"
	"time"

	"github.com/abolfazl/w-ui/internal/service"
)

// The account page reports the plan's own device limit, not "unlimited",
// and counts the devices that have actually been used — not every file the
// plan holds. A device listed once per host variant counts once.
func TestCoffeeInfoReportsTheLimitAndOnlyDevicesInUse(t *testing.T) {
	now := time.Now()
	page := &service.SubPage{
		Name:        "ali",
		Status:      "active",
		QuotaBytes:  100,
		UsedBytes:   10,
		DeviceLimit: 2,
		Devices: []service.SubPageDevice{
			{ID: 1, Protocol: "wireguard", LastHandshake: &now},
			{ID: 2, Protocol: "xray", UsedBytes: 5},
			{ID: 2, Protocol: "xray", UsedBytes: 5},
			{ID: 3, Protocol: "wireguard"},
		},
	}
	info := coffeeInfo(service.SubSettings{}, page, nil)
	if got := info["device_limit"]; got != 2 {
		t.Errorf("device_limit = %v, want 2", got)
	}
	if got := info["devices_active"]; got != 2 {
		t.Errorf("devices_active = %v, want 2", got)
	}
}

// A seller's customer is sent to the seller: their contact stands where
// the panel's support address stood -- a Telegram handle made into its
// link -- and their name comes with it. An owner's customer, and a
// seller's whose seller set no contact, get the panel's address and no
// seller at all: the answer has no seller_name key, so the page draws
// no line for one.
func TestCoffeeInfoSendsASellersCustomerToTheSeller(t *testing.T) {
	cfg := service.SubSettings{SupportURL: "https://t.me/panel_support"}
	page := &service.SubPage{
		Name: "theirs", Status: "active",
		SellerName: "Reza VPN", SellerSupport: "@reza_support",
	}
	info := coffeeInfo(cfg, page, nil)
	if got := info["support_contact"]; got != "https://t.me/reza_support" {
		t.Errorf("support_contact = %v, want the seller's handle as a link", got)
	}
	if got := info["seller_name"]; got != "Reza VPN" {
		t.Errorf("seller_name = %v, want the seller's name", got)
	}

	// A contact that is already a link passes through as written.
	page.SellerSupport = "https://t.me/reza_help"
	info = coffeeInfo(cfg, page, nil)
	if got := info["support_contact"]; got != "https://t.me/reza_help" {
		t.Errorf("support_contact = %v, want the seller's link untouched", got)
	}
}

func TestCoffeeInfoKeepsThePanelsSupportForEveryoneElse(t *testing.T) {
	cfg := service.SubSettings{SupportURL: "https://t.me/panel_support"}
	for name, page := range map[string]*service.SubPage{
		"the owner's customer":       {Name: "mine", Status: "active"},
		"a contact-less reseller's":  {Name: "theirs", Status: "active", SellerName: "Reza VPN"},
		"a customer with no devices": {Name: "bare", Status: "active"},
	} {
		info := coffeeInfo(cfg, page, nil)
		if got := info["support_contact"]; got != "https://t.me/panel_support" {
			t.Errorf("%s: support_contact = %v, want the panel's", name, got)
		}
		if _, ok := info["seller_name"]; ok {
			t.Errorf("%s: the answer names a seller it should not", name)
		}
	}
}
