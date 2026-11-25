package domain

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

var (
	isoDateTime    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}`)
	zonelessLayout = regexp.MustCompile(`"2006-01-02T15:04:05[^"]*"`)
	zonedTime      = regexp.MustCompile(`(Z|[+-]\d{2}:\d{2})$`)
	flexType       = reflect.TypeOf(FlexTime{})
	utcType        = reflect.TypeOf(UTCTime{})
	stdTimeType    = reflect.TypeOf(time.Time{})
)

// A server clock that is not UTC must still produce the same instant.
var saigonZone = time.FixedZone("ICT", 7*3600)

func isTimeType(t reflect.Type) bool {
	return t == flexType || t == utcType || t == stdTimeType
}

// fillTimes sets every time-typed field reachable from v, so the contract
// covers fields added later without anyone listing them.
func fillTimes(v reflect.Value, at time.Time) {
	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		fillTimes(v.Elem(), at)
	case reflect.Struct:
		switch v.Type() {
		case flexType:
			v.Set(reflect.ValueOf(FlexTime{Time: at}))
		case utcType:
			v.Set(reflect.ValueOf(UTCTime{Time: at}))
		case stdTimeType:
			v.Set(reflect.ValueOf(at))
		default:
			for i := 0; i < v.NumField(); i++ {
				if v.Type().Field(i).IsExported() {
					fillTimes(v.Field(i), at)
				}
			}
		}
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillTimes(v.Index(0), at)
	}
}

func collectTimestampStrings(value any, path string, out map[string]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			collectTimestampStrings(item, path+"."+key, out)
		}
	case []any:
		for _, item := range typed {
			collectTimestampStrings(item, path+"[]", out)
		}
	case string:
		if isoDateTime.MatchString(typed) {
			out[path] = typed
		}
	}
}

func TestEveryTimeFieldInAResponseCarriesItsZone(t *testing.T) {
	responses := map[string]any{
		"DatabaseInstance":     &DatabaseInstance{},
		"ProvisioningResponse": &ProvisioningResponse{},
		"DatabaseMetrics":      &DatabaseMetrics{},
		"Alert":                &Alert{},
		"SnapshotInfo":         &SnapshotInfo{},
		"MigrationRecord":      &MigrationRecord{},
		"Org":                  &Org{},
		"OrgMember":            &OrgMember{},
		"ProjectMember":        &ProjectMember{},
		"PendingInvite":        &PendingInvite{},
	}
	instant := time.Date(2026, 11, 25, 9, 30, 15, 123456789, saigonZone)
	for name, response := range responses {
		t.Run(name, func(t *testing.T) {
			fillTimes(reflect.ValueOf(response), instant)
			raw, err := json.Marshal(response)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var decoded any
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatalf("decode: %v", err)
			}
			found := map[string]string{}
			collectTimestampStrings(decoded, name, found)
			if len(found) == 0 {
				t.Fatalf("%s has no time field; drop it from the list", name)
			}
			for path, text := range found {
				if !zonedTime.MatchString(text) {
					t.Errorf("%s = %q has no zone", path, text)
				}
				parsed, err := time.Parse(time.RFC3339Nano, text)
				if err != nil || !parsed.Equal(instant) {
					t.Errorf("%s = %q is not the instant %s (%v)", path, text, instant, err)
				}
			}
		})
	}
}

// Only the readers may spell a zoneless layout; a writer using one would put
// a bare clock time on the wire again.
func TestNoWriterEmitsAZonelessLayout(t *testing.T) {
	readers := map[string]bool{"internal/domain/instance.go": true, "internal/domain/utctime.go": true, "internal/tableimport/types.go": true}
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return walkErr
		}
		if readers[filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))] {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, layout := range zonelessLayout.FindAllString(string(source), -1) {
			if !strings.Contains(layout, "Z") {
				t.Errorf("%s spells the zoneless layout %s", path, layout)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
