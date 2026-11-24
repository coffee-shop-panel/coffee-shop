package api

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/abolfazl/w-ui/internal/service"
)

// The coffee page: the subscription page Ali's customers knew from the old
// panel, carried over whole. It is a self-contained page that speaks a small
// JSON API -- info and nodes under /api/v1/sub/<token>/ -- which this file
// answers from the same SubPage the panel's own page is built from. The look
// is chosen with the "parseh" template; every other template keeps the
// panel's own page.

//go:embed assets/coffee.html
var coffeePageHTML []byte

// coffeeServedPage is the page as served: its two inline scripts taken out
// and registered as files of their own (subassets.go), because the service's
// content-security policy allows scripts only from its own origin -- an
// inline script is refused by the browser and the page sits on its spinner.
// The scripts keep their places and their order; only their address changes.
var coffeeServedPage = coffeeExternalScripts(coffeePageHTML)

func coffeeExternalScripts(page []byte) []byte {
	out := page
	for i, name := range []string{"coffee-qr", "coffee-app"} {
		open := []byte("<script>")
		start := bytes.Index(out, open)
		if start < 0 {
			return page
		}
		bodyStart := start + len(open)
		end := bytes.Index(out[bodyStart:], []byte("</script>"))
		if end < 0 {
			return page
		}
		body := out[bodyStart : bodyStart+end]
		src := registerSubAsset(name, ".js", "text/javascript; charset=utf-8", body)
		tag := []byte(`<script src="` + src + `"></script>`)
		next := make([]byte, 0, len(out))
		next = append(next, out[:start]...)
		next = append(next, tag...)
		next = append(next, out[bodyStart+end+len("</script>"):]...)
		out = next
		_ = i
	}
	return out
}

// serveCoffeePage writes the coffee page. Its figures are not in the markup:
// the page asks the API below for them, so one copy serves every customer.
func (s *Server) serveCoffeePage(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(coffeeServedPage)
}

// serveCoffeeAPI answers the coffee page's two calls. True means the request
// was this API's, answered or refused; anything else falls through.
func (s *Server) serveCoffeeAPI(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/sub/")
	if rest == r.URL.Path {
		return false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || (parts[1] != "info" && parts[1] != "nodes" && parts[1] != "hiddify") {
		return false
	}
	token := parts[0]
	ctx := r.Context()
	cfg, err := s.subs.Settings(ctx)
	if err != nil || !cfg.Enabled {
		http.NotFound(w, r)
		return true
	}
	exists, err := s.subs.TokenExists(ctx, token)
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return true
	}
	if !exists {
		http.NotFound(w, r)
		return true
	}
	link, _ := s.subs.LinkFor(ctx, token, r.Host)
	page, err := s.subs.PageFor(ctx, token, link)
	if err != nil {
		http.NotFound(w, r)
		return true
	}
	if parts[1] == "hiddify" {
		s.serveCoffeeHiddify(w, r, page, token)
		return true
	}
	nodes := coffeeNodes(cfg, page, token)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if parts[1] == "nodes" {
		_ = json.NewEncoder(w).Encode(map[string]any{"nodes": nodes})
		return true
	}
	_ = json.NewEncoder(w).Encode(coffeeInfo(cfg, page, nodes))
	return true
}

// coffeeNodes is the page's config list: one entry per file the customer can
// take, named for the host it was written for when it has one, and fetched
// from the same device download the panel's own page links to.
func coffeeNodes(cfg service.SubSettings, page *service.SubPage, token string) []map[string]any {
	base := strings.TrimSuffix(cfg.Path, "/") + "/" + token
	nodes := make([]map[string]any, 0, len(page.Devices))
	for _, d := range page.Devices {
		name := d.HostName
		if name == "" {
			name = d.Label
		}
		if name == "" {
			name = d.Tunnel
		}
		nodes = append(nodes, map[string]any{
			"node_id":     d.ID,
			"name":        name,
			"online":      true,
			"recommended": false,
			"config_path": base + "?device=" + uitoa(d.ID) + "&host=" + uitoa(d.HostID),
		})
	}
	return nodes
}

// coffeeInfo is the account the coffee page opens with, in its own field
// names: bytes counted, the expiry as a date and a day count, and the nodes
// riding along so the page needs no second call.
func coffeeInfo(cfg service.SubSettings, page *service.SubPage, nodes []map[string]any) map[string]any {
	status := page.Status
	now := time.Now().UTC()
	if page.ExpiresAt != nil && now.After(*page.ExpiresAt) && status == "active" {
		status = "expired"
	}
	if status == "active" && page.QuotaBytes > 0 && page.UsedBytes >= page.QuotaBytes {
		status = "limited"
	}
	if status == "exhausted" {
		status = "limited"
	}
	var expiry any
	var daysLeft any
	if page.ExpiresAt != nil {
		expiry = page.ExpiresAt.UTC().Format(time.RFC3339)
		d := int(time.Until(*page.ExpiresAt).Hours()) / 24
		if d < 0 {
			d = 0
		}
		daysLeft = d
	}
	var lastHandshake any
	if page.LastOnline != nil {
		lastHandshake = page.LastOnline.UTC().Format(time.RFC3339)
	}
	seen := map[uint]bool{}
	hasLinks := false
	for _, d := range page.Devices {
		seen[d.ID] = true
		// Link-form configs are the proxy apps' food: an Xray tunnel's
		// links, or a plain WireGuard one's. An AmneziaWG file has no
		// link form -- the panel's own link list skips it too -- so it
		// does not count, and the page hides its V2Box tile for a
		// customer who holds nothing else.
		if d.Protocol == "xray" || (d.Protocol == "wireguard" && d.AmneziaKey == "") {
			hasLinks = true
		}
	}
	return map[string]any{
		"label":             page.Name,
		"status":            status,
		"data_quota_bytes":  page.QuotaBytes,
		"data_used_bytes":   page.UsedBytes,
		"expiry_at":         expiry,
		"days_left":         daysLeft,
		"device_limit":      0,
		"devices_active":    len(seen),
		"last_handshake_at": lastHandshake,
		"support_contact":   cfg.SupportURL,
		"has_links":         hasLinks,
		"nodes":             nodes,
	}
}

// serveCoffeeHiddify answers the page's one-tap Hiddify button: the whole
// subscription as one sing-box JSON of AmneziaWG endpoints, which the
// current Hiddify app imports as a group. The panel's own link list cannot
// carry these configs -- a wireguard:// link has no room for the AmneziaWG
// values -- so each endpoint is rebuilt here from the same rendered file
// the page offers for download, value for value.
func (s *Server) serveCoffeeHiddify(w http.ResponseWriter, r *http.Request, page *service.SubPage, token string) {
	type peer struct {
		Address   string   `json:"address"`
		Port      int      `json:"port"`
		PublicKey string   `json:"public_key"`
		Preshared string   `json:"pre_shared_key,omitempty"`
		// The same key under the spelling Hiddify's own docs print; their
		// fork reads one, upstream sing-box the other, so both ride along.
		PresharedAlt string `json:"preshared_key,omitempty"`
		Allowed   []string `json:"allowed_ips"`
		Keepalive int      `json:"persistent_keepalive_interval,omitempty"`
	}
	type endpoint struct {
		Type  string         `json:"type"`
		Tag   string         `json:"tag"`
		Key   string         `json:"private_key"`
		Addr  []string       `json:"address"`
		MTU   int            `json:"mtu,omitempty"`
		AWG   map[string]any `json:"awg,omitempty"`
		Peers []peer         `json:"peers"`
	}
	ctx := r.Context()
	endpoints := make([]endpoint, 0, len(page.Devices))
	tags := make([]string, 0, len(page.Devices))
	usedTags := map[string]int{}
	for _, d := range page.Devices {
		profile, err := s.subs.DeviceConfig(ctx, token, d.ID, d.HostID)
		if err != nil {
			continue
		}
		vals := parseConf(profile.Body)
		priv := vals["interface.privatekey"]
		pub := vals["peer.publickey"]
		ep := vals["peer.endpoint"]
		if priv == "" || pub == "" || ep == "" {
			continue
		}
		host, portStr, err := net.SplitHostPort(ep)
		if err != nil {
			i := strings.LastIndex(ep, ":")
			if i < 0 {
				continue
			}
			host, portStr = ep[:i], ep[i+1:]
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		name := d.HostName
		if name == "" {
			name = d.Label
		}
		if name == "" {
			name = d.Tunnel
		}
		if name == "" {
			name = "node"
		}
		usedTags[name]++
		if usedTags[name] > 1 {
			name = fmt.Sprintf("%s (%d)", name, usedTags[name])
		}
		awg := map[string]any{}
		for _, k := range []string{"jc", "jmin", "jmax", "s1", "s2", "s3", "s4"} {
			if v, err := strconv.Atoi(vals["interface."+k]); err == nil && v != 0 {
				awg[k] = v
			}
		}
		for _, k := range []string{"h1", "h2", "h3", "h4", "i1", "i2", "i3", "i4", "i5"} {
			if v := vals["interface."+k]; v != "" && v != "0" {
				awg[k] = v
			}
		}
		if raw, err := base64.StdEncoding.DecodeString(vals["interface.headerprotectionkey"]); err == nil && len(raw) > 0 {
			awg["header_protection_key"] = hex.EncodeToString(raw)
		}
		e := endpoint{
			Type: "awg",
			Tag:  name,
			Key:  priv,
			Addr: splitCSV(vals["interface.address"]),
			Peers: []peer{{
				Address:   host,
				Port:      port,
				PublicKey: pub,
				Preshared:    vals["peer.presharedkey"],
				PresharedAlt: vals["peer.presharedkey"],
				Allowed:   splitCSV(vals["peer.allowedips"]),
				Keepalive: atoiOr(vals["peer.persistentkeepalive"], 0),
			}},
		}
		if mtu, err := strconv.Atoi(vals["interface.mtu"]); err == nil && mtu > 0 {
			e.MTU = mtu
		}
		if len(awg) > 0 {
			e.AWG = awg
		}
		endpoints = append(endpoints, e)
		tags = append(tags, e.Tag)
	}
	if len(endpoints) == 0 {
		http.Error(w, "nothing on this subscription is for Hiddify", http.StatusNotFound)
		return
	}
	doc := map[string]any{
		"endpoints": endpoints,
		"outbounds": []any{
			map[string]any{"type": "urltest", "tag": "auto", "outbounds": tags, "url": "https://cp.cloudflare.com/generate_204", "interval": "3m0s", "tolerance": 50},
			map[string]any{"type": "selector", "tag": "select", "outbounds": append([]string{"auto"}, tags...), "default": "auto"},
		},
	}
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	if page.QuotaBytes > 0 || page.ExpiresAt != nil {
		var expire int64
		if page.ExpiresAt != nil {
			expire = page.ExpiresAt.Unix()
		}
		h.Set("Subscription-Userinfo", fmt.Sprintf("upload=0; download=%d; total=%d; expire=%d", page.UsedBytes, page.QuotaBytes, expire))
	}
	_ = json.NewEncoder(w).Encode(doc)
}

// parseConf reads a WireGuard-family file as section.key -> value, first
// occurrence winning, comments and blank lines skipped.
func parseConf(body []byte) map[string]string {
	vals := map[string]string{}
	section := ""
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := section + "." + strings.ToLower(strings.TrimSpace(k))
		if _, seen := vals[key]; !seen {
			vals[key] = strings.TrimSpace(v)
		}
	}
	return vals
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func atoiOr(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

func uitoa(v uint) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
