package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // RFC 6238 default; HMAC-SHA1 is not affected by SHA-1 collisions and is what every authenticator app supports
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	totpPeriod    = 30
	totpDigits    = 6
	totpSecretLen = 20 // 160 bits, as recommended by RFC 4226
	totpSkew      = 1  // accept the previous and the next 30-second step (clock drift)
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a random secret and its base32 form (what the user types or scans).
func NewTOTPSecret() ([]byte, string, error) {
	s := make([]byte, totpSecretLen)
	if _, err := rand.Read(s); err != nil {
		return nil, "", err
	}
	return s, b32.EncodeToString(s), nil
}

// totpCode computes the RFC 6238 code for a time step.
func totpCode(secret []byte, step int64) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1_000_000)
}

// verifyTOTP checks code against the steps around now and returns the matched step. A step at or before
// lastStep is refused, so a code can be used only once (RFC 6238 section 5.2).
func verifyTOTP(secret []byte, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	cur := now.Unix() / totpPeriod
	matched, found := int64(0), 0
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		step := cur + d
		// Compare every candidate in constant time and do not stop early.
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, step)), []byte(code)) == 1 && step > lastStep {
			matched, found = step, 1
		}
	}
	return matched, found == 1
}

// TOTPURI builds the otpauth:// URI that authenticator apps import (also as a QR code).
func TOTPURI(issuer, account, secretB32 string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secretB32)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	return "otpauth://totp/" + label + "?" + q.Encode()
}
