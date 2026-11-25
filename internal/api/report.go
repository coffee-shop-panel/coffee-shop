package api

import (
	"net/http"
	"time"
)

// One operator's month, as the report page asks for it: the customers
// they made and what those sales promised, the customers their sales
// bot renewed, and what all of theirs carried, by day and by heaviest
// customer. The service counts over the caller's own customers alone
// (see MonthReport), so the answer is the caller's month and nobody
// else's, whichever operator asks.

// handleOverviewReport serves GET /api/overview/report?month=2006-01.
// No month is this month; a month that does not parse is refused rather
// than silently answered with another one.
func (s *Server) handleOverviewReport(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	if admin == nil {
		writeError(w, http.StatusForbidden, "this report is an operator's")
		return
	}

	now := time.Now().UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	if m := r.URL.Query().Get("month"); m != "" {
		parsed, err := time.Parse("2006-01", m)
		if err != nil {
			writeError(w, http.StatusBadRequest, "month must be YYYY-MM")
			return
		}
		start = parsed
	}

	rep, err := s.clients.MonthReport(r.Context(), start)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
