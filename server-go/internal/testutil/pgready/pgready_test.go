package pgready

import (
	"reflect"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// testcontainers' own default is 60s at every level; a slow runner needs more.
const testcontainersDefault = 60 * time.Second

func deadlineOf(t *testing.T, strategy *wait.MultiStrategy) time.Duration {
	t.Helper()
	field := reflect.ValueOf(strategy).Elem().FieldByName("deadline")
	if field.IsNil() {
		t.Fatal("strategy has no deadline")
	}
	return time.Duration(field.Elem().Int())
}

func TestStrategy_InnerStepsOutlastTheLibraryDefault(t *testing.T) {
	strategy := Strategy().(*wait.MultiStrategy)
	if timeout := strategy.Timeout(); timeout == nil || *timeout <= testcontainersDefault {
		t.Fatalf("inner steps keep the library default of %v: %v", testcontainersDefault, timeout)
	}
	if deadline := deadlineOf(t, strategy); deadline <= testcontainersDefault {
		t.Fatalf("deadline %v does not outlast the library default", deadline)
	}
}

func TestWait_OuterDeadlineOutlastsTheLibraryDefault(t *testing.T) {
	var request testcontainers.GenericContainerRequest
	if err := Wait()(&request); err != nil {
		t.Fatal(err)
	}
	outer, ok := request.WaitingFor.(*wait.MultiStrategy)
	if !ok {
		t.Fatalf("waiting for %T", request.WaitingFor)
	}
	if deadline := deadlineOf(t, outer); deadline <= testcontainersDefault {
		t.Fatalf("outer deadline %v is the library's 60s wrapper", deadline)
	}
}
