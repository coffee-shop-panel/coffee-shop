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
