// Package kmsseal keeps the vault unseal key under AWS KMS.
//
// The vault's single unseal share is encrypted with a KMS key the moment it is
// generated (ShareWrapper) and only that ciphertext is stored. At boot the
// ciphertext is decrypted in memory and the vault unsealed (UnsealAtBoot); no
// plaintext unseal key is ever written to a Secret, a file or the environment.
// Access to the key is then an IAM-authorized, audited kms:Decrypt that can be
// revoked, not possession of a copyable secret.
//
// AWS_ENDPOINT_URL_KMS points the client at a KMS emulator (LocalStack) for
// tests; unset uses real AWS. Region and credentials come from the standard
// AWS chain (env, web identity, instance role).
package kmsseal

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/excalibase/provisioning-poc/pkg/vault"
)

// ErrCiphertextMissing is returned when the vault is initialized but no
// wrapped unseal key was supplied: starting would leave it sealed.
var ErrCiphertextMissing = errors.New("the vault is initialized but VAULT_UNSEAL_KEY_CIPHERTEXT is empty; refusing to start with a sealed vault")

// Decrypter unwraps a KMS ciphertext blob.
type Decrypter interface {
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
}

// Encrypter wraps plaintext under a KMS key.
type Encrypter interface {
	Encrypt(ctx context.Context, keyID string, plaintext []byte) ([]byte, error)
}

// Client is a KMS-backed Encrypter and Decrypter.
type Client struct {
	kms *kms.Client
}

// NewClient builds a KMS client honoring AWS_ENDPOINT_URL_KMS.
func NewClient(ctx context.Context) (*Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	var kmsOpts []func(*kms.Options)
	if ep := os.Getenv("AWS_ENDPOINT_URL_KMS"); ep != "" {
		kmsOpts = append(kmsOpts, func(o *kms.Options) { o.BaseEndpoint = aws.String(ep) })
	}
	return &Client{kms: kms.NewFromConfig(cfg, kmsOpts...)}, nil
}

// Decrypt unwraps a KMS ciphertext blob to plaintext.
func (c *Client) Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	out, err := c.kms.Decrypt(ctx, &kms.DecryptInput{CiphertextBlob: ciphertext})
	if err != nil {
		return nil, err
	}
	return out.Plaintext, nil
}

// Encrypt wraps plaintext under keyID.
func (c *Client) Encrypt(ctx context.Context, keyID string, plaintext []byte) ([]byte, error) {
	out, err := c.kms.Encrypt(ctx, &kms.EncryptInput{KeyId: aws.String(keyID), Plaintext: plaintext})
	if err != nil {
		return nil, err
	}
	return out.CiphertextBlob, nil
}

// ShareWrapper returns a vault.ShareWrapper that encrypts every share under
// keyID and returns them base64-encoded, ready to store.
func ShareWrapper(ctx context.Context, e Encrypter, keyID string) vault.ShareWrapper {
	return func(shares []string) ([]string, error) {
		wrapped := make([]string, len(shares))
		for i, share := range shares {
			ct, err := e.Encrypt(ctx, keyID, []byte(share))
			if err != nil {
				return nil, fmt.Errorf("kms encrypt unseal share: %w", err)
			}
			wrapped[i] = base64.StdEncoding.EncodeToString(ct)
		}
		return wrapped, nil
	}
}

// UnsealKeyFromCiphertext decrypts a base64 KMS ciphertext into the plaintext
// unseal key.
func UnsealKeyFromCiphertext(ctx context.Context, d Decrypter, ciphertextB64 string) (string, error) {
	ct, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return "", fmt.Errorf("decode unseal ciphertext (base64): %w", err)
	}
	pt, err := d.Decrypt(ctx, ct)
	if err != nil {
		return "", fmt.Errorf("kms decrypt unseal key: %w", err)
	}
	return string(pt), nil
}

// Sealable is the part of the vault boot-time unsealing needs.
type Sealable interface {
	CheckInitialized() (bool, error)
	Sealed() bool
	Unseal(shareHex string) (*vault.UnsealProgress, error)
}

// UnsealAtBoot opens an initialized vault from its wrapped unseal key, or
// returns why it cannot. It never leaves an initialized vault sealed without
// an error: a missing ciphertext, one KMS will not decrypt, or a key that does
// not open this vault all refuse boot. An uninitialized vault is left alone —
// the bootstrap Job initializes it.
func UnsealAtBoot(ctx context.Context, v Sealable, d Decrypter, ciphertextB64 string) error {
	initialized, err := v.CheckInitialized()
	if err != nil {
		return fmt.Errorf("cannot tell whether the vault is initialized: %w", err)
	}
	if !initialized || !v.Sealed() {
		return nil
	}
	if ciphertextB64 == "" {
		return ErrCiphertextMissing
	}
	key, err := UnsealKeyFromCiphertext(ctx, d, ciphertextB64)
	if err != nil {
		return err
	}
	progress, err := v.Unseal(key)
	if err != nil {
		return fmt.Errorf("the KMS-decrypted unseal key does not open this vault: %w", err)
	}
	if !progress.Done || v.Sealed() {
		return errors.New("the KMS-decrypted unseal key did not unseal the vault")
	}
	return nil
}
