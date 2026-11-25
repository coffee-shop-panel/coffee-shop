package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/fault"
	"github.com/abolfazl/w-ui/internal/notify"
)

// Signing in with a passkey: a fingerprint, a face or a device PIN, in
// place of the password.
//
// A passkey is a key pair the operator's own device keeps. The panel
// stores only the public half, so the table is not a list of secrets; and
// the assertion the device signs is bound to this panel's name and this
// ceremony's challenge, so one read off the wire -- or phished out of the
// operator on another site -- opens nothing here.
//
// The ceremonies lean on the panel's one hard requirement: WebAuthn only
// exists on an encrypted origin. The relying party is therefore derived
// from the request itself -- the host as the browser sent it -- and every
// endpoint here refuses a request the panel cannot see arrived over TLS,
// the same clientScheme test the session cookie's Secure flag uses. A
// panel opened by plain HTTP simply has no passkeys, and the sign-in page
// hides the button rather than offering one that cannot work.

// passkeyCeremonyTTL is how long a begun ceremony may be finished in.
// Long enough for a person to find their phone and put a finger on it;
// short enough that a challenge left lying in a page is worthless by the
// time anyone picks it up.
const passkeyCeremonyTTL = 5 * time.Minute

// passkeyCeremony is one begun ceremony, held server-side until it is
// finished or it lapses. The challenge is the key: the answer carries it
// inside its signed client data, so nothing about the ceremony has to be
// trusted from the request beyond what the signature covers.
type passkeyCeremony struct {
	session *webauthn.SessionData
	// adminID is the operator a registration will belong to. A sign-in
	// ceremony has none: who it belongs to is what the assertion says.
	adminID uint
	// name is the label asked for with the registration options, kept
	// here so the verify step -- whose body is the authenticator's
	// answer and nothing else -- still has it.
	name  string
	login bool
}

// errPasskeyNeedsTLS is the refusal every passkey endpoint gives a
// request that did not arrive over an encrypted connection.
var errPasskeyNeedsTLS = errors.New("passkeys need the panel over HTTPS")

// passkeyWA builds the WebAuthn relying party for this request.
//
// The id is the host without its port and the one origin allowed is the
// address exactly as the browser sent it, port and all: the panel may be
// installed on any port, behind the proxy that holds its certificate, and
// a relying party named at build time would be wrong on every install
// that differs. Deriving it per request keeps one binary honest on all
// of them.
func (s *Server) passkeyWA(r *http.Request) (*webauthn.WebAuthn, error) {
	if clientScheme(r) != "https" {
		return nil, errPasskeyNeedsTLS
	}
	host := r.Host
	rpID := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		rpID = h
	}
	wa, err := webauthn.New(&webauthn.Config{
		RPDisplayName: "W-UI",
		RPID:          rpID,
		RPOrigins:     []string{"https://" + host},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: passkeyCeremonyTTL},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: passkeyCeremonyTTL},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("api: webauthn for host %q: %w", host, err)
	}
	return wa, nil
}

// passkeyReady answers the relying party for a handler, or writes the
// refusal -- the feature missing, or the connection not encrypted -- and
// reports false.
func (s *Server) passkeyReady(w http.ResponseWriter, r *http.Request) (*webauthn.WebAuthn, bool) {
	if s.passkeys == nil {
		writeError(w, http.StatusNotFound, "this panel has no passkeys")
		return nil, false
	}
	wa, err := s.passkeyWA(r)
	if errors.Is(err, errPasskeyNeedsTLS) {
		writeError(w, http.StatusBadRequest,
			"passkeys only work when the panel is opened over HTTPS")
		return nil, false
	}
	if err != nil {
		fail(w, s.log, err)
		return nil, false
	}
	return wa, true
}

// beginCeremony keeps a begun ceremony until its verify step, and drops
// the ones that lapsed without being finished.
func (s *Server) beginCeremony(c *passkeyCeremony) {
	s.pkMu.Lock()
	defer s.pkMu.Unlock()
	if s.pkPending == nil {
		s.pkPending = map[string]*passkeyCeremony{}
	}
	now := time.Now()
	for k, v := range s.pkPending {
		if now.After(v.session.Expires) {
			delete(s.pkPending, k)
		}
	}
	s.pkPending[c.session.Challenge] = c
}

// takeCeremony takes the ceremony a challenge names, finished or not:
// a challenge is spent by being answered once, so a failed ceremony
// cannot be retried against the same one, and a captured answer cannot
// be played back.
func (s *Server) takeCeremony(challenge string) *passkeyCeremony {
	s.pkMu.Lock()
	defer s.pkMu.Unlock()
	c := s.pkPending[challenge]
	if c == nil {
		return nil
	}
	delete(s.pkPending, challenge)
	if time.Now().After(c.session.Expires) {
		return nil
	}
	return c
}

// ceremonyChallenge reads the challenge out of a ceremony answer and
// returns the body with it, for the library to read again.
//
// The challenge is inside the client data, which is inside the answer --
// three layers down -- and the session it names has to be found before
// the library can be asked to judge the answer at all. What is read here
// is only ever a lookup key: the library re-reads the whole answer,
// signature and all, before anything is believed.
func ceremonyChallenge(r *http.Request) (string, []byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return "", nil, fmt.Errorf("read ceremony answer: %w", err)
	}
	var probe struct {
		Response struct {
			ClientDataJSON string `json:"clientDataJSON"`
		} `json:"response"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || probe.Response.ClientDataJSON == "" {
		return "", body, errors.New("the answer carries no client data")
	}
	raw, err := base64.RawURLEncoding.DecodeString(
		strings.TrimRight(probe.Response.ClientDataJSON, "="))
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(probe.Response.ClientDataJSON)
		if err != nil {
			return "", body, errors.New("the client data is not readable")
		}
	}
	var clientData struct {
		Challenge string `json:"challenge"`
	}
	if err := json.Unmarshal(raw, &clientData); err != nil || clientData.Challenge == "" {
		return "", body, errors.New("the client data carries no challenge")
	}
	return clientData.Challenge, body, nil
}

// passkeyUser is an operator as WebAuthn needs them: a stable handle --
// the operator's id, which never changes under them -- and the keys they
// have registered.
type passkeyUser struct {
	admin *model.Admin
	creds []webauthn.Credential
}

func (u *passkeyUser) WebAuthnID() []byte {
	return []byte(strconv.FormatUint(uint64(u.admin.ID), 10))
}

func (u *passkeyUser) WebAuthnName() string        { return u.admin.Username }
func (u *passkeyUser) WebAuthnDisplayName() string { return u.admin.Username }
func (u *passkeyUser) WebAuthnCredentials() []webauthn.Credential {
	return u.creds
}

// passkeyUserFor loads an operator with their keys in library form.
func (s *Server) passkeyUserFor(r *http.Request, admin *model.Admin) (*passkeyUser, error) {
	rows, err := s.passkeys.List(r.Context(), admin.ID)
	if err != nil {
		return nil, err
	}
	user := &passkeyUser{admin: admin}
	for _, row := range rows {
		user.creds = append(user.creds, libraryCredential(row))
	}
	return user, nil
}

// libraryCredential rebuilds a stored passkey for the library.
//
// The flags are rebuilt from what was stored: present and verified are
// set, because both were required of the ceremony that registered the
// key, and the backup flags are the row's own.
func libraryCredential(row model.PasskeyCredential) webauthn.Credential {
	var transports []protocol.AuthenticatorTransport
	if row.Transports != "" {
		for _, t := range strings.Split(row.Transports, ",") {
			transports = append(transports, protocol.AuthenticatorTransport(strings.TrimSpace(t)))
		}
	}
	flags := protocol.FlagUserPresent | protocol.FlagUserVerified
	if row.BackupEligible {
		flags |= protocol.FlagBackupEligible
	}
	if row.BackupState {
		flags |= protocol.FlagBackupState
	}
	return webauthn.Credential{
		ID:        row.CredentialID,
		PublicKey: row.PublicKey,
		Transport: transports,
		Flags:     webauthn.NewCredentialFlags(flags),
		Authenticator: webauthn.Authenticator{
			SignCount: row.SignCount,
		},
	}
}

// passkeyRow is a proved credential as a row, under the name its owner
// asked for.
func passkeyRow(adminID uint, name string, cred *webauthn.Credential) *model.PasskeyCredential {
	transports := make([]string, 0, len(cred.Transport))
	for _, t := range cred.Transport {
		transports = append(transports, string(t))
	}
	return &model.PasskeyCredential{
		AdminID:        adminID,
		CredentialID:   cred.ID,
		PublicKey:      cred.PublicKey,
		SignCount:      cred.Authenticator.SignCount,
		Transports:     strings.Join(transports, ","),
		BackupEligible: cred.Flags.BackupEligible,
		BackupState:    cred.Flags.BackupState,
		Name:           name,
	}
}

type registerOptionsRequest struct {
	Name string `json:"name"`
}

// handlePasskeyRegisterOptions begins a registration for the signed-in
// operator.
//
// The key must be discoverable and the person must be verified by it --
// a fingerprint, not a touch -- because the key is going to stand in for
// the password entirely. The keys already registered are excluded, so
// the same authenticator is never enrolled twice.
func (s *Server) handlePasskeyRegisterOptions(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	if admin == nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	wa, ok := s.passkeyReady(w, r)
	if !ok {
		return
	}

	// The name rides with the options because the verify step's body is
	// the authenticator's answer and carries nothing else. A body that
	// does not parse names the key by its date instead.
	var req registerOptionsRequest
	if r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if err == nil {
			_ = json.Unmarshal(body, &req)
		}
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "Passkey · " + time.Now().Format("2006-01-02")
	}

	user, err := s.passkeyUserFor(r, admin)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	creation, session, err := wa.BeginRegistration(user,
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		}),
		webauthn.WithExclusions(webauthn.Credentials(user.creds).CredentialDescriptors()),
	)
	if err != nil {
		fail(w, s.log, fmt.Errorf("begin passkey registration: %w", err))
		return
	}
	s.beginCeremony(&passkeyCeremony{session: session, adminID: admin.ID, name: name})
	writeJSON(w, http.StatusOK, creation)
}

// handlePasskeyRegisterVerify finishes a registration and stores the key
// the ceremony proved.
func (s *Server) handlePasskeyRegisterVerify(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	if admin == nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	wa, ok := s.passkeyReady(w, r)
	if !ok {
		return
	}
	challenge, body, err := ceremonyChallenge(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "that passkey answer is not readable")
		return
	}
	c := s.takeCeremony(challenge)
	if c == nil || c.login || c.adminID != admin.ID {
		writeError(w, http.StatusBadRequest,
			"that passkey ceremony has expired; start again")
		return
	}
	user, err := s.passkeyUserFor(r, admin)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	cred, err := wa.FinishRegistration(user, *c.session, r)
	if err != nil {
		s.log.Warn("passkey registration refused", "username", admin.Username,
			"ip", clientIP(r), "error", err)
		writeError(w, http.StatusBadRequest,
			"that passkey could not be registered; try again")
		return
	}
	row := passkeyRow(admin.ID, c.name, cred)
	if err := s.passkeys.Create(r.Context(), row); err != nil {
		fail(w, s.log, err)
		return
	}
	s.log.Info("passkey registered", "username", admin.Username, "ip", clientIP(r))
	writeJSON(w, http.StatusCreated, passkeyViewOf(*row))
}

// passkeyView is a passkey as its owner sees it in the settings: the
// label, when it arrived, and when it last signed somebody in. Never the
// key itself.
type passkeyView struct {
	ID         uint       `json:"id"`
	Name       string     `json:"name"`
	Transports []string   `json:"transports"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

func passkeyViewOf(row model.PasskeyCredential) passkeyView {
	view := passkeyView{
		ID:         row.ID,
		Name:       row.Name,
		Transports: []string{},
		CreatedAt:  row.CreatedAt,
		LastUsedAt: row.LastUsedAt,
	}
	if row.Transports != "" {
		view.Transports = strings.Split(row.Transports, ",")
	}
	return view
}

// handlePasskeyList lists the signed-in operator's own passkeys.
func (s *Server) handlePasskeyList(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	if admin == nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if s.passkeys == nil {
		writeError(w, http.StatusNotFound, "this panel has no passkeys")
		return
	}
	rows, err := s.passkeys.List(r.Context(), admin.ID)
	if err != nil {
		fail(w, s.log, err)
		return
	}
	views := make([]passkeyView, 0, len(rows))
	for _, row := range rows {
		views = append(views, passkeyViewOf(row))
	}
	writeJSON(w, http.StatusOK, views)
}

// handlePasskeyDelete revokes one of the signed-in operator's own
// passkeys. Another operator's is answered as not found, because to this
// operator it may as well not exist.
func (s *Server) handlePasskeyDelete(w http.ResponseWriter, r *http.Request) {
	admin := adminFrom(r.Context())
	if admin == nil {
		writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if s.passkeys == nil {
		writeError(w, http.StatusNotFound, "this panel has no passkeys")
		return
	}
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, "no such passkey")
		return
	}
	deleted, err := s.passkeys.Delete(r.Context(), admin.ID, uint(id))
	if err != nil {
		fail(w, s.log, err)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "no such passkey")
		return
	}
	s.log.Info("passkey revoked", "username", admin.Username, "ip", clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// handlePasskeyLoginOptions begins a sign-in. No username is asked for:
// the credential the device offers names its owner, which is the point
// of a discoverable key -- and of the fingerprint prompt Ali asked for.
func (s *Server) handlePasskeyLoginOptions(w http.ResponseWriter, r *http.Request) {
	wa, ok := s.passkeyReady(w, r)
	if !ok {
		return
	}
	assertion, session, err := wa.BeginDiscoverableLogin(
		webauthn.WithUserVerification(protocol.VerificationRequired))
	if err != nil {
		fail(w, s.log, fmt.Errorf("begin passkey sign-in: %w", err))
		return
	}
	s.beginCeremony(&passkeyCeremony{session: session, login: true})
	writeJSON(w, http.StatusOK, assertion)
}

// handlePasskeyLogin is servePasskeyLogin with the attempt kept in the
// activity table, as the password door's is: a sign-in, and especially a
// failed one, is what an owner goes looking for after something has gone
// wrong.
func (s *Server) handlePasskeyLogin(w http.ResponseWriter, r *http.Request) {
	rec := &actionRecorder{statusRecorder: statusRecorder{ResponseWriter: w, status: http.StatusOK}}
	username := s.servePasskeyLogin(rec, r)
	if s.activity != nil {
		entry := model.ActivityEntry{
			Actor:  username,
			Action: "POST /api/passkeys/login/verify",
			Status: rec.status,
			IP:     clientIP(r),
		}
		if rec.status >= 400 {
			entry.Reason = rec.reason()
		}
		s.activity.Record(entry)
	}
}

// servePasskeyLogin finishes a passkey sign-in and answers with the same
// session the password door issues -- minted by the same code, so the
// binding and the epoch are asked of it in exactly the same way. It
// returns the name that was signed in under, for the activity record.
//
// A passkey with user verification is two proofs in one gesture -- the
// device is held, and the person is matched -- so it stands in for the
// password whole, second factor included. An operator the owner has
// switched off is refused, as at the password door, and only once the
// assertion has proved itself, so the refusal tells a guesser nothing.
func (s *Server) servePasskeyLogin(w http.ResponseWriter, r *http.Request) string {
	now := time.Now()
	ipKey := "ip:" + clientIP(r)
	if wait := s.throttle.retryAfter(ipKey, now); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, lockoutMessage(wait))
		return ""
	}

	wa, ok := s.passkeyReady(w, r)
	if !ok {
		return ""
	}
	challenge, body, err := ceremonyChallenge(r)
	if err != nil {
		s.throttle.fail(ipKey, now)
		writeError(w, http.StatusBadRequest, "that passkey answer is not readable")
		return ""
	}
	c := s.takeCeremony(challenge)
	if c == nil || !c.login {
		s.throttle.fail(ipKey, now)
		writeError(w, http.StatusUnauthorized,
			"that sign-in has expired; try again")
		return ""
	}

	// Who the assertion names, resolved while the library judges it: the
	// credential id says which key, the key says which operator. The
	// handle the device sends back must agree with the key's owner --
	// a key answering for somebody else is not a key out of place, it is
	// not this panel's key at all.
	var resolved *model.Admin
	var resolvedRow *model.PasskeyCredential
	handler := func(rawID, userHandle []byte) (webauthn.User, error) {
		row, err := s.passkeys.ByCredentialID(r.Context(), rawID)
		if err != nil {
			return nil, err
		}
		handleID, err := strconv.ParseUint(string(userHandle), 10, 64)
		if err != nil || uint(handleID) != row.AdminID {
			return nil, fault.ErrNotFound
		}
		admin, err := s.admins.ForSession(r.Context(), fmt.Sprint(row.AdminID))
		if err != nil {
			return nil, err
		}
		user, err := s.passkeyUserFor(r, admin)
		if err != nil {
			return nil, err
		}
		resolved, resolvedRow = admin, row
		return user, nil
	}

	r.Body = io.NopCloser(bytes.NewReader(body))
	_, cred, err := wa.FinishPasskeyLogin(handler, *c.session, r)
	if err != nil {
		s.throttle.fail(ipKey, now)
		s.log.Warn("failed passkey sign-in", "ip", clientIP(r), "error", err)
		writeError(w, http.StatusUnauthorized,
			"passkey sign-in failed; try again, or use your password")
		return ""
	}
	admin := resolved
	if admin == nil || resolvedRow == nil {
		// The library returned a user; the handler above is the only path
		// to one, and it sets both. Reaching here would mean that changed.
		fail(w, s.log, errors.New("passkey sign-in resolved nobody"))
		return ""
	}

	if refusal := signInRefusal(admin, time.Now().UTC()); refusal != "" {
		s.log.Warn("passkey sign-in refused: account switched off",
			"username", admin.Username, "ip", clientIP(r))
		writeError(w, http.StatusForbidden, refusal)
		return ""
	}

	// The counter the next assertion is compared against, and when this
	// key was last used. Neither failing stops the sign-in: the person
	// has proved who they are. A counter that went backwards is the one
	// thing worth shouting about -- it is how a cloned key announces
	// itself -- so it is logged where the owner will see it.
	if err := s.passkeys.Used(r.Context(), resolvedRow.ID,
		cred.Authenticator.SignCount, time.Now()); err != nil {
		s.log.Error("record passkey use", "username", admin.Username, "error", err)
	}
	if cred.Authenticator.CloneWarning {
		s.log.Warn("passkey counter went backwards; the authenticator may be cloned",
			"username", admin.Username, "ip", clientIP(r))
	}

	expires := time.Now().Add(s.sessionTTL(r.Context()))
	token, err := s.issueToken(w, r, admin, expires)
	if err != nil {
		fail(w, s.log, err)
		return ""
	}
	s.throttle.succeed(ipKey)

	ip := clientIP(r)
	s.admins.SignedIn(r.Context(), admin, ip, time.Now())
	s.log.Info("admin signed in with a passkey", "username", admin.Username, "ip", ip)
	if s.notifier != nil {
		s.notifier.Send(notify.Event{
			Kind:  notify.KindLogin,
			Title: "Panel sign-in",
			Body:  fmt.Sprintf("%s from %s (passkey)", admin.Username, ip),
		})
	}
	writeJSON(w, http.StatusOK, loginResponse{
		Token: token, ExpiresAt: expires, Admin: s.adminView(r.Context(), admin)})
	return admin.Username
}
