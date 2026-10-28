package domain

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestParseDBSourceAllowListAcceptsAddressesAndRanges(t *testing.T) {
	list, err := ParseDBSourceAllowList(false, []string{
		" 203.0.113.0/24 ", "198.51.100.7", "2001:db8::/32", "2001:db8:1::5", "10.0.0.0/8", "100.64.0.0/10",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"10.0.0.0/8", "100.64.0.0/10", "198.51.100.7/32", "203.0.113.0/24", "2001:db8::/32", "2001:db8:1::5/128"}
	if list.Access != DBSourceAccessListed || !slices.Equal(list.Sources, want) {
		t.Fatalf("got %s %v, want listed %v", list.Access, list.Sources, want)
	}
}

func TestParseDBSourceAllowListRemovesDuplicates(t *testing.T) {
	list, err := ParseDBSourceAllowList(false, []string{"198.51.100.7", "198.51.100.7/32", "2001:DB8::1"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !slices.Equal(list.Sources, []string{"198.51.100.7/32", "2001:db8::1/128"}) {
		t.Fatalf("got %v", list.Sources)
	}
}

func TestParseDBSourceAllowListAllowAny(t *testing.T) {
	list, err := ParseDBSourceAllowList(true, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if list.Access != DBSourceAccessAny || len(list.Sources) != 0 {
		t.Fatalf("got %s %v", list.Access, list.Sources)
	}
	if list.SourceRanges() != nil {
		t.Fatalf("allow any renders no ranges, got %v", list.SourceRanges())
	}
}

func TestParseDBSourceAllowListRefusals(t *testing.T) {
	cases := map[string]struct {
		allowAny bool
		sources  []string
		mention  string
	}{
		"empty list":              {sources: nil, mention: "at least one"},
		"any with sources":        {allowAny: true, sources: []string{"198.51.100.7"}, mention: "either"},
		"every IPv4 address":      {sources: []string{"0.0.0.0/0"}, mention: "allow any"},
		"every IPv6 address":      {sources: []string{"::/0"}, mention: "allow any"},
		"too wide IPv4":           {sources: []string{"0.0.0.0/1"}, mention: "/8"},
		"too wide IPv6":           {sources: []string{"2000::/8"}, mention: "/16"},
		"host bits set":           {sources: []string{"203.0.113.5/24"}, mention: "203.0.113.0/24"},
		"loopback":                {sources: []string{"127.0.0.1"}, mention: "loopback"},
		"IPv6 loopback":           {sources: []string{"::1"}, mention: "loopback"},
		"unspecified":             {sources: []string{"0.0.0.0/32"}, mention: "unspecified"},
		"link-local":              {sources: []string{"169.254.0.0/16"}, mention: "link-local"},
		"IPv6 link-local":         {sources: []string{"fe80::/64"}, mention: "link-local"},
		"multicast":               {sources: []string{"224.0.0.1"}, mention: "multicast"},
		"IPv4-mapped IPv6":        {sources: []string{"::ffff:198.51.100.7"}, mention: "IPv4 form"},
		"zone":                    {sources: []string{"fe80::1%eth0"}, mention: "not an address"},
		"not an address":          {sources: []string{"example.com"}, mention: "not an address"},
		"blank entry":             {sources: []string{"198.51.100.7", " "}, mention: "not an address"},
		"prefix out of range":     {sources: []string{"198.51.100.0/33"}, mention: "not an address"},
		"loopback range overlaps": {sources: []string{"127.0.0.0/8"}, mention: "loopback"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseDBSourceAllowList(tc.allowAny, tc.sources)
			if !errors.Is(err, ErrInvalidDBSourceAllowList) {
				t.Fatalf("want ErrInvalidDBSourceAllowList, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.mention) {
				t.Fatalf("error %q does not mention %q", err, tc.mention)
			}
		})
	}
}

func TestParseDBSourceAllowListCapsEntries(t *testing.T) {
	sources := make([]string, 0, MaxDBAllowedSources+1)
	for i := 0; i <= MaxDBAllowedSources; i++ {
		sources = append(sources, "198.51.100."+strconv.Itoa(i+1))
	}
	if _, err := ParseDBSourceAllowList(false, sources[:MaxDBAllowedSources]); err != nil {
		t.Fatalf("the cap itself is allowed: %v", err)
	}
	_, err := ParseDBSourceAllowList(false, sources)
	if !errors.Is(err, ErrInvalidDBSourceAllowList) || !strings.Contains(err.Error(), strconv.Itoa(MaxDBAllowedSources)) {
		t.Fatalf("want the cap named, got %v", err)
	}
}

func TestDBSourceAllowListRanges(t *testing.T) {
	listed := DBSourceAllowList{Access: DBSourceAccessListed, Sources: []string{"198.51.100.7/32"}}
	if !slices.Equal(listed.SourceRanges(), []string{"198.51.100.7/32"}) {
		t.Fatalf("listed renders its sources, got %v", listed.SourceRanges())
	}
	unset := DBSourceAllowList{}
	if unset.Chosen() || unset.SourceRanges() != nil {
		t.Fatalf("unset is not chosen and renders no ranges")
	}
	ranges := listed.SourceRanges()
	ranges[0] = "changed"
	if listed.Sources[0] != "198.51.100.7/32" {
		t.Fatal("SourceRanges must return a copy")
	}
}

// The shared address is IPv4: a list of IPv6 entries alone matches no client,
// and kube-proxy drops a list with no entry of the Service's family.
func TestParseDBSourceAllowListNeedsAnIPv4Entry(t *testing.T) {
	_, err := ParseDBSourceAllowList(false, []string{"2001:db8::/32"})
	if !errors.Is(err, ErrInvalidDBSourceAllowList) || !strings.Contains(err.Error(), "IPv4") {
		t.Fatalf("want an IPv4 refusal, got %v", err)
	}
	if _, err := ParseDBSourceAllowList(false, []string{"2001:db8::/32", "198.51.100.7"}); err != nil {
		t.Fatalf("an IPv6 entry beside an IPv4 one is kept: %v", err)
	}
}
