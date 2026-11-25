package api

import (
	"net/http"
	"sort"
	"strings"
	"testing"
)

// What a reseller can reach, pinned.
//
// The gate is derived from the heading a route is documented under, which
// makes the default the strictest one: a new endpoint is the owner's unless
// somebody says otherwise. This test is the other half of that -- it lists,
// exactly, everything a signed-in operator who is not the owner may call, so
// adding to that set is a deliberate edit here rather than a side effect of
// choosing a heading.
func TestWhatANonOwnerMayReach(t *testing.T) {
	want := []string{
		// Their own account, and signing in and out of it.
		"GET /api/auth/me",
		"PATCH /api/auth/me",
		"POST /api/auth/password",
		"POST /api/auth/totp/confirm",
		"POST /api/auth/totp/disable",
		"POST /api/auth/totp/start",
		// Their own passkeys: registered, listed and revoked under the
		// caller's id alone, so one operator's keys are never another's
		// business -- the same scoping the account routes above have.
		"POST /api/passkeys/register/options",
		"POST /api/passkeys/register/verify",
		"GET /api/passkeys",
		"DELETE /api/passkeys/{id}",
		// Their own presets for the new-customer form: saved, listed and
		// deleted under the caller's id alone, the same scoping again --
		// one operator's plan shapes are never another's business.
		"GET /api/presets",
		"POST /api/presets",
		"DELETE /api/presets/{id}",

		// The two pages that are theirs.
		"GET /api/clients",
		"POST /api/clients",
		"GET /api/clients/{id}",
		"PATCH /api/clients/{id}",
		"DELETE /api/clients/{id}",
		"GET /api/clients/{id}/configs",
		// The connection check: it reads through the same scoped customer
		// lookup as the page above, so it only ever shows their own.
		"GET /api/clients/{id}/diagnostics",
		"POST /api/clients/{id}/devices",
		"POST /api/clients/{id}/reset",
		"POST /api/clients/{id}/rotate-keys",
		"POST /api/clients/adjust",
		"POST /api/clients/batch",
		"POST /api/clients/bulk",
		// Adding time or traffic, and selecting every customer a filter
		// matches: over their own customers alone, through the same scope.
		"POST /api/clients/extend",
		"GET /api/clients/ids",
		"GET /api/clients/export",
		"POST /api/clients/import",
		"POST /api/clients/purge",
		"POST /api/clients/reset-all",
		"POST /api/clients/servers/attach",
		"POST /api/clients/servers/detach",
		"GET /api/groups",
		"POST /api/groups",
		"POST /api/groups/action",
		"POST /api/groups/assign",
		"POST /api/groups/delete",
		"GET /api/groups/names",
		"POST /api/groups/rename",
		"GET /api/devices/{id}/profile",
		"GET /api/devices/{id}/profiles",
		"DELETE /api/devices/{id}",

		// Their own sales bot: its settings, plans, codes and orders,
		// each narrowed to the caller in the handler.
		"GET /api/shop/config",
		"PUT /api/shop/config",
		"GET /api/admin-bot/config",
		"PUT /api/admin-bot/config",
		"GET /api/shop/plans",
		"POST /api/shop/plans",
		"DELETE /api/shop/plans/{id}",
		"GET /api/shop/codes",
		"POST /api/shop/codes",
		"DELETE /api/shop/codes/{id}",
		"GET /api/shop/orders",
		"POST /api/shop/orders/{id}/approve",
		"POST /api/shop/orders/{id}/reject",
		"GET /api/shop/orders/{id}/receipt",
		"GET /api/shop/buyers",
		"POST /api/shop/announce",
		"GET /api/shop/stats",

		// The few under an owner's heading they cannot work without.
		"GET /api/interfaces",
		"GET /api/l2tp/devices",
		"GET /api/settings",
		// The tiles over their own customer list, counted over their own
		// customers alone.
		"GET /api/overview",
		// Their own dashboard: their figures, their tunnels, their latest
		// activity -- each counted over their own customers alone.
		"GET /api/overview/mine",
		// Their monthly report, counted over their own customers alone.
		"GET /api/overview/report",
		// The activity record, narrowed in the store to their own lines:
		// what they did, never what anybody else did.
		"GET /api/activity",
		"GET /api/clients/{id}/subscription",
		"POST /api/clients/{id}/subscription/rotate",
	}
	sort.Strings(want)

	var got []string
	for _, r := range newRouteServer().routes() {
		// Unauthenticated routes are the sign-in page's and are not part of
		// this question.
		if !r.Auth && !r.Operator {
			continue
		}
		if r.forOwner() {
			continue
		}
		got = append(got, r.Method+" "+r.Path)
	}
	sort.Strings(got)

	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("what a non-owner may reach has changed.\n got:\n  %s\nwant:\n  %s",
			strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

// Nothing that changes the machine is reachable by anyone but the owner.
func TestTheMachineIsTheOwnersAlone(t *testing.T) {
	ownersGroups := map[string]bool{
		"Interfaces": true, "Nodes": true, "Outbounds": true, "Routing": true,
		"Hosts": true, "Engine": true, "Server": true, "Settings": true,
		"Operators": true,
	}
	for _, r := range newRouteServer().routes() {
		if !ownersGroups[r.Group] || r.forOwner() {
			continue
		}
		// The handful of reads a reseller needs are allowed; a write never is.
		if r.Method != http.MethodGet {
			t.Errorf("%s %s changes the machine and is not the owner's alone", r.Method, r.Path)
		}
	}
}
