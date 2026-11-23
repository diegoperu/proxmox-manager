// Package auth holds the accounts of the manager (voce 20): password hashing (argon2id), TOTP, the
// encryption of TOTP secrets at rest, sessions, login with lockout and the first-start setup code.
// Authorization (which role may call which route) lives in the HTTP layer and uses Role from here.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// argon2id parameters (RFC 9106 second recommended option scaled down for a small team server):
// 64 MiB, 3 passes, 1 lane, 16-byte salt, 32-byte key. Stored in PHC string format so they can change later.
const (
	argonMemory  = 64 * 1024
	argonTime    = 3
	argonThreads = 1
	argonSaltLen = 16
	argonKeyLen  = 32

	PasswordMin = 12
	// PasswordMaxBytes bounds the work an attacker can force on argon2 with huge inputs.
	PasswordMaxBytes = 256
)

var ErrWeakPassword = fmt.Errorf("la password deve avere da %d a %d caratteri, senza caratteri di controllo", PasswordMin, PasswordMaxBytes)

// ValidPassword checks the policy: length 12..256 bytes, valid UTF-8, no control characters.
// Length beats composition rules (NIST SP 800-63B); no maximum below 64 characters.
func ValidPassword(p string) error {
	if len(p) > PasswordMaxBytes || utf8.RuneCountInString(p) < PasswordMin || !utf8.ValidString(p) {
		return ErrWeakPassword
	}
	for _, r := range p {
		if unicode.IsControl(r) {
			return ErrWeakPassword
		}
	}
	return nil
}

// HashPassword returns a PHC-format argon2id hash with a random salt.
func HashPassword(p string) (string, error) {
	if err := ValidPassword(p); err != nil {
		return "", err
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(p), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

var errBadHash = errors.New("hash della password non valido")

// VerifyPassword compares p with a stored hash in constant time. The parameters come from the hash, within
// bounds, so a tampered database row cannot make verification allocate gigabytes.
func VerifyPassword(p, stored string) (bool, error) {
	if len(p) > PasswordMaxBytes {
		return false, nil
	}
	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errBadHash
	}
	var m, t uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &threads); err != nil ||
		m < 8*1024 || m > 1024*1024 || t < 1 || t > 10 || threads < 1 || threads > 8 {
		return false, errBadHash
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return false, errBadHash
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false, errBadHash
	}
	got := argon2.IDKey([]byte(p), salt, t, m, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is verified when a login names an unknown user, so that the response time does not tell
// which usernames exist.
var dummyHash = func() string {
	h, err := HashPassword("dummy-password-never-valid")
	if err != nil {
		panic(err)
	}
	return h
}()

// RandomPassword returns a 24-character password from a 56-symbol alphabet without look-alike characters
// (about 139 bits), used by the command line when it creates or resets an account. Rejection sampling keeps
// every symbol equally likely.
func RandomPassword() (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	const limit = 256 - 256%len(alphabet) // bytes at or above limit would bias the first symbols
	out := make([]byte, 0, 24)
	buf := make([]byte, 32)
	for len(out) < cap(out) {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if int(b) < limit && len(out) < cap(out) {
				out = append(out, alphabet[int(b)%len(alphabet)])
			}
		}
	}
	return string(out), nil
}
