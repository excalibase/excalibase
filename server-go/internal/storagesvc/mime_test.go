package storagesvc

import (
	"context"
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

// "type/*" allows every subtype of that type and nothing else.
func TestCheckMIMEAllowlist_TypeWildcard(t *testing.T) {
	for _, mediaType := range []string{"image/png", "image/jpeg", "image/webp"} {
		if err := checkMIMEAllowlist([]string{"IMAGE/*"}, mediaType); err != nil {
			t.Errorf("image/* should allow %q: %v", mediaType, err)
		}
	}
	for _, mediaType := range []string{"text/plain", "application/image", "imagex/png"} {
		if err := checkMIMEAllowlist([]string{"image/*"}, mediaType); err == nil {
			t.Errorf("image/* should refuse %q", mediaType)
		}
	}
}

// A wildcard is not the owner naming a renderable type: an SVG on a public
// bucket still needs "image/svg+xml" listed by name.
func TestCheckPublicBucketType_WildcardDoesNotNameARenderableType(t *testing.T) {
	bucket := &Bucket{Public: true, AllowedTypes: []string{"image/*"}}
	if err := checkPublicBucketType(bucket, "image/svg+xml"); err == nil {
		t.Error("image/* must not let a public bucket serve SVG")
	}
}

func TestValidateBucketLimits(t *testing.T) {
	good := []CreateBucketRequest{
		{},
		{FileSizeLimit: 1024, AllowedMimeTypes: []string{"image/png", "image/*", "*/*", "Application/JSON"}},
	}
	for _, req := range good {
		if err := validateBucketLimits(req); err != nil {
			t.Errorf("%+v: %v", req, err)
		}
	}
	bad := map[string]CreateBucketRequest{
		"negative size":     {FileSizeLimit: -1},
		"no subtype":        {AllowedMimeTypes: []string{"image"}},
		"wildcard type":     {AllowedMimeTypes: []string{"*/png"}},
		"partial wildcard":  {AllowedMimeTypes: []string{"image/pn*"}},
		"blank entry":       {AllowedMimeTypes: []string{" "}},
		"with a parameter":  {AllowedMimeTypes: []string{"text/plain x=1"}},
		"three parts":       {AllowedMimeTypes: []string{"a/b/c"}},
		"good then garbage": {AllowedMimeTypes: []string{"image/png", "nope"}},
	}
	for name, req := range bad {
		var invalid *ValidationError
		if err := validateBucketLimits(req); !errors.As(err, &invalid) {
			t.Errorf("%s: err = %v, want a ValidationError", name, err)
		}
	}
}

func TestService_CreateBucket_RefusesInvalidLimits(t *testing.T) {
	svc := NewServiceWithObjectStore(newMemStore(), newFakeObjectStore(), nil)
	_, err := svc.CreateBucket(context.Background(), testProjX, CreateBucketRequest{Name: "assets", FileSizeLimit: -5})
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("a negative size limit must be refused, got %v", err)
	}
	b, err := svc.CreateBucket(context.Background(), testProjX, CreateBucketRequest{Name: "photos", AllowedMimeTypes: []string{"image/*"}})
	if err != nil || len(b.AllowedTypes) != 1 {
		t.Fatalf("image/* must be accepted, got %v, %v", b, err)
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
