package shop

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// signInitData builds an initData payload the way Telegram does: the
// same check string and the same HMAC chain ValidateInitData checks,
// so a payload signed here is a payload Telegram would have signed
// for a bot with this token.
func signInitData(token string, fields map[string]string) string {
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var check strings.Builder
	for i, k := range keys {
		if i > 0 {
			check.WriteByte('\n')
		}
		check.WriteString(k + "=" + fields[k])
	}
	mac := hmac.New(sha256.New, []byte("WebAppData"))
	mac.Write([]byte(token))
	secret := mac.Sum(nil)
	mac = hmac.New(sha256.New, secret)
	mac.Write([]byte(check.String()))

	q := url.Values{}
	for k, v := range fields {
		q.Set(k, v)
	}
	q.Set("hash", hex.EncodeToString(mac.Sum(nil)))
	return q.Encode()
}

func miniAppFields(authDate time.Time, tgID int64, firstName string) map[string]string {
	user, _ := json.Marshal(map[string]any{"id": tgID, "first_name": firstName})
	return map[string]string{
		"auth_date":  strconv.FormatInt(authDate.Unix(), 10),
		"query_id":   "AAHdF6IQAAAAAN0XohDhrOrc",
		"user":       string(user),
		"chat_type":  "private",
		"start_param": "",
	}
}

func TestValidateInitDataAcceptsASignedPayload(t *testing.T) {
	now := time.Now().UTC()
	data := signInitData("123456:TEST-TOKEN", miniAppFields(now, 4242, "Buyer"))
	id, name, err := ValidateInitData("123456:TEST-TOKEN", data, 24*time.Hour, now)
	if err != nil {
		t.Fatalf("a signed payload was refused: %v", err)
	}
	if id != 4242 || name != "Buyer" {
		t.Errorf("the payload read back id %d name %q", id, name)
	}
}

func TestValidateInitDataRefusesATamperedUser(t *testing.T) {
	now := time.Now().UTC()
	data := signInitData("123456:TEST-TOKEN", miniAppFields(now, 4242, "Buyer"))
	// The signature was made over buyer 4242; a payload that names
	// somebody else under it must not pass.
	tampered := strings.Replace(data, "%224242%22", "%229999%22", 1)
	if tampered == data {
		user, _ := json.Marshal(map[string]any{"id": 9999, "first_name": "Buyer"})
		q, _ := url.ParseQuery(data)
		q.Set("user", string(user))
		tampered = q.Encode()
	}
	if _, _, err := ValidateInitData("123456:TEST-TOKEN", tampered, 24*time.Hour, now); err == nil {
		t.Error("a payload naming another user under the signature passed")
	}
}

func TestValidateInitDataRefusesAStalePayload(t *testing.T) {
	now := time.Now().UTC()
	data := signInitData("123456:TEST-TOKEN", miniAppFields(now.Add(-48*time.Hour), 4242, "Buyer"))
	if _, _, err := ValidateInitData("123456:TEST-TOKEN", data, 24*time.Hour, now); err == nil {
		t.Error("a two-day-old payload passed a one-day fence")
	}
}

func TestValidateInitDataRefusesTheWrongToken(t *testing.T) {
	now := time.Now().UTC()
	data := signInitData("123456:TEST-TOKEN", miniAppFields(now, 4242, "Buyer"))
	if _, _, err := ValidateInitData("999999:OTHER-TOKEN", data, 24*time.Hour, now); err == nil {
		t.Error("a payload signed for another bot passed")
	}
}

func TestValidateInitDataRefusesUnsignedAndEmpty(t *testing.T) {
	now := time.Now().UTC()
	for _, data := range []string{"", "user=%7B%22id%22%3A4242%7D", "hash=zz"} {
		if _, _, err := ValidateInitData("123456:TEST-TOKEN", data, 24*time.Hour, now); err == nil {
			t.Errorf("payload %q passed", data)
		}
	}
	if _, _, err := ValidateInitData("", signInitData("123456:TEST-TOKEN", miniAppFields(now, 1, "x")), 24*time.Hour, now); err == nil {
		t.Error("a shop with no token accepted a payload")
	}
}
