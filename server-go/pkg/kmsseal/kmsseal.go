// Package kmsseal implements AWS-KMS envelope protection for the vault unseal key.
//
// Instead of storing a plaintext unseal key (k8s Secret / env), the operator
// stores only KMS-encrypted ciphertext. At boot the vault calls KMS Decrypt to
// unwrap it — so no retrievable plaintext unseal key exists anywhere, and access
// is an IAM-authorized (revocable, audited) Decrypt rather than possession of a
// copyable secret.
//
// The vault's existing auto-unseal reads VAULT_UNSEAL_KEY; ResolveUnsealKeyEnv
// bridges the two — call it at the top of an entrypoint's main() before the vault
// is created. Endpoint is overridable via AWS_ENDPOINT_URL_KMS so CI/AIO can point
// at floci (LocalStack-compatible KMS); unset uses real AWS. Region + credentials
// come from the standard AWS chain (env, IRSA, instance role).
package kmsseal

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// Decrypter unwraps a KMS ciphertext blob. Abstracted so the unseal wiring is
// unit-testable without a real (or emulated) KMS.
type Decrypter interface {
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)
}

// Client is a KMS-backed Decrypter (and Encrypter, for the one-time bootstrap).
type Client struct {
	kms *kms.Client
}

// NewClient builds a KMS client honoring AWS_ENDPOINT_URL_KMS (→ floci in CI/AIO).
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

// Encrypt wraps plaintext under keyID — used once, out of band, to produce the
// ciphertext the operator stores (see EncryptUnsealKey).
func (c *Client) Encrypt(ctx context.Context, keyID string, plaintext []byte) ([]byte, error) {
	out, err := c.kms.Encrypt(ctx, &kms.EncryptInput{KeyId: aws.String(keyID), Plaintext: plaintext})
	if err != nil {
		return nil, err
	}
	return out.CiphertextBlob, nil
}

// UnsealKeyFromCiphertext decrypts a base64 KMS ciphertext into the plaintext
// unseal key. Empty input returns ("", nil).
func UnsealKeyFromCiphertext(ctx context.Context, d Decrypter, ciphertextB64 string) (string, error) {
	if ciphertextB64 == "" {
		return "", nil
	}
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

// EncryptUnsealKey wraps a plaintext unseal key and returns base64 ciphertext for
// the operator to store (bootstrap helper; not on the boot path).
func EncryptUnsealKey(ctx context.Context, c *Client, keyID, unsealKey string) (string, error) {
	ct, err := c.Encrypt(ctx, keyID, []byte(unsealKey))
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ct), nil
}

// ResolveUnsealKeyEnv bridges KMS-wrapped storage to the vault's env-based
// auto-unseal: when VAULT_UNSEAL_KEY_CIPHERTEXT is set, it KMS-decrypts it and
// sets VAULT_UNSEAL_KEY so newVault() unseals as usual. No-op when the ciphertext
// env is unset (legacy plaintext VAULT_UNSEAL_KEY path). Call at the top of main().
func ResolveUnsealKeyEnv(ctx context.Context) error {
	ct := os.Getenv("VAULT_UNSEAL_KEY_CIPHERTEXT")
	if ct == "" {
		return nil
	}
	c, err := NewClient(ctx)
	if err != nil {
		return err
	}
	key, err := UnsealKeyFromCiphertext(ctx, c, ct)
	if err != nil {
		return err
	}
	return os.Setenv("VAULT_UNSEAL_KEY", key)
}
