package kmsseal

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// Live round-trip against a real KMS endpoint (floci in CI/AIO, or AWS). Skipped
// unless EXC_KMS_ITEST=1; requires AWS_ENDPOINT_URL_KMS + creds/region in env.
func TestIntegration_KMSRoundTrip(t *testing.T) {
	if os.Getenv("EXC_KMS_ITEST") == "" {
		t.Skip("set EXC_KMS_ITEST=1 (+ AWS_ENDPOINT_URL_KMS) to run the live KMS round-trip")
	}
	ctx := context.Background()
	c, err := NewClient(ctx)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	keyOut, err := c.kms.CreateKey(ctx, &kms.CreateKeyInput{})
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	keyID := *keyOut.KeyMetadata.KeyId

	const share = "test-unseal-share-xyz"
	ctB64, err := EncryptUnsealKey(ctx, c, keyID, share)
	if err != nil {
		t.Fatalf("EncryptUnsealKey: %v", err)
	}
	got, err := UnsealKeyFromCiphertext(ctx, c, ctB64)
	if err != nil {
		t.Fatalf("UnsealKeyFromCiphertext: %v", err)
	}
	if got != share {
		t.Fatalf("round-trip mismatch: got %q want %q", got, share)
	}
}

type fakeDecrypter struct {
	plaintext []byte
	err       error
	gotCT     []byte
}

func (f *fakeDecrypter) Decrypt(_ context.Context, ct []byte) ([]byte, error) {
	f.gotCT = ct
	return f.plaintext, f.err
}

func TestUnsealKeyFromCiphertext_decrypts(t *testing.T) {
	fake := &fakeDecrypter{plaintext: []byte("the-shamir-share")}
	ctB64 := base64.StdEncoding.EncodeToString([]byte("wrapped-blob"))

	key, err := UnsealKeyFromCiphertext(context.Background(), fake, ctB64)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "the-shamir-share" {
		t.Fatalf("got key %q, want %q", key, "the-shamir-share")
	}
	if string(fake.gotCT) != "wrapped-blob" {
		t.Fatalf("decrypter got ciphertext %q, want raw decoded blob", fake.gotCT)
	}
}

func TestUnsealKeyFromCiphertext_emptyIsNoop(t *testing.T) {
	key, err := UnsealKeyFromCiphertext(context.Background(), &fakeDecrypter{}, "")
	if err != nil || key != "" {
		t.Fatalf("empty ciphertext must return (\"\", nil); got (%q, %v)", key, err)
	}
}

func TestUnsealKeyFromCiphertext_badBase64(t *testing.T) {
	if _, err := UnsealKeyFromCiphertext(context.Background(), &fakeDecrypter{}, "not!base64!"); err == nil {
		t.Fatal("expected error on invalid base64 ciphertext")
	}
}

func TestUnsealKeyFromCiphertext_decryptError(t *testing.T) {
	fake := &fakeDecrypter{err: errors.New("kms denied")}
	ctB64 := base64.StdEncoding.EncodeToString([]byte("blob"))
	if _, err := UnsealKeyFromCiphertext(context.Background(), fake, ctB64); err == nil {
		t.Fatal("expected error to propagate from Decrypt")
	}
}
