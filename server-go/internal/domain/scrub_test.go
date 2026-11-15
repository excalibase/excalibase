package domain

import (
	"strings"
	"testing"
)

func TestScrubClusterTextRemovesWhatNamesTheCluster(t *testing.T) {
	cases := map[string]string{
		"dial tcp p1-postgres-rw.org1-p1.svc.cluster.local:5432: i/o timeout": "svc.cluster.local",
		"lookup moco-p1-mysql-primary.org1-p1.svc: no such host":              ".svc",
		"pods is forbidden in namespace org1-p1":                              "org1-p1",
		"connect 10.42.3.7:5432 refused":                                      "10.42.3.7",
		"connect 172.20.1.9 refused":                                          "172.20.1.9",
		"connect 192.168.4.4 refused":                                         "192.168.4.4",
		"connect [fd00:10:244::5]:5432 refused":                               "fd00:10:244::5",
	}
	for text, leaked := range cases {
		got := ScrubClusterText(text, "org1-p1")
		if strings.Contains(got, leaked) {
			t.Errorf("%q still names %q: %q", text, leaked, got)
		}
		if got == text || got == "" {
			t.Errorf("%q came back as %q", text, got)
		}
	}
}

func TestScrubClusterTextLeavesOrdinaryTextAndPublicAddressesAlone(t *testing.T) {
	for _, text := range []string{"the backup could not be taken", "dial db.excalibase.io:5432 failed", "connect 8.8.8.8:53 refused", "172.32.0.1 and 11.0.0.1", ""} {
		if got := ScrubClusterText(text, "org1-p1"); got != text {
			t.Errorf("%q became %q", text, got)
		}
	}
}
