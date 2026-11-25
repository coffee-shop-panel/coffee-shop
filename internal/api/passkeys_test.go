package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/abolfazl/w-ui/internal/database/model"
	"github.com/abolfazl/w-ui/internal/operators"
	"github.com/abolfazl/w-ui/internal/service"
)

// Passkeys: the ceremonies' shape, their single use, who a stored key is
// visible to, and the door's refusal to work unencrypted. What no test
// here can do is press a finger to a sensor; the ceremonies' cryptographic
// half is exercised with a real authenticator in the browser test, and
// what is checked here is everything around it.

const passkeyHost = "https://panel.example.test"

func passkeyServer(t *testing.T) (*Server, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(model.AllModels()...); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &Server{
		log:       log,
		throttle:  newThrottle(),
		jwtSecret: []byte("a test session key, 32 bytes!!!"),
		admins:    operators.NewAdmins(db, log),
		passkeys:  operators.NewPasskeys(db, log),
		activity:  service.NewActivity(db, log),
	}, db
}

// tlsRequest builds a request as it arrives from the proxy on an
// encrypted panel: an https address, so the scheme test sees TLS.
func tlsRequest(method, path string, body []byte) *http.Request {
	var r *http.Request
	if body == nil {
		r = httptest.NewRequest(method, passkeyHost+path, nil)
	} else {
		r = httptest.NewRequest(method, passkeyHost+path, bytes.NewReader(body))
	}
	r.RemoteAddr = "203.0.113.9:40000"
	return r
}

// ceremonyAnswer builds the body a browser sends back from a ceremony:
// the challenge, wrapped the way the authenticator wraps it.
func ceremonyAnswer(challenge string) []byte {
	clientData, _ := json.Marshal(map[string]string{
		"type":      "webauthn.get",
		"challenge": challenge,
		"origin":    "https://panel.example.test",
	})
	answer, _ := json.Marshal(map[string]any{
		"id":    "a-credential",
		"rawId": "a-credential",
		"type":  "public-key",
		"response": map[string]string{
			"clientDataJSON": base64.RawURLEncoding.EncodeToString(clientData),
		},
	})
	return answer
}

func TestPasskeyRegistrationOptionsAreWellFormed(t *testing.T) {
	s, db := passkeyServer(t)
	owner := admin(t, db, model.Admin{Username: "owner"}, "a long password")

	body, _ := json.Marshal(map[string]string{"name": "My phone"})
	r := asAdmin(owner, http.MethodPost, passkeyHost+"/api/passkeys/register/options", nil)
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.RemoteAddr = "203.0.113.9:40000"
	w := httptest.NewRecorder()
	s.handlePasskeyRegisterOptions(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("options: %d %s", w.Code, w.Body)
	}
	var creation struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			RP        struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"rp"`
			User struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"user"`
			AuthenticatorSelection struct {
				ResidentKey      string `json:"residentKey"`
				UserVerification string `json:"userVerification"`
			} `json:"authenticatorSelection"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &creation); err != nil {
		t.Fatalf("options are not the creation shape: %v", err)
	}
	pk := creation.PublicKey
	if pk.Challenge == "" {
		t.Error("no challenge was issued")
	}
	if pk.RP.ID != "panel.example.test" {
		t.Errorf("the relying party is %q, not the request's host without its port", pk.RP.ID)
	}
	if pk.User.Name != "owner" {
		t.Errorf("the ceremony names %q, not the operator", pk.User.Name)
	}
	if pk.AuthenticatorSelection.ResidentKey != "required" {
		t.Errorf("the key is not asked to be discoverable: %q", pk.AuthenticatorSelection.ResidentKey)
	}
	if pk.AuthenticatorSelection.UserVerification != "required" {
		t.Errorf("user verification is not required: %q", pk.AuthenticatorSelection.UserVerification)
	}
	// The challenge is being held for its verify step.
	if s.takeCeremony(pk.Challenge) == nil {
		t.Error("the issued challenge was not kept")
	}
}

func TestPasskeyCeremoniesRefusePlainHTTP(t *testing.T) {
	s, db := passkeyServer(t)
	owner := admin(t, db, model.Admin{Username: "owner"}, "a long password")

	r := asAdmin(owner, http.MethodPost, "/api/passkeys/register/options", nil)
	r.RemoteAddr = "203.0.113.9:40000"
	w := httptest.NewRecorder()
	s.handlePasskeyRegisterOptions(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "HTTPS") {
		t.Errorf("registration over HTTP: %d %s", w.Code, w.Body)
	}

	w = httptest.NewRecorder()
	s.handlePasskeyLoginOptions(w, tlsRequest(http.MethodPost, "/api/passkeys/login/options", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("login options over HTTPS: %d %s", w.Code, w.Body)
	}
	r = httptest.NewRequest(http.MethodPost, "/api/passkeys/login/options", nil)
	r.RemoteAddr = "203.0.113.9:40000"
	w = httptest.NewRecorder()
	s.handlePasskeyLoginOptions(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "HTTPS") {
		t.Errorf("login over HTTP: %d %s", w.Code, w.Body)
	}
}

func TestPasskeyLoginOptionsAreDiscoverable(t *testing.T) {
	s, _ := passkeyServer(t)
	w := httptest.NewRecorder()
	s.handlePasskeyLoginOptions(w, tlsRequest(http.MethodPost, "/api/passkeys/login/options", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("options: %d %s", w.Code, w.Body)
	}
	var assertion struct {
		PublicKey struct {
			Challenge        string `json:"challenge"`
			AllowCredentials []any  `json:"allowCredentials"`
			UserVerification string `json:"userVerification"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &assertion); err != nil {
		t.Fatal(err)
	}
	if assertion.PublicKey.Challenge == "" {
		t.Error("no challenge was issued")
	}
	if len(assertion.PublicKey.AllowCredentials) != 0 {
		t.Error("a discoverable sign-in names no credentials in advance")
	}
	if assertion.PublicKey.UserVerification != "required" {
		t.Errorf("user verification is %q, not required", assertion.PublicKey.UserVerification)
	}
}

func TestPasskeyVerifyRejectsGarbageAndSpentChallenges(t *testing.T) {
	s, db := passkeyServer(t)
	owner := admin(t, db, model.Admin{Username: "owner"}, "a long password")

	// Begin a registration, then answer it with nonsense: refused.
	body, _ := json.Marshal(map[string]string{"name": "My phone"})
	r := asAdmin(owner, http.MethodPost, passkeyHost+"/api/passkeys/register/options", nil)
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.RemoteAddr = "203.0.113.9:40000"
	w := httptest.NewRecorder()
	s.handlePasskeyRegisterOptions(w, r)
	var creation struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &creation); err != nil {
		t.Fatal(err)
	}
	challenge := creation.PublicKey.Challenge

	answer := ceremonyAnswer(challenge)
	r = asAdmin(owner, http.MethodPost, passkeyHost+"/api/passkeys/register/verify", nil)
	r.Body = io.NopCloser(bytes.NewReader(answer))
	r.RemoteAddr = "203.0.113.9:40000"
	w = httptest.NewRecorder()
	s.handlePasskeyRegisterVerify(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("a garbage answer was accepted: %d %s", w.Code, w.Body)
	}

	// The same challenge again: it was spent by the first answer, failed
	// or not, so there is nothing left to answer.
	r = asAdmin(owner, http.MethodPost, passkeyHost+"/api/passkeys/register/verify", nil)
	r.Body = io.NopCloser(bytes.NewReader(answer))
	r.RemoteAddr = "203.0.113.9:40000"
	w = httptest.NewRecorder()
	s.handlePasskeyRegisterVerify(w, r)
	if w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "expired") {
		t.Errorf("a spent challenge was answered again: %d %s", w.Code, w.Body)
	}

	// A challenge nobody issued: refused the same way.
	r = asAdmin(owner, http.MethodPost, passkeyHost+"/api/passkeys/register/verify", nil)
	r.Body = io.NopCloser(bytes.NewReader(ceremonyAnswer("never-issued")))
	r.RemoteAddr = "203.0.113.9:40000"
	w = httptest.NewRecorder()
	s.handlePasskeyRegisterVerify(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("an unknown challenge was accepted: %d %s", w.Code, w.Body)
	}

	// Nothing was stored through any of it.
	rows, err := s.passkeys.List(t.Context(), owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("refused ceremonies stored %d passkeys", len(rows))
	}
}

func TestPasskeyListsAndRevokesAreScopedToTheirOwner(t *testing.T) {
	s, db := passkeyServer(t)
	owner := admin(t, db, model.Admin{Username: "owner"}, "a long password")
	other := admin(t, db, model.Admin{Username: "reseller", Role: model.RoleReseller}, "a long password")

	mine := &model.PasskeyCredential{
		AdminID: owner.ID, CredentialID: []byte("owner-credential"),
		PublicKey: []byte("a public key"), Name: "My phone",
	}
	if err := s.passkeys.Create(t.Context(), mine); err != nil {
		t.Fatal(err)
	}

	// The owner sees their key.
	r := asAdmin(owner, http.MethodGet, "/api/passkeys", nil)
	w := httptest.NewRecorder()
	s.handlePasskeyList(w, r)
	var list []passkeyView
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("the owner's list: %d %s", w.Code, w.Body)
	}
	if list[0].Name != "My phone" || list[0].ID != mine.ID {
		t.Errorf("the list names %+v", list[0])
	}

	// The other operator sees none of it.
	r = asAdmin(other, http.MethodGet, "/api/passkeys", nil)
	w = httptest.NewRecorder()
	s.handlePasskeyList(w, r)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 0 {
		t.Errorf("another operator's list: %d %s", w.Code, w.Body)
	}

	// And cannot revoke it: the answer is the one a missing key gets.
	r = asAdmin(other, http.MethodDelete, "/api/passkeys/1", nil)
	r.SetPathValue("id", "1")
	w = httptest.NewRecorder()
	s.handlePasskeyDelete(w, r)
	if w.Code != http.StatusNotFound {
		t.Errorf("revoking another's passkey: %d %s", w.Code, w.Body)
	}
	rows, err := s.passkeys.List(t.Context(), owner.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("the key went somewhere: %v %d", err, len(rows))
	}

	// The owner can.
	r = asAdmin(owner, http.MethodDelete, "/api/passkeys/1", nil)
	r.SetPathValue("id", "1")
	w = httptest.NewRecorder()
	s.handlePasskeyDelete(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("revoking their own passkey: %d %s", w.Code, w.Body)
	}
	rows, err = s.passkeys.List(t.Context(), owner.ID)
	if err != nil || len(rows) != 0 {
		t.Errorf("the key survived its revocation: %v %d", err, len(rows))
	}
}

func TestPasskeyLoginRefusesAGarbageAssertion(t *testing.T) {
	s, db := passkeyServer(t)
	owner := admin(t, db, model.Admin{Username: "owner"}, "a long password")
	if err := s.passkeys.Create(t.Context(), &model.PasskeyCredential{
		AdminID: owner.ID, CredentialID: []byte("owner-credential"),
		PublicKey: []byte("a public key"), Name: "My phone",
	}); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	s.handlePasskeyLoginOptions(w, tlsRequest(http.MethodPost, "/api/passkeys/login/options", nil))
	var assertion struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
		} `json:"publicKey"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &assertion); err != nil {
		t.Fatal(err)
	}

	w = httptest.NewRecorder()
	s.handlePasskeyLogin(w, tlsRequest(http.MethodPost, "/api/passkeys/login/verify",
		ceremonyAnswer(assertion.PublicKey.Challenge)))
	if w.Code == http.StatusOK {
		t.Fatalf("a garbage assertion signed in: %s", w.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err == nil && out["token"] != nil {
		t.Fatal("a garbage assertion was answered with a token")
	}

	// The attempt is in the activity record, as a failed sign-in at the
	// password door would be.
	entries, err := s.activity.List(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == "POST /api/passkeys/login/verify" && e.Status >= 400 {
			found = true
		}
	}
	if !found {
		t.Error("the failed passkey sign-in was not recorded")
	}

	// And the challenge is spent: the same answer cannot be replayed.
	w = httptest.NewRecorder()
	s.handlePasskeyLogin(w, tlsRequest(http.MethodPost, "/api/passkeys/login/verify",
		ceremonyAnswer(assertion.PublicKey.Challenge)))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("a replayed answer: %d %s", w.Code, w.Body)
	}
}

// A switched-off operator is refused at the password door by
// signInRefusal; the passkey door asks the same question of the same
// function before it mints anything. The full path -- a valid assertion
// from a switched-off operator -- needs a real authenticator and is
// covered in the browser test; what is pinned here is that both doors
// share the refusal.
func TestPasskeyDoorSharesTheSwitchedOffRefusal(t *testing.T) {
	off := &model.Admin{Username: "off", Role: model.RoleReseller, Enabled: false}
	if refusal := signInRefusal(off, time.Now().UTC()); refusal == "" {
		t.Fatal("a switched-off operator draws no refusal to share")
	}
	on := &model.Admin{Username: "on", Role: model.RoleReseller, Enabled: true}
	if refusal := signInRefusal(on, time.Now().UTC()); refusal != "" {
		t.Fatalf("an enabled operator was refused: %s", refusal)
	}
}
