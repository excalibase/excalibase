// Package pgscram makes the SCRAM-SHA-256 verifier Postgres stores for a
// password, so a password can be set without its plaintext ever reaching the
// database: CREATE/ALTER ROLE ... PASSWORD takes the verifier as-is.
package pgscram

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/xdg-go/stringprep"
)

const (
	// Iterations is Postgres' default scram_iterations.
	Iterations = 4096
	saltLength = 16
)

// Verifier salts with crypto/rand, which does not fail (it crashes the
// program rather than return an error since Go 1.24).
func Verifier(password string) (string, error) {
	salt := make([]byte, saltLength)
	rand.Read(salt)
	return VerifierWithSalt(password, salt)
}

// VerifierWithSalt is the value Postgres stores for password (RFC 5802/7677),
// in its own format. Like Postgres, it runs SASLprep first and keeps the raw
// password when SASLprep refuses it.
func VerifierWithSalt(password string, salt []byte) (string, error) {
	salted, err := pbkdf2.Key(sha256.New, prepare(password), salt, Iterations, sha256.Size)
	if err != nil {
		return "", fmt.Errorf("derive SCRAM key: %w", err)
	}
	clientKey := hmacSHA256(salted, "Client Key")
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(salted, "Server Key")
	encode := base64.StdEncoding.EncodeToString
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", Iterations, encode(salt), encode(storedKey[:]), encode(serverKey)), nil
}

// prepare mirrors pg_saslprep: plain ASCII is left alone, anything else is
// SASLprepped, and a password SASLprep refuses is used as it is.
func prepare(password string) string {
	if isASCII(password) {
		return password
	}
	prepared, err := stringprep.SASLprep.Prepare(password)
	if err != nil || prepared == "" {
		return password
	}
	return prepared
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func hmacSHA256(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return mac.Sum(nil)
}

// Matches reports whether stored, a verifier in Postgres' format, was made
// from password. Anything that is not such a verifier matches nothing, and so
// does a verifier with any iteration count but Iterations: the count is chosen
// by whoever set the password, so it is never fed to PBKDF2 (EXC-821).
func Matches(stored, password string) bool {
	salt, storedKey, serverKey, ok := parseVerifier(stored)
	if !ok {
		return false
	}
	salted, err := pbkdf2.Key(sha256.New, prepare(password), salt, Iterations, sha256.Size)
	if err != nil {
		return false
	}
	clientKey := hmacSHA256(salted, "Client Key")
	gotStored := sha256.Sum256(clientKey)
	gotServer := hmacSHA256(salted, "Server Key")
	// Both keys are compared, without short-circuiting.
	storedOK := subtle.ConstantTimeCompare(gotStored[:], storedKey)
	serverOK := subtle.ConstantTimeCompare(gotServer, serverKey)
	return storedOK&serverOK == 1
}

// parseVerifier splits SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>
// and accepts it only with the platform's iteration count.
func parseVerifier(stored string) (salt, storedKey, serverKey []byte, ok bool) {
	rest, found := strings.CutPrefix(stored, "SCRAM-SHA-256$")
	if !found {
		return nil, nil, nil, false
	}
	params, keys, found := strings.Cut(rest, "$")
	if !found {
		return nil, nil, nil, false
	}
	iterations, saltText, found := strings.Cut(params, ":")
	if !found {
		return nil, nil, nil, false
	}
	if count, err := strconv.Atoi(iterations); err != nil || count != Iterations {
		return nil, nil, nil, false
	}
	storedText, serverText, found := strings.Cut(keys, ":")
	if !found {
		return nil, nil, nil, false
	}
	decode := base64.StdEncoding.DecodeString
	var errs [3]error
	salt, errs[0] = decode(saltText)
	storedKey, errs[1] = decode(storedText)
	serverKey, errs[2] = decode(serverText)
	return salt, storedKey, serverKey, errs[0] == nil && errs[1] == nil && errs[2] == nil
}
