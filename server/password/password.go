// Package password hashes and checks the passwords people sign in with.
//
// argon2id at the parameters OWASP recommends for it (19 MiB, two passes, one
// lane), written in the PHC string format so the parameters travel with the
// hash and can be raised later without breaking the hashes already stored.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	memory  = 19 * 1024
	passes  = 2
	lanes   = 1
	keyLen  = 32
	saltLen = 16
)

// MinLen is the shortest password accepted.
const MinLen = 8

// ErrWeak is a password too short to keep.
var ErrWeak = fmt.Errorf("a password is at least %d characters", MinLen)

// Hash answers what is stored for `pw`.
func Hash(pw string) (string, error) {
	if len([]rune(pw)) < MinLen {
		return "", ErrWeak
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, passes, memory, lanes, keyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memory, passes, lanes, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// Check answers whether `pw` is the password `stored` was hashed from.
func Check(stored, pw string) (bool, error) {
	parts := strings.Split(stored, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("password: not an argon2id hash")
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, fmt.Errorf("password: parameters: %w", err)
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// Make answers a password nobody chose: four groups of letters and digits a
// person can read aloud.
func Make() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	b := make([]byte, 16)
	rand.Read(b)
	out := strings.Builder{}
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			out.WriteByte('-')
		}
		out.WriteByte(alphabet[int(c)%len(alphabet)])
	}
	return out.String()
}
