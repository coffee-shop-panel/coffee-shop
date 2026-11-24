package shop

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abolfazl/w-ui/internal/fault"
)

// The Telegram Mini App's proof of who is holding the phone.
//
// When a shop's customer opens the shop's page from inside Telegram,
// the client hands the page a string of signed fields — initData —
// and the page hands it back with every call it makes. The signature
// is Telegram's own scheme: an HMAC chain keyed by the bot's token,
// which only the bot's owner and Telegram hold. Checking it is what
// turns "a browser claiming to be this buyer" into "Telegram says
// this buyer", and it is pure — no database, no HTTP — so it can be
// tested against payloads signed the way Telegram signs them.

// ValidateInitData checks one initData string against a bot token
// and, when it holds, says who Telegram signed it for: the buyer's
// Telegram id and the first name the client showed for them.
//
// The check string is every field but the hash itself, keys sorted,
// one key=value per line; the secret is the token HMACed under the
// constant "WebAppData", and the hash is that secret HMACed over the
// check string. auth_date is fenced by maxAge: a signature is a fact
// about when it was made, and one replayed tomorrow must not still
// open the shop.
func ValidateInitData(botToken, initData string, maxAge time.Duration, now time.Time) (int64, string, error) {
	if strings.TrimSpace(botToken) == "" {
		return 0, "", fmt.Errorf("%w: this shop has no bot token to check a signature against", fault.ErrInvalid)
	}
	values, err := url.ParseQuery(initData)
	if err != nil {
		return 0, "", fmt.Errorf("%w: the mini app data could not be read", fault.ErrInvalid)
	}
	given := values.Get("hash")
	if given == "" {
		return 0, "", fmt.Errorf("%w: the mini app data carries no signature", fault.ErrInvalid)
	}
	userRaw := values.Get("user")
	if userRaw == "" {
		return 0, "", fmt.Errorf("%w: the mini app data names no user", fault.ErrInvalid)
	}
	authRaw := values.Get("auth_date")
	if authRaw == "" {
		return 0, "", fmt.Errorf("%w: the mini app data carries no signing time", fault.ErrInvalid)
	}

	keys := make([]string, 0, len(values))
	for k := range values {
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var check strings.Builder
	for i, k := range keys {
		if i > 0 {
			check.WriteByte('\n')
		}
		check.WriteString(k)
		check.WriteByte('=')
		check.WriteString(values.Get(k))
	}

	mac := hmac.New(sha256.New, []byte("WebAppData"))
	mac.Write([]byte(botToken))
	secret := mac.Sum(nil)
	mac = hmac.New(sha256.New, secret)
	mac.Write([]byte(check.String()))
	expected := mac.Sum(nil)
	got, err := hex.DecodeString(given)
	if err != nil || subtle.ConstantTimeCompare(got, expected) != 1 {
		return 0, "", fmt.Errorf("%w: the mini app signature does not hold", fault.ErrInvalid)
	}

	authUnix, err := strconv.ParseInt(authRaw, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("%w: the mini app signing time could not be read", fault.ErrInvalid)
	}
	authDate := time.Unix(authUnix, 0)
	if authDate.After(now.Add(time.Minute)) || now.Sub(authDate) > maxAge {
		return 0, "", fmt.Errorf("%w: the mini app data is too old; open the shop again", fault.ErrInvalid)
	}

	var user struct {
		ID        int64  `json:"id"`
		FirstName string `json:"first_name"`
	}
	if err := json.Unmarshal([]byte(userRaw), &user); err != nil || user.ID == 0 {
		return 0, "", fmt.Errorf("%w: the mini app data names no user", fault.ErrInvalid)
	}
	return user.ID, user.FirstName, nil
}
