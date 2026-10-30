package domain

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestMFAKeyPath_LivesUnderTheProjectPrefix(t *testing.T) {
	if got := MFAKeyPath("proj_1"); got != "projects/proj_1/auth/mfa" {
		t.Fatalf("path = %q", got)
	}
	if MFAKeyField != "key" {
		t.Fatalf("field = %q", MFAKeyField)
	}
}

func TestNewMFAKey_Is32RandomBytesInStandardBase64(t *testing.T) {
	first, err := NewMFAKey()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(first)
	if err != nil || len(raw) != 32 {
		t.Fatalf("key decodes to %d bytes: %v", len(raw), err)
	}
	second, _ := NewMFAKey()
	if first == second {
		t.Fatal("two keys are equal")
	}
	if !ValidMFAKey(first) {
		t.Fatal("a fresh key is not valid")
	}
}

func TestValidMFAKey_RefusesAnythingButExactly32Bytes(t *testing.T) {
	for _, key := range []string{
		"",
		"not base64!",
		base64.StdEncoding.EncodeToString(make([]byte, 31)),
		base64.StdEncoding.EncodeToString(make([]byte, 33)),
		base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("\xff", 32))),
		" " + base64.StdEncoding.EncodeToString(make([]byte, 32)),
	} {
		if ValidMFAKey(key) {
			t.Errorf("%q passed", key)
		}
	}
	if !ValidMFAKey(base64.StdEncoding.EncodeToString(make([]byte, 32))) {
		t.Error("a 32-byte key was refused")
	}
}
