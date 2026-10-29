package appregistry

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
)

// PullCredential is what a project's pull secret carries to the token endpoint.
type PullCredential struct {
	Username string
	Password string
}

// PullCredential derives the project's password from the signing key: the
// same on every replica, nothing stored, and a new key replaces them all.
func (i *Issuer) PullCredential(projectID string) PullCredential {
	return PullCredential{Username: projectID, Password: base64.RawURLEncoding.EncodeToString(i.pullMAC(projectID))}
}

// VerifyPull checks password in constant time.
func (i *Issuer) VerifyPull(projectID, password string) bool {
	given, err := base64.RawURLEncoding.DecodeString(password)
	return err == nil && password != "" && hmac.Equal(given, i.pullMAC(projectID))
}

func (i *Issuer) pullMAC(projectID string) []byte {
	mac := hmac.New(sha256.New, i.pullKey)
	mac.Write([]byte("registry pull\x00" + projectID))
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
