package sites

import (
	"errors"
	"sync"
)

// DefaultDeploysInFlight bounds uploads being spooled and sent at once across the process.
const DefaultDeploysInFlight = 4

var (
	ErrTooManyDeploys       = errors.New("too many deploys are uploading right now; try again in a minute")
	ErrProjectDeployRunning = errors.New("this project already has a deploy uploading; wait for it to finish")
)

// admission caps uploads in flight: a few overall, one per project, so spooled
// archives cannot fill the disk or tie up every request.
type admission struct {
	slots    chan struct{}
	mu       sync.Mutex
	projects map[string]bool
}

func newAdmission(capacity int) *admission {
	if capacity <= 0 {
		capacity = DefaultDeploysInFlight
	}
	return &admission{slots: make(chan struct{}, capacity), projects: map[string]bool{}}
}

// admit takes a slot for projectID, answering the release; it never waits.
func (a *admission) admit(projectID string) (func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.projects[projectID] {
		return nil, ErrProjectDeployRunning
	}
	select {
	case a.slots <- struct{}{}:
	default:
		return nil, ErrTooManyDeploys
	}
	a.projects[projectID] = true
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.projects, projectID)
		<-a.slots
	}, nil
}
