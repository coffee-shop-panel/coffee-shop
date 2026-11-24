package adminbot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The Telegram client: the Bot API is a handful of JSON calls, so it is
// spoken directly, the way the panel's other bots speak it, rather than
// through a library that would be most of this package.
//
// This is the management bots' own client, not the sales bots' shared
// one: the two bot families run different operators' tokens on
// different schedules, and a client each keeps a change to one family's
// wire from reaching the other.
//
// APIBase is a variable so tests can point the client at a fake server.
// Production never changes it.
var APIBase = "https://api.telegram.org"

// tgUser is who a message or a button press came from.
type tgUser struct {
	ID        int64  `json:"id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

// name is how the person is addressed in operator-facing text: their
// name, and their handle when they have one.
func (u *tgUser) name() string {
	n := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if n == "" {
		n = fmt.Sprint(u.ID)
	}
	if u.Username != "" {
		n += " (@" + u.Username + ")"
	}
	return n
}

// tgPhoto is one size of a photo a chat sent. Telegram offers several;
// the last in the list is the largest.
type tgPhoto struct {
	FileID   string `json:"file_id"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	FileSize int64  `json:"file_size"`
}

// tgMessage is one message, in either direction.
type tgMessage struct {
	ID      int64      `json:"message_id"`
	From    *tgUser    `json:"from"`
	Chat    tgChat     `json:"chat"`
	Text    string     `json:"text"`
	Caption string     `json:"caption"`
	Photo   []tgPhoto  `json:"photo"`
	ReplyTo *tgMessage `json:"reply_to_message"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

// photoFileID is the file id of the largest size of the message's
// photo, or "" when the message carries none.
func (m *tgMessage) photoFileID() string {
	if len(m.Photo) == 0 {
		return ""
	}
	return m.Photo[len(m.Photo)-1].FileID
}

// tgCallback is a press on an inline button.
type tgCallback struct {
	ID      string     `json:"id"`
	From    *tgUser    `json:"from"`
	Message *tgMessage `json:"message"`
	Data    string     `json:"data"`
}

// tgUpdate is one thing Telegram reports to a polling bot.
type tgUpdate struct {
	ID       int64       `json:"update_id"`
	Message  *tgMessage  `json:"message"`
	Callback *tgCallback `json:"callback_query"`
}

// tgButton is one inline button: either a callback the bot hears about,
// or a link it does not.
type tgButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data,omitempty"`
	URL  string `json:"url,omitempty"`
}

// tgKeyboard is an inline keyboard: rows of buttons.
type tgKeyboard [][]tgButton

// markup renders the keyboard as sendMessage's reply_markup value, or
// nil when there is none.
func (k tgKeyboard) markup() map[string]any {
	if len(k) == 0 {
		return nil
	}
	return map[string]any{"inline_keyboard": k}
}

// tgClient talks to one bot's API. One client per bot: the token is the
// identity, and operators' bots must never share one.
type tgClient struct {
	token string
	http  *http.Client
}

func newTGClient(token string) *tgClient {
	return &tgClient{token: token, http: &http.Client{}}
}

// call posts one method and decodes Telegram's result into out.
func (c *tgClient) call(ctx context.Context, method string, params map[string]any, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		APIBase+"/bot"+url.PathEscape(c.token)+"/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var envelope struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&envelope); err != nil {
		return fmt.Errorf("telegram %s: %s: %w", method, resp.Status, err)
	}
	if !envelope.OK {
		return fmt.Errorf("telegram %s: %s", method, envelope.Description)
	}
	if out != nil {
		return json.Unmarshal(envelope.Result, out)
	}
	return nil
}

// getMe is who this token belongs to; the bot learns its own username
// from it, which the panel shows the operator.
func (c *tgClient) getMe(ctx context.Context) (*tgUser, error) {
	var me tgUser
	if err := c.call(ctx, "getMe", map[string]any{}, &me); err != nil {
		return nil, err
	}
	return &me, nil
}

// deleteWebhook clears any webhook left on the bot. A bot that has a
// webhook set receives nothing by polling, so a token that was ever used
// with one would look dead here for no visible reason.
func (c *tgClient) deleteWebhook(ctx context.Context) error {
	return c.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
}

// getUpdates long-polls for what happened since offset. The server holds
// the request open for up to timeoutSec; the client's own deadline is set
// by call, comfortably past that.
func (c *tgClient) getUpdates(ctx context.Context, offset int64, timeoutSec int) ([]tgUpdate, error) {
	var updates []tgUpdate
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message", "callback_query"},
	}, &updates)
	return updates, err
}

// sendMessage writes a message, HTML-formatted, with an optional
// keyboard, and returns what was sent.
func (c *tgClient) sendMessage(ctx context.Context, chatID int64, text string, kb tgKeyboard) (*tgMessage, error) {
	params := map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	if m := kb.markup(); m != nil {
		params["reply_markup"] = m
	}
	var sent tgMessage
	if err := c.call(ctx, "sendMessage", params, &sent); err != nil {
		return nil, err
	}
	return &sent, nil
}

// editMessageText rewrites a message the bot sent, keyboard included.
// The keyboard is always sent — an empty one when kb is nil — so an edit
// lands with exactly the buttons it means to have, never with the ones
// the previous screen left behind.
func (c *tgClient) editMessageText(ctx context.Context, chatID, msgID int64, text string, kb tgKeyboard) error {
	m := kb.markup()
	if m == nil {
		m = map[string]any{"inline_keyboard": []tgButton{}}
	}
	return c.call(ctx, "editMessageText", map[string]any{
		"chat_id":                  chatID,
		"message_id":               msgID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
		"reply_markup":             m,
	}, nil)
}

// editMessageReplyMarkup swaps a message's keyboard; an empty keyboard
// takes the buttons away, which is how a decided screen stops offering
// its buttons.
func (c *tgClient) editMessageReplyMarkup(ctx context.Context, chatID, msgID int64, kb tgKeyboard) error {
	m := kb.markup()
	if m == nil {
		m = map[string]any{"inline_keyboard": []tgButton{}}
	}
	return c.call(ctx, "editMessageReplyMarkup", map[string]any{
		"chat_id": chatID, "message_id": msgID, "reply_markup": m,
	}, nil)
}

// answerCallbackQuery acknowledges a button press; text, when given, is
// what Telegram shows the person who pressed it.
func (c *tgClient) answerCallbackQuery(ctx context.Context, id, text string, alert bool) error {
	params := map[string]any{"callback_query_id": id}
	if text != "" {
		params["text"] = text
		params["show_alert"] = alert
	}
	return c.call(ctx, "answerCallbackQuery", params, nil)
}

// copyMessage forwards a message as a copy. A caption, when given,
// replaces the original's. It returns the copy.
func (c *tgClient) copyMessage(ctx context.Context, toChat, fromChat, msgID int64, caption string, kb tgKeyboard) (*tgMessage, error) {
	params := map[string]any{
		"chat_id":      toChat,
		"from_chat_id": fromChat,
		"message_id":   msgID,
	}
	if caption != "" {
		params["caption"] = caption
		params["parse_mode"] = "HTML"
	}
	if m := kb.markup(); m != nil {
		params["reply_markup"] = m
	}
	var sent tgMessage
	if err := c.call(ctx, "copyMessage", params, &sent); err != nil {
		return nil, err
	}
	return &sent, nil
}

// escapeHTML makes a user-derived string safe to sit inside the bot's
// HTML: a customer's name is theirs to choose, and an unescaped one
// could break the message's formatting or forge its links.
func escapeHTML(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	).Replace(s)
}
