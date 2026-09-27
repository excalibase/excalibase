// Package objectcreds mints short-lived object-store credentials confined to
// one bucket and one key prefix. The platform's own key (the parent) signs or
// requests them and never leaves the platform: only what Mint returns is ever
// written into a tenant namespace (EXC-476).
package objectcreds

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Access is what a minted credential may do under its prefix.
type Access string

const (
	// ReadWrite reads, lists, writes and deletes objects under the prefix.
	ReadWrite Access = "object-read-write"
	// ReadOnly reads and lists objects under the prefix.
	ReadOnly Access = "object-read-only"
)

// Provider names accepted by NewMinter.
const (
	// ProviderR2 signs Cloudflare R2 temporary credentials locally with the
	// parent key; nothing is created on the Cloudflare account.
	ProviderR2 = "r2"
	// ProviderSTS asks an S3-compatible STS endpoint (MinIO) for an AssumeRole
	// session narrowed by an inline policy.
	ProviderSTS = "sts"
)

// MaxTTL is the longest lifetime either provider grants.
const MaxTTL = 7 * 24 * time.Hour

var (
	// ErrInvalidRequest refuses a mint that would be unscoped or that the
	// provider cannot honour.
	ErrInvalidRequest = errors.New("objectcreds: invalid credential request")
	// ErrUnknownProvider refuses a provider name nothing implements.
	ErrUnknownProvider = errors.New("objectcreds: unknown temporary-credential provider")
)

// Parent is the platform's own key for the store. It stays in the platform.
type Parent struct {
	AccessKeyID     string
	SecretAccessKey string
	Endpoint        string
	Bucket          string
	Region          string
}

// Scope is what one minted credential is confined to.
type Scope struct {
	// Prefix is the key prefix, ending in "/"; nothing outside it is reachable.
	Prefix string
	Access Access
	TTL    time.Duration
}

// Credentials are the three values an S3 client needs, plus when they stop
// working.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	ExpiresAt       time.Time
}

// Minter issues credentials derived from a parent key.
type Minter interface {
	Mint(ctx context.Context, parent Parent, scope Scope) (Credentials, error)
}

// NewMinter returns the provider configured by name. There is no default: a
// deployment that hands credentials to tenants must say how they are made.
func NewMinter(provider string) (Minter, error) {
	switch provider {
	case ProviderR2:
		return R2Signer{}, nil
	case ProviderSTS:
		return STSMinter{}, nil
	default:
		return nil, fmt.Errorf("%w: %q (want %q or %q)", ErrUnknownProvider, provider, ProviderR2, ProviderSTS)
	}
}

// validate refuses anything that would widen a credential beyond one prefix
// of one bucket, or that no provider can issue.
func validate(parent Parent, scope Scope, minTTL time.Duration) error {
	switch {
	case parent.AccessKeyID == "" || parent.SecretAccessKey == "":
		return fmt.Errorf("%w: parent key incomplete", ErrInvalidRequest)
	case parent.Bucket == "":
		return fmt.Errorf("%w: bucket required", ErrInvalidRequest)
	case scope.Prefix == "" || strings.HasPrefix(scope.Prefix, "/") || !strings.HasSuffix(scope.Prefix, "/"):
		return fmt.Errorf("%w: prefix %q must be non-empty, relative and end in /", ErrInvalidRequest, scope.Prefix)
	case scope.Access != ReadWrite && scope.Access != ReadOnly:
		return fmt.Errorf("%w: access %q", ErrInvalidRequest, scope.Access)
	case scope.TTL < minTTL || scope.TTL > MaxTTL:
		return fmt.Errorf("%w: ttl %s outside [%s, %s]", ErrInvalidRequest, scope.TTL, minTTL, MaxTTL)
	}
	return nil
}
