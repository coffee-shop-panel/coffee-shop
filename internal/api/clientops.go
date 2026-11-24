package api

import (
	"net/http"
	"time"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// One customer, read as a diagnosis rather than as a form: what their plan
// is, and per device which server it sits on, whether that server is
// answering, and when it was last heard from. It is the set of facts an
// operator otherwise gathers from three pages while a customer waits, and
// most support answers are one of them: the plan ran out, the server is
// down, or the device has simply never connected.

// diagClient is the plan half of the diagnosis. The subscription secret
// itself is never carried, only whether there is one: the link is the
// customer's key, and a page that shows why a customer cannot connect has
// no need of it.
type diagClient struct {
	Name        string             `json:"name"`
	Status      model.ClientStatus `json:"status"`
	QuotaBytes  uint64             `json:"quotaBytes"`
	UsedBytes   uint64             `json:"usedBytes"`
	ExpiresAt   *string            `json:"expiresAt"`
	DeviceLimit int                `json:"deviceLimit"`
	HasSubToken bool               `json:"hasSubToken"`
}

// diagAccount is one device of the customer, with the server it is on.
type diagAccount struct {
	ID            uint    `json:"id"`
	DeviceName    string  `json:"deviceName"`
	InterfaceID   uint    `json:"interfaceId"`
	InterfaceName string  `json:"interfaceName"`
	Protocol      string  `json:"protocol"`
	NodeName      string  `json:"nodeName"`
	NodeUp        bool    `json:"nodeUp"`
	Address       string  `json:"address"`
	LastHandshake *string `json:"lastHandshake"`
	UpBytes       uint64  `json:"upBytes"`
	DownBytes     uint64  `json:"downBytes"`
	HasLink       bool    `json:"hasLink"`
}

type diagView struct {
	Client   diagClient    `json:"client"`
	Accounts []diagAccount `json:"accounts"`
}

func rfc3339OrNil(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// handleClientDiagnostics answers for one customer with the facts a support
// conversation needs. The read goes through the clients service, so it is
// narrowed to the caller like every other customer read: a reseller asking
// after somebody else's customer is told there is no such customer.
func (s *Server) handleClientDiagnostics(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	client, err := s.clients.Get(r.Context(), id)
	if err != nil {
		fail(w, s.log, err)
		return
	}

	// The tunnels and the servers they run on, read once and joined here
	// the way the interface list joins them, rather than one query per
	// device. A dependency that is not there -- a test server built with
	// only the clients service -- leaves the names empty instead of
	// failing a read that has everything else it needs.
	ifacesByID := map[uint]model.Interface{}
	if s.ifaces != nil {
		if list, err := s.ifaces.List(r.Context()); err == nil {
			for _, f := range list {
				ifacesByID[f.ID] = f
			}
		}
	}
	nodesByID := map[uint]model.Node{}
	if s.nodes != nil {
		if list, err := s.nodes.List(r.Context()); err == nil {
			for _, n := range list {
				nodesByID[n.ID] = n
			}
		}
	}

	out := diagView{
		Client: diagClient{
			Name:        client.Name,
			Status:      client.Status,
			QuotaBytes:  client.QuotaBytes,
			UsedBytes:   client.UsedBytes,
			ExpiresAt:   rfc3339OrNil(client.ExpiresAt),
			DeviceLimit: client.DeviceLimit,
			HasSubToken: client.SubToken != "",
		},
		Accounts: make([]diagAccount, 0, len(client.Accounts)),
	}
	for i := range client.Accounts {
		a := &client.Accounts[i]
		av := diagAccount{
			ID:            a.ID,
			DeviceName:    a.DeviceName,
			InterfaceID:   a.InterfaceID,
			Address:       a.IP,
			LastHandshake: rfc3339OrNil(a.LastHandshake),
			UpBytes:       a.UpBytes,
			DownBytes:     a.DownBytes,
			// A config this panel renders itself always exists to hand
			// over; a 3x-ui account has only the link that panel made for
			// it, and until the panel has answered there is none.
			HasLink: true,
		}
		nodeID := a.NodeID
		if f, ok := ifacesByID[a.InterfaceID]; ok {
			av.InterfaceName = f.Name
			av.Protocol = string(f.Protocol)
			if nodeID == 0 {
				nodeID = f.NodeID
			}
			if f.Protocol == model.ProtocolXray {
				// The account's own counters live on the 3x-ui panel's
				// report, not in the tunnel counters, which stay zero.
				av.UpBytes, av.DownBytes = a.XUIUp, a.XUIDown
				av.HasLink = a.XUILink != ""
			}
		}
		if n, ok := nodesByID[nodeID]; ok {
			av.NodeName = n.Name
			// The local node is up by definition: this code is running
			// on it.
			av.NodeUp = n.Kind == model.KindLocal || n.Reachable
		}
		out.Accounts = append(out.Accounts, av)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleMoveInterfaceClients empties one WireGuard server onto another:
// every customer on the server named in the path is moved to the one named
// in the body, and the answer is how many moved and how many could not be.
func (s *Server) handleMoveInterfaceClients(w http.ResponseWriter, r *http.Request) {
	fromID, ok := pathID(w, r)
	if !ok {
		return
	}
	var body struct {
		ToInterfaceID uint `json:"toInterfaceId"`
	}
	if !decode(w, r, &body) {
		return
	}
	moved, failed, err := s.clients.MoveInterface(r.Context(), fromID, body.ToInterfaceID)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"moved": moved, "failed": failed})
}
