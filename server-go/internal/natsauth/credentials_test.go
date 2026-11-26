package natsauth

import (
	"testing"
)

func TestNewPasswordIsRandomAndLongEnough(t *testing.T) {
	first, err := NewPassword()
	if err != nil {
		t.Fatalf("NewPassword: %v", err)
	}
	second, _ := NewPassword()
	if first == second {
		t.Error("NewPassword returned the same value twice")
	}
	if len(first) < 32 {
		t.Errorf("password length = %d, want >= 32", len(first))
	}
}

func TestHashPasswordDoesNotStorePlaintext(t *testing.T) {
	hash, err := HashPassword("s3cret-value")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "s3cret-value" {
		t.Error("hash equals plaintext")
	}
}
