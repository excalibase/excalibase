package objectcreds

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	r2HostSuffix = ".r2.cloudflarestorage.com"
	clockSkew    = time.Minute
)

// R2Signer mints Cloudflare R2 temporary credentials by local signing: an
// HS256 JWT signed with the parent secret is the session token, its SHA-256
// is the secret, and the parent's key id is reused. R2 validates the JWT on
// every request, so the credential is bound to the bucket, scope, prefix and
// expiry it names. See developers.cloudflare.com/r2/api/s3/temporary-credentials.
type R2Signer struct {
	Now func() time.Time
}

type r2Claims struct {
	Bucket string  `json:"bucket"`
	Scope  Access  `json:"scope"`
	Paths  r2Paths `json:"paths"`
	Sub    string  `json:"sub"`
	Iss    string  `json:"iss"`
	Aud    string  `json:"aud"`
	Iat    int64   `json:"iat"`
	Exp    int64   `json:"exp"`
}

type r2Paths struct {
	PrefixPaths []string `json:"prefixPaths"`
	ObjectPaths []string `json:"objectPaths"`
}

// Mint signs a credential for scope. It makes no network call.
func (s R2Signer) Mint(_ context.Context, parent Parent, scope Scope) (Credentials, error) {
	if err := validate(parent, scope, time.Second); err != nil {
		return Credentials{}, err
	}
	host, account, err := r2Account(parent.Endpoint)
	if err != nil {
		return Credentials{}, err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	issued := now().UTC().Truncate(time.Second)
	expires := issued.Add(scope.TTL)
	// Backdated so a platform clock slightly ahead of R2's is not refused.
	claims := r2Claims{
		Bucket: parent.Bucket,
		Scope:  scope.Access,
		Paths:  r2Paths{PrefixPaths: []string{scope.Prefix}, ObjectPaths: []string{}},
		Sub:    account,
		Iss:    parent.AccessKeyID,
		Aud:    host,
		Iat:    issued.Add(-clockSkew).Unix(),
		Exp:    expires.Unix(),
	}
	token, err := signHS256(claims, parent.SecretAccessKey)
	if err != nil {
		return Credentials{}, err
	}
	digest := sha256.Sum256([]byte(token))
	return Credentials{
		AccessKeyID:     parent.AccessKeyID,
		SecretAccessKey: hex.EncodeToString(digest[:]),
		SessionToken:    base64.StdEncoding.EncodeToString([]byte("jwt/" + token)),
		ExpiresAt:       expires,
	}, nil
}

// r2Account reads the account id from an R2 S3 endpoint
// (https://<account>.r2.cloudflarestorage.com).
func r2Account(endpoint string) (host, account string, err error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" {
		return "", "", fmt.Errorf("%w: endpoint %q is not an https R2 endpoint", ErrInvalidRequest, endpoint)
	}
	host = parsed.Hostname()
	account, ok := strings.CutSuffix(host, r2HostSuffix)
	if !ok || account == "" || strings.Contains(account, ".") {
		return "", "", fmt.Errorf("%w: endpoint %q is not an R2 account endpoint", ErrInvalidRequest, endpoint)
	}
	return host, account, nil
}

func signHS256(claims r2Claims, secret string) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
