package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// The owner's announcements to the operators, and each operator's
// reading of them.
//
// The writing half is the owner's alone -- the gates say so before
// these handlers run, and the create stamps the caller as the one
// who spoke. The reading half belongs to whoever is signed in: the
// list an operator gets is the announcements they have not
// dismissed, narrowed in the store by the caller's own id, and a
// dismissal names an announcement but always lands on the caller's
// own row -- there is nothing in a request that could put another
// operator's banners away.

type announcementCreateRequest struct {
	Text string `json:"text"`
}

func (s *Server) handleAnnouncementList(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	if admin == nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if s.announcements == nil {
		writeError(w, http.StatusNotFound, "this panel has no announcements")
		return
	}
	rows, err := s.announcements.List(r.Context())
	if err != nil {
		fail(w, s.log, err)
		return
	}
	if rows == nil {
		rows = []model.Announcement{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleAnnouncementCreate(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	if admin == nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if s.announcements == nil {
		writeError(w, http.StatusNotFound, "this panel has no announcements")
		return
	}
	var req announcementCreateRequest
	if !decode(w, r, &req) {
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		writeError(w, http.StatusBadRequest, "an announcement needs some words")
		return
	}
	if len([]rune(text)) > 2000 {
		writeError(w, http.StatusBadRequest, "the announcement is too long; 2000 characters at most")
		return
	}
	row, err := s.announcements.Create(r.Context(), text, admin.ID)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (s *Server) handleAnnouncementDelete(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	if admin == nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if s.announcements == nil {
		writeError(w, http.StatusNotFound, "this panel has no announcements")
		return
	}
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such announcement")
		return
	}
	deleted, err := s.announcements.Delete(r.Context(), uint(id))
	if err != nil {
		fail(w, s.log, err)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "no such announcement")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleAnnouncementMine(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	if admin == nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if s.announcements == nil {
		writeError(w, http.StatusNotFound, "this panel has no announcements")
		return
	}
	rows, err := s.announcements.ListForOperator(r.Context(), admin.ID)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	if rows == nil {
		rows = []model.Announcement{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) handleAnnouncementDismiss(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	if admin == nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if s.announcements == nil {
		writeError(w, http.StatusNotFound, "this panel has no announcements")
		return
	}
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such announcement")
		return
	}
	found, err := s.announcements.Dismiss(r.Context(), admin.ID, uint(id))
	if err != nil {
		fail(w, s.log, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "no such announcement")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dismissed": true})
}
