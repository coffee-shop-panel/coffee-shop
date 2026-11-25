package api

import (
	"net/http"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/service"
)

// The dashboard an operator who is not the owner lands on, in one call:
// their own customers' figures, the tunnels they were given to sell with
// whether each is up, and their own latest activity. Everything in it is
// the caller's -- the aggregates are narrowed in the service by the
// caller's owner set, the tunnels by the same scope the customer form's
// server picker is narrowed by, and the activity by the caller's id in
// the store. Nothing here is the panel's totals in miniature.

// mineTunnel is one of the caller's tunnels with the one fact the
// dashboard exists to show about it: whether it is carrying anyone. The
// load, the pool and the node behind it stay the owner's business, as
// they are in the server picker's thinner answer.
type mineTunnel struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Mode     string `json:"mode"`
	Enabled  bool   `json:"enabled"`
	Up       bool   `json:"up"`
}

// mineResponse is the dashboard's whole answer.
type mineResponse struct {
	*service.MineOverview
	Tunnels  []mineTunnel   `json:"tunnels"`
	Activity []activityView `json:"activity"`
}

// mineActivityCount is how much of the caller's record the dashboard
// shows. The activity page is one click away for the rest of it.
const mineActivityCount = 6

func (s *Server) handleOverviewMine(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	if admin == nil {
		writeError(w, http.StatusForbidden, "this dashboard is an operator's")
		return
	}
	ctx := r.Context()

	figures, err := s.clients.Mine(ctx)
	if err != nil {
		fail(w, s.log, err)
		return
	}

	views, err := s.interfaceViews(r)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	sc := service.ScopeOf(ctx)
	tunnels := make([]mineTunnel, 0, len(views))
	for _, v := range views {
		if !sc.Allows(v.Interface.ID) {
			continue
		}
		tunnels = append(tunnels, mineTunnel{
			ID:       v.Interface.ID,
			Name:     v.Interface.Name,
			Protocol: string(v.Interface.Protocol),
			Mode:     string(v.Interface.Mode),
			Enabled:  v.Interface.Enabled,
			Up:       v.Running,
		})
	}

	activity := []activityView{}
	if s.activity != nil {
		var entries []model.ActivityEntry
		var err error
		if admin.Role.ManagesPanel() {
			entries, err = s.activity.List(mineActivityCount, 0)
		} else {
			entries, err = s.activity.ListForActor(admin.ID, admin.Username, mineActivityCount, 0)
		}
		if err != nil {
			fail(w, s.log, err)
			return
		}
		activity = activityViews(entries)
	}

	writeJSON(w, http.StatusOK, mineResponse{
		MineOverview: figures,
		Tunnels:      tunnels,
		Activity:     activity,
	})
}
