package sitestest

import (
	"context"
	"net"
	"strings"
	"sync"
)

// Resolver stands in for public DNS: TXT records by fully qualified name, so
// a lookup without the root's trailing dot finds nothing.
type Resolver struct {
	mu      sync.Mutex
	records map[string][]string
	lookups []string
	fail    error
}

func NewResolver() *Resolver { return &Resolver{records: map[string][]string{}} }

// Set publishes one TXT value under name, replacing any other.
func (r *Resolver) Set(name, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[name+"."] = []string{value}
}

// Add publishes another TXT value under name.
func (r *Resolver) Add(name, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records[name+"."] = append(r.records[name+"."], value)
}

// Remove deletes every TXT value under name.
func (r *Resolver) Remove(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.records, name+".")
}

// FailWith answers every lookup with err until it is called with nil.
func (r *Resolver) FailWith(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fail = err
}

// Lookups are the names asked for, in order.
func (r *Resolver) Lookups() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lookups...)
}

func (r *Resolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lookups = append(r.lookups, name)
	if r.fail != nil {
		return nil, r.fail
	}
	values, ok := r.records[name]
	if !ok || !strings.HasSuffix(name, ".") {
		return nil, &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
	}
	return append([]string(nil), values...), nil
}

// Projects stands in for the project lifecycle: which projects may serve and
// whose lifecycle lease another operation holds.
type Projects struct {
	mu      sync.Mutex
	stopped map[string]bool
	busy    map[string]bool
	// servingLeft counts the answers a project still serves before it is stopped.
	servingLeft map[string]int
	holds       int
}

func NewProjects() *Projects {
	return &Projects{stopped: map[string]bool{}, busy: map[string]bool{}, servingLeft: map[string]int{}}
}

// Stop schedules the project for deletion.
func (p *Projects) Stop(projectID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopped[projectID] = true
}

// StopAfter answers that the project serves count more times, then that it is scheduled for deletion.
func (p *Projects) StopAfter(projectID string, count int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.servingLeft[projectID] = count
}

// Busy marks the project's lifecycle lease as held by another operation.
func (p *Projects) Busy(projectID string, busy bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.busy[projectID] = busy
}

// Holds counts the lifecycle leases granted.
func (p *Projects) Holds() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.holds
}

func (p *Projects) HoldProject(_ context.Context, projectID string) (func(), bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.busy[projectID] {
		return nil, false, nil
	}
	p.holds++
	return func() {}, true, nil
}

func (p *Projects) ProjectServing(_ context.Context, projectID string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if left, counting := p.servingLeft[projectID]; counting {
		if left == 0 {
			return false, nil
		}
		p.servingLeft[projectID] = left - 1
	}
	return !p.stopped[projectID], nil
}
