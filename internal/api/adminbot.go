package api

import (
	"net/http"

	"github.com/abolfazl/w-ui/internal/database/model"
)

// The «بات مدیریت» page.
//
// Every operator may run a management bot of their own — a second bot
// beside their sales bot, under its own token, through which they
// manage their own customers. Everything here is scoped to the caller
// the way the shop's page is: an operator reads and writes their own
// bot's row and no one else's exists as far as these handlers are
// concerned.
//
// The bot token is the one secret this page handles, and it is handled
// the way the panel handles every secret: whether one is set is
// reported, the value never is. A page that echoed it back would put
// it in browser history, in screenshots and in support conversations,
// where it has no business being.

// adminBotOperator is the caller these handlers serve: the manager,
// when this panel was built with one, and the signed-in operator. A
// panel without the manager answers 404 — the feature is not
// off-with-an-error, it is not there — and a request without an
// operator is refused the way the panel's other operator pages refuse
// it.
func (s *Server) adminBotOperator(w http.ResponseWriter, r *http.Request) (*model.Admin, bool) {
	if s.adminBot == nil {
		writeError(w, http.StatusNotFound, "this panel has no management bot")
		return nil, false
	}
	op := adminFrom(r.Context())
	if op == nil {
		writeError(w, http.StatusForbidden, "this needs a signed-in administrator, not an API token")
		return nil, false
	}
	return op, true
}

// adminBotConfigView is one operator's management bot as their page
// reads it. The token is present only as tokenSet, for the reason at
// the top of this file.
type adminBotConfigView struct {
	Enabled     bool   `json:"enabled"`
	TokenSet    bool   `json:"tokenSet"`
	AdminChatID int64  `json:"adminChatId"`
	BotRunning  bool   `json:"botRunning"`
	BotUsername string `json:"botUsername"`
}

// adminBotConfig reads the caller's bot and writes its view. Shared by
// the read and the save, so the page is answered after a save with
// exactly what a fresh read would say.
func (s *Server) adminBotConfig(w http.ResponseWriter, r *http.Request, op *model.Admin) {
	cfg, err := s.adminBot.ConfigFor(r.Context(), op.ID)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	view := adminBotConfigView{
		Enabled:     cfg.Enabled,
		TokenSet:    cfg.Token != "",
		AdminChatID: cfg.AdminChatID,
	}
	view.BotUsername, view.BotRunning = s.adminBot.BotInfo(op.ID)
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleAdminBotConfig(w http.ResponseWriter, r *http.Request) {
	op, ok := s.adminBotOperator(w, r)
	if !ok {
		return
	}
	s.adminBotConfig(w, r, op)
}

// adminBotConfigBody is what the page sends back. The token is a
// pointer to absence, not emptiness: it is a field the page may
// legitimately leave alone, and "not mentioned" has to be tellable
// from a value.
type adminBotConfigBody struct {
	Enabled     bool    `json:"enabled"`
	Token       *string `json:"token"`
	AdminChatID int64   `json:"adminChatId"`
}

func (s *Server) handleAdminBotConfigSave(w http.ResponseWriter, r *http.Request) {
	op, ok := s.adminBotOperator(w, r)
	if !ok {
		return
	}
	var body adminBotConfigBody
	if !decode(w, r, &body) {
		return
	}
	ctx := r.Context()

	cfg, err := s.adminBot.ConfigFor(ctx, op.ID)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	cfg.Enabled = body.Enabled
	cfg.AdminChatID = body.AdminChatID
	// An absent or empty token keeps the one on file: the page never
	// holds the value (it is only ever told one is set), so what it
	// sends back on an ordinary save is "no change", and treating that
	// as "clear it" would switch every bot off at its next save.
	if body.Token != nil && *body.Token != "" {
		cfg.Token = *body.Token
	}
	if err := s.adminBot.SaveConfig(ctx, cfg); err != nil {
		fail(w, s.log, err)
		return
	}
	// The manager is told after the save, so the bot it (re)starts runs
	// on what was written rather than on what was asked for.
	s.adminBot.Reload(op.ID)
	s.adminBotConfig(w, r, op)
}
