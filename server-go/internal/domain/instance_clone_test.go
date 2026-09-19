package domain

import (
	"reflect"
	"testing"
	"time"
)

// A store hands readers a Clone so they cannot reach back into the stored
// row. A shallow copy would still share every reference field, so this walks
// the struct by reflection over pointers, maps and slices alike: a reference
// field added later fails here until Clone handles it.
func TestCloneSharesNoPointerWithTheOriginal(t *testing.T) {
	original := fullyPopulatedInstance()
	clone := original.Clone()

	if !reflect.DeepEqual(original, clone) {
		t.Fatal("a clone must carry the same values as the original")
	}

	ov := reflect.ValueOf(original).Elem()
	cv := reflect.ValueOf(clone).Elem()
	for i := 0; i < ov.NumField(); i++ {
		field := ov.Type().Field(i)
		switch ov.Field(i).Kind() {
		case reflect.Ptr, reflect.Map, reflect.Slice:
		default:
			continue
		}
		if ov.Field(i).IsNil() {
			t.Fatalf("%s is nil in the fixture; populate it so the sharing check is real", field.Name)
		}
		if ov.Field(i).Pointer() == cv.Field(i).Pointer() {
			t.Errorf("%s is shared with the original; a reader could rewrite the stored row through it", field.Name)
		}
	}
}

// Writing through the clone must leave the original untouched.
func TestCloneIsIndependentOfTheOriginal(t *testing.T) {
	original := fullyPopulatedInstance()
	clone := original.Clone()

	*clone.DeletionProtection = !*original.DeletionProtection
	*clone.Port = 9999
	clone.CreatedAt.Time = clone.CreatedAt.Add(time.Hour)

	if *original.DeletionProtection != true {
		t.Error("deletion protection was changed through the clone")
	}
	if *original.Port != 5432 {
		t.Errorf("port was changed through the clone: %d", *original.Port)
	}
	if !original.CreatedAt.Equal(time.Unix(0, 0).UTC()) {
		t.Errorf("createdAt was changed through the clone: %v", original.CreatedAt)
	}
}

func TestCloneOfNilIsNil(t *testing.T) {
	var inst *DatabaseInstance
	if inst.Clone() != nil {
		t.Error("cloning nothing must give nothing")
	}
}

// fullyPopulatedInstance sets every pointer field so the reflection walk has
// something to compare on each of them.
func fullyPopulatedInstance() *DatabaseInstance {
	id := int64(7)
	port := 5432
	yes := true
	days := 7
	minutes := 30
	epoch := &FlexTime{Time: time.Unix(0, 0).UTC()}
	return &DatabaseInstance{
		ID: &id, ProjectID: "proj-1", OrgID: "org-1",
		Port: &port, DeletionProtection: &yes, PoolerEnabled: &yes,
		NetworkPolicyEnabled: &yes, AutoMinorVersionUpgrade: &yes, BackupEnabled: &yes,
		MaintenanceWindowDurationMinutes: &minutes, BackupRetentionDays: &days,
		LastActiveAt: epoch, CreatedAt: epoch, UpdatedAt: epoch, LastHealthCheck: epoch,
	}
}
