package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// An operator's own presets for the new-customer form: the plans they
// sell often, saved once and applied with a choice instead of a
// retyping of the tunnels, the quota, the length and the device count.
//
// Every handler here takes the operator from the session and the store
// scopes every statement to them, so the endpoints have no id of an
// operator in them at all: there is nothing in a request that could be
// pointed at another seller's list. A preset is a bookmark, not a
// grant -- applying one fills the form, and the create behind the form
// still refuses a tunnel the operator may not use, exactly as it does
// for values typed by hand.

type presetCreateRequest struct {
	Name         string `json:"name"`
	InterfaceIDs []uint `json:"interfaceIds"`
	QuotaBytes   uint64 `json:"quotaBytes"`
	Days         int    `json:"days"`
	DeviceLimit  int    `json:"deviceLimit"`
}

func (s *Server) handlePresetList(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	if admin == nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if s.presets == nil {
		writeError(w, http.StatusNotFound, "this panel has no presets")
		return
	}
	rows, err := s.presets.List(r.Context(), admin.ID)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	if rows == nil {
		rows = []model.ClientPreset{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handlePresetCreate(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	if admin == nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if s.presets == nil {
		writeError(w, http.StatusNotFound, "this panel has no presets")
		return
	}
	var req presetCreateRequest
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "a preset needs a name")
		return
	}
	if len([]rune(name)) > 64 {
		writeError(w, http.StatusBadRequest, "the preset's name is too long; 64 characters at most")
		return
	}
	if len(req.InterfaceIDs) > 1024 {
		writeError(w, http.StatusBadRequest, "a preset cannot hold that many tunnels")
		return
	}
	if req.Days < 0 || req.Days > 36500 {
		writeError(w, http.StatusBadRequest, "the preset's length is out of range")
		return
	}
	if req.DeviceLimit < 0 || req.DeviceLimit > 1000 {
		writeError(w, http.StatusBadRequest, "the preset's device limit is out of range")
		return
	}
	// A tunnel picked twice is a form slip, not a plan: the list the
	// preset carries is the set the form will be given.
	ids := make([]uint, 0, len(req.InterfaceIDs))
	seen := map[uint]bool{}
	for _, id := range req.InterfaceIDs {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	row := &model.ClientPreset{
		AdminID:      admin.ID,
		Name:         name,
		InterfaceIDs: model.JSON(ids),
		QuotaBytes:   req.QuotaBytes,
		Days:         req.Days,
		DeviceLimit:  req.DeviceLimit,
	}
	if err := s.presets.Create(r.Context(), row); err != nil {
		fail(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (s *Server) handlePresetDelete(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	if admin == nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if s.presets == nil {
		writeError(w, http.StatusNotFound, "this panel has no presets")
		return
	}
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such preset")
		return
	}
	deleted, err := s.presets.Delete(r.Context(), admin.ID, uint(id))
	if err != nil {
		fail(w, s.log, err)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "no such preset")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}
