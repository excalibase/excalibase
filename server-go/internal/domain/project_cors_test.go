package domain

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParseCorsOrigins_CanonicalisesAcceptedEntries(t *testing.T) {
	got, err := ParseCorsOrigins([]string{
		" HTTPS://App.Example.com ",
		"http://localhost:5173",
		"https://app.example.com",
		"https://app.example.com:443",
		"http://shop.example.com:80",
		"",
		"capacitor://localhost",
		"https://[2001:db8::1]:8443",
	}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{
		"capacitor://localhost",
		"http://localhost:5173",
		"http://shop.example.com",
		"https://[2001:db8::1]:8443",
		"https://app.example.com",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestParseCorsOrigins_EmptyListIsEmptyNotNil(t *testing.T) {
	got, err := ParseCorsOrigins(nil, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("want empty non-nil list, got %#v", got)
	}
}

func TestParseCorsOrigins_Rejects(t *testing.T) {
	cases := []struct {
		name  string
		entry string
	}{
		{"no scheme", "app.example.com"},
		{"scheme only", "https://"},
		{"path", "https://app.example.com/"},
		{"deep path", "https://app.example.com/app"},
		{"query", "https://app.example.com?x=1"},
		{"fragment", "https://app.example.com#top"},
		{"userinfo", "https://user:pw@app.example.com"},
		{"subdomain wildcard", "https://*.example.com"},
		{"port zero", "https://app.example.com:0"},
		{"port too large", "https://app.example.com:70000"},
		{"port not numeric", "https://app.example.com:abc"},
		{"whitespace inside", "https://app example.com"},
		{"null origin", "null"},
		{"comma list", "https://a.example.com,https://b.example.com"},
		{"bare wildcard without flag", "*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseCorsOrigins([]string{tc.entry}, false)
			if !errors.Is(err, ErrInvalidCorsOrigin) {
				t.Fatalf("%q: want ErrInvalidCorsOrigin, got %v", tc.entry, err)
			}
		})
	}
}

func TestParseCorsOrigins_ErrorNamesEntry(t *testing.T) {
	_, err := ParseCorsOrigins([]string{"https://ok.example.com", "bad.example.com"}, false)
	if err == nil || !strings.Contains(err.Error(), "bad.example.com") {
		t.Fatalf("error must name the offending entry, got %v", err)
	}
}

func TestParseCorsOrigins_WildcardOnlyAsSingleEntryWithFlag(t *testing.T) {
	got, err := ParseCorsOrigins([]string{"*"}, true)
	if err != nil {
		t.Fatalf("wildcard with flag: %v", err)
	}
	if !reflect.DeepEqual(got, []string{CorsWildcard}) {
		t.Fatalf("got %v", got)
	}
	if _, err := ParseCorsOrigins([]string{"*", "https://app.example.com"}, true); !errors.Is(err, ErrInvalidCorsOrigin) {
		t.Fatalf("wildcard mixed with origins must be refused, got %v", err)
	}
	if _, err := ParseCorsOrigins([]string{"*"}, false); !errors.Is(err, ErrInvalidCorsOrigin) {
		t.Fatalf("wildcard without allowWildcard must be refused, got %v", err)
	}
	// The flag alone never widens a concrete list.
	got, err = ParseCorsOrigins([]string{"https://app.example.com"}, true)
	if err != nil || !reflect.DeepEqual(got, []string{"https://app.example.com"}) {
		t.Fatalf("flag with a concrete list: got %v err %v", got, err)
	}
}

func TestParseCorsOrigins_CapsEntryCount(t *testing.T) {
	entries := make([]string, 0, MaxCorsOrigins+1)
	for i := 0; i <= MaxCorsOrigins; i++ {
		entries = append(entries, fmt.Sprintf("https://app%d.example.com", i))
	}
	if _, err := ParseCorsOrigins(entries, false); !errors.Is(err, ErrInvalidCorsOrigin) {
		t.Fatalf("want cap error, got %v", err)
	}
	if _, err := ParseCorsOrigins(entries[:MaxCorsOrigins], false); err != nil {
		t.Fatalf("exactly %d entries must be accepted: %v", MaxCorsOrigins, err)
	}
}

func TestIsCorsWildcard(t *testing.T) {
	if !IsCorsWildcard([]string{"*"}) {
		t.Fatal("[*] is the wildcard list")
	}
	if IsCorsWildcard([]string{"https://app.example.com"}) || IsCorsWildcard(nil) {
		t.Fatal("concrete or empty lists are not wildcard")
	}
}
