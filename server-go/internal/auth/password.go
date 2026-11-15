package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (OWASP recommended)
const (
	argon2Time    = 3
	argon2Memory  = 64 * 1024 // 64 MB
	argon2Threads = 4
	argon2KeyLen  = 32
	argon2SaltLen = 16
)

// HashPassword hashes a password using argon2id.
// It waits for a slot in the shared password gate and returns ErrBusy when
// none frees up in time.
func HashPassword(password string) (string, error) {
	var hash string
	var err error
	if gateErr := currentGate().run(func() { hash, err = hashArgon2id(password) }); gateErr != nil {
		return "", gateErr
	}
	return hash, err
}

func hashArgon2id(password string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argon2Memory, argon2Time, argon2Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword checks a password against an argon2id hash inside the shared
// password gate. client is the caller's address (IPv6 grouped by /64): one
// client may hold only a share of the gate, whether or not the account exists.
// It returns ErrBusy when no slot frees up in time; a hash that is empty or
// not argon2id costs nothing and is simply false.
func VerifyPassword(password, hash, client string) (bool, error) {
	if hash == "" || !strings.HasPrefix(hash, "$argon2id$") {
		return false, nil
	}
	var ok bool
	if err := currentGate().runFor(client, func() { ok = checkArgon2id(password, hash) }); err != nil {
		return false, err
	}
	return ok, nil
}

// CheckPassword is VerifyPassword for callers that cannot act on a refusal: a
// busy gate reads as a mismatch. Request handlers use VerifyPassword.
func CheckPassword(password, hash string) bool {
	ok, _ := VerifyPassword(password, hash, "")
	return ok
}

func checkArgon2id(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	// $argon2id$v=N$m=N,t=N,p=N$salt$key → 6 parts (first is empty)
	if len(parts) != 6 {
		return false
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false
	}

	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}

	expectedKey, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}

	key := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(expectedKey)))
	return subtle.ConstantTimeCompare(key, expectedKey) == 1
}
