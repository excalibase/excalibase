package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAProjectRecordSaysWhetherItHasADatabase(t *testing.T) {
	for _, tc := range []struct {
		name string
		inst DatabaseInstance
		want string
	}{
		{"with a database", DatabaseInstance{ProjectID: "p"}, `"noDatabase":false`},
		{"without a database", DatabaseInstance{ProjectID: "p", NoDatabase: true}, `"noDatabase":true`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, err := json.Marshal(tc.inst)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), tc.want) {
				t.Fatalf("serialised project %s lacks %s", body, tc.want)
			}
		})
	}
}

func TestTheNoDatabaseRefusalNamesWhatIsMissing(t *testing.T) {
	if ErrNoDatabase.Error() != "project has no database" {
		t.Fatalf("got %q", ErrNoDatabase.Error())
	}
}
