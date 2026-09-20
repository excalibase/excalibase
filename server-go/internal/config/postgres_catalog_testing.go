package config

import "fmt"

// PublishPostgresCatalogForTest gives every catalogue entry a published image
// for the duration of a test, and returns a function that puts the real
// catalogue back.
//
// It exists because an entry with no published image is a real state the code
// refuses on purpose (see PostgresImage). Tests that are about something else
// — provisioning stages, rollback, CRD shape — need the resolution step to
// succeed without asserting anything about which digest it produced. Tests
// that are about the refusal itself use the real catalogue.
func PublishPostgresCatalogForTest() (restore func()) {
	previous := postgresCatalog

	published := PostgresCatalog{DocumentDBRef: previous.DocumentDBRef}
	for _, entry := range previous.Majors {
		if entry.Image == "" {
			entry.Image = fmt.Sprintf("ghcr.io/excalibase/postgresql@sha256:%064d", mustAtoiMajor(entry.Major))
		}
		published.Majors = append(published.Majors, entry)
	}
	postgresCatalog = published

	return func() { postgresCatalog = previous }
}

func mustAtoiMajor(major string) int {
	var n int
	for _, r := range major {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
