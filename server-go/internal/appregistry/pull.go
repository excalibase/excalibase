package appregistry

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"strconv"
)

// PullCredential is what a project's pull secret carries to the token endpoint.
type PullCredential struct {
	Username string
	Password string
}

// PullCredential derives the project's password from the signing key and its
// generation: the same on every replica, only the generation stored; a new
// generation retires one project's password, a new key retires them all.
func (i *Issuer) PullCredential(projectID string, generation int) PullCredential {
	return PullCredential{Username: projectID, Password: base64.RawURLEncoding.EncodeToString(i.pullMAC(projectID, generation))}
}

// VerifyPull checks password against the project's current generation in constant time.
func (i *Issuer) VerifyPull(projectID string, generation int, password string) bool {
	given, err := base64.RawURLEncoding.DecodeString(password)
	return err == nil && password != "" && hmac.Equal(given, i.pullMAC(projectID, generation))
}

func (i *Issuer) pullMAC(projectID string, generation int) []byte {
	mac := hmac.New(sha256.New, i.pullKey)
	mac.Write([]byte("registry pull\x00" + projectID + "\x00" + strconv.Itoa(generation)))
	return mac.Sum(nil)
}

// derivePullKey keeps pull passwords apart from signatures: an HMAC key bound to the private key.
func derivePullKey(der []byte) []byte {
	mac := hmac.New(sha256.New, der)
	mac.Write([]byte("excalibase registry pull key v1"))
	return mac.Sum(nil)
}

func privateKeyDER(key any) ([]byte, error) {
	return x509.MarshalPKCS8PrivateKey(key)
}
