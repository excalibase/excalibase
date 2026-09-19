package storagesvc

import (
	"errors"
	"testing"
)

// Normalisation decides what "the same type" means. Both the declared type
// and the stored one go through it, so an allow-list cannot be dodged by
// casing or by hanging parameters off the value, and a well-formed request
// is not rejected for spelling its charset out.
func TestNormaliseMIME(t *testing.T) {
	ok := map[string]string{
		"image/png":                 "image/png",
		"IMAGE/PNG":                 "image/png",
		"  image/png  ":             "image/png",
		"image/png; charset=binary": "image/png",
		"text/plain;charset=UTF-8":  "text/plain",
		"Application/JSON":          "application/json",
	}
	for in, want := range ok {
		got, err := normaliseMIME(in)
		if err != nil {
			t.Errorf("normaliseMIME(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("normaliseMIME(%q) = %q, want %q", in, got, want)
		}
	}

	bad := []string{
		"",                // absent
		"   ",             // blank
		"image",           // no subtype
		"image/",          // empty subtype
		"/png",            // empty type
		"image/png x=1",   // malformed parameters
		"image/png; =bad", // malformed parameters
		"a/b/c",           // not a media type
	}
	for _, in := range bad {
		if got, err := normaliseMIME(in); err == nil {
			t.Errorf("normaliseMIME(%q) = %q, want an error", in, got)
		}
	}
}

func TestNormaliseMIME_MissingValueIsAValidationError(t *testing.T) {
	_, err := normaliseMIME("")
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("a missing content type is the caller's to fix, got %T", err)
	}
}

// A parameter cannot smuggle a type past the allow-list, and casing on either
// side of the comparison makes no difference.
func TestCheckMIMEAllowlist(t *testing.T) {
	allowed := []string{"image/png", "image/jpeg"}
	for _, value := range []string{"image/png", "IMAGE/PNG", "image/png; charset=binary"} {
		mediaType, err := normaliseMIME(value)
		if err != nil {
			t.Fatalf("normaliseMIME(%q): %v", value, err)
		}
		if err := checkMIMEAllowlist(allowed, mediaType); err != nil {
			t.Errorf("%q should be allowed: %v", value, err)
		}
	}
	for _, value := range []string{"application/x-msdownload", "image/png.exe", "text/html"} {
		mediaType, err := normaliseMIME(value)
		if err != nil {
			continue // unparseable values never reach the allow-list
		}
		if err := checkMIMEAllowlist(allowed, mediaType); err == nil {
			t.Errorf("%q should be refused", value)
		}
	}
}

// An allow-list written in mixed case still means what it says.
func TestCheckMIMEAllowlist_NormalisesEntries(t *testing.T) {
	if err := checkMIMEAllowlist([]string{"IMAGE/PNG; charset=binary"}, "image/png"); err != nil {
		t.Errorf("a mixed-case allow-list entry should still match: %v", err)
	}
}

// A malformed entry must match nothing rather than widen the bucket.
func TestCheckMIMEAllowlist_IgnoresMalformedEntries(t *testing.T) {
	if err := checkMIMEAllowlist([]string{"image", "image/png"}, "image/png"); err != nil {
		t.Errorf("valid sibling entry should still match: %v", err)
	}
	if err := checkMIMEAllowlist([]string{"image"}, "image/gif"); err == nil {
		t.Error("a malformed entry must not allow an unrelated type")
	}
}

func TestCheckMIMEAllowlist_EmptyAndWildcard(t *testing.T) {
	if err := checkMIMEAllowlist(nil, "application/octet-stream"); err != nil {
		t.Errorf("an empty allow-list permits any type: %v", err)
	}
	if err := checkMIMEAllowlist([]string{"*/*"}, "application/octet-stream"); err != nil {
		t.Errorf("wildcard permits any type: %v", err)
	}
}
