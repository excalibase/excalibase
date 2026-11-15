// Package adminaccess fences the operator console (EXC-757). The console is
// served on its own hostname behind Cloudflare Access; the origin cannot
// trust the Host header alone, because anyone can send it straight to the
// origin's address. So every admin-host request must also carry the signed
// assertion Access adds, which only Cloudflare can mint.
package adminaccess

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	certsPath      = "/cdn-cgi/access/certs"
	fetchTimeout   = 5 * time.Second
	maxCertsBody   = 1 << 20
	keyLifetime    = time.Hour
	refetchBackoff = time.Minute
	clockLeeway    = 30 * time.Second
)

// Config names the Access application the assertion must be minted for.
type Config struct {
	// TeamDomain is <team>.cloudflareaccess.com: where the signing keys are
	// published and what the assertion's issuer must be.
	TeamDomain string
	// Audience is the Access application's AUD tag.
	Audience   string
	HTTPClient *http.Client
}

// Verifier checks Cloudflare Access assertions.
type Verifier struct {
	cfg        Config
	minRefetch time.Duration

	mu        sync.Mutex
	keys      map[string]*rsa.PublicKey
	fetchedAt time.Time
}

func NewVerifier(cfg Config) *Verifier {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: fetchTimeout}
	}
	return &Verifier{cfg: cfg, minRefetch: refetchBackoff}
}

// Verify accepts a Cf-Access-Jwt-Assertion value only when it is RS256-signed
// by a key Cloudflare publishes for the team, names this application in its
// audience, was issued by the team, and is inside its validity window.
func (v *Verifier) Verify(assertion string) error {
	if assertion == "" {
		return errors.New("no assertion")
	}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithAudience(v.cfg.Audience),
		jwt.WithIssuer("https://"+v.cfg.TeamDomain),
		jwt.WithExpirationRequired(),
		jwt.WithLeeway(clockLeeway),
	)
	_, err := parser.Parse(assertion, v.keyFor)
	return err
}

func (v *Verifier) keyFor(token *jwt.Token) (any, error) {
	kid, _ := token.Header["kid"].(string)
	if kid == "" {
		return nil, errors.New("assertion names no key")
	}
	key, err := v.lookup(kid)
	if err != nil {
		return nil, err
	}
	return key, nil
}

// lookup returns the named key. A key not in the cache triggers one refetch,
// at most once per backoff, so a flood of made-up key ids cannot turn into a
// flood of requests to Cloudflare.
func (v *Verifier) lookup(kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	stale := time.Since(v.fetchedAt) > keyLifetime
	if key, ok := v.keys[kid]; ok && !stale {
		return key, nil
	}
	if v.keys == nil || stale || time.Since(v.fetchedAt) >= v.minRefetch {
		if err := v.refreshLocked(); err != nil {
			if v.keys == nil {
				return nil, fmt.Errorf("read the Access signing keys: %w", err)
			}
			log.Printf("WARN: refresh Access signing keys: %v (using the cached ones)", err)
		}
	}
	if key, ok := v.keys[kid]; ok {
		return key, nil
	}
	return nil, errors.New("assertion is signed by an unknown key")
}

func (v *Verifier) refreshLocked() error {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+v.cfg.TeamDomain+certsPath, nil)
	if err != nil {
		return err
	}
	resp, err := v.cfg.HTTPClient.Do(req)
	if err != nil {
		v.fetchedAt = time.Now()
		return err
	}
	defer resp.Body.Close()
	v.fetchedAt = time.Now()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("keys endpoint answered %d", resp.StatusCode)
	}
	keys, err := parseKeys(io.LimitReader(resp.Body, maxCertsBody))
	if err != nil {
		return err
	}
	v.keys = keys
	return nil
}

type jwk struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func parseKeys(body io.Reader) (map[string]*rsa.PublicKey, error) {
	var set struct {
		Keys []jwk `json:"keys"`
	}
	if err := json.NewDecoder(body).Decode(&set); err != nil {
		return nil, fmt.Errorf("decode keys: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, entry := range set.Keys {
		if entry.Kty != "RSA" || entry.Kid == "" {
			continue
		}
		modulus, errN := base64.RawURLEncoding.DecodeString(entry.N)
		exponent, errE := base64.RawURLEncoding.DecodeString(entry.E)
		if errN != nil || errE != nil {
			continue
		}
		keys[entry.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(new(big.Int).SetBytes(exponent).Int64())}
	}
	if len(keys) == 0 {
		return nil, errors.New("the keys endpoint published no usable key")
	}
	return keys, nil
}
