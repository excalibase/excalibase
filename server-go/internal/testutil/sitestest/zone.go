package sitestest

import (
	"context"
	"net/http"
	"sync"

	"github.com/excalibase/provisioning-poc/internal/cfworkers"
	"github.com/google/uuid"
)

// Zone stands in for Cloudflare for SaaS: custom hostnames by id, with the
// zone's refusal of a hostname it already holds.
type Zone struct {
	mu        sync.Mutex
	hosts     map[string]cfworkers.CustomHostname
	created   []string
	reads     int
	revalidns int
	// Log records deletes in order.
	Log        []string
	FailCreate error
	FailDelete error
	FailGet    error
	// FailRevalidate answers the re-validate PATCH with this error.
	FailRevalidate error
	// RevalidateAnswer, when set, is what the re-validate PATCH answers instead of the stored hostname.
	RevalidateAnswer *cfworkers.CustomHostname
	// FailCreateAfterStore creates the hostname, then answers this error, as a lost answer would.
	FailCreateAfterStore error
}

func NewZone() *Zone { return &Zone{hosts: map[string]cfworkers.CustomHostname{}} }

// Seed puts a hostname on the zone as if an earlier create got through, answering its id.
func (z *Zone) Seed(hostname, status, certificate string) string {
	z.mu.Lock()
	defer z.mu.Unlock()
	id := uuid.NewString()
	z.hosts[id] = cfworkers.CustomHostname{ID: id, Hostname: hostname, Status: status, SSL: cfworkers.HostnameSSL{Status: certificate}}
	return id
}

// SetState moves the hostname's activation and certificate status.
func (z *Zone) SetState(hostname, status, certificate string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	for id, host := range z.hosts {
		if host.Hostname == hostname {
			host.Status, host.SSL.Status = status, certificate
			z.hosts[id] = host
		}
	}
}

// Created lists "hostname/method" for every create the zone took.
func (z *Zone) Created() []string {
	z.mu.Lock()
	defer z.mu.Unlock()
	return append([]string(nil), z.created...)
}

func (z *Zone) Reads() int {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.reads
}

func (z *Zone) Revalidations() int {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.revalidns
}

func (z *Zone) Holds(hostname string) bool {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.findLocked(hostname) != nil
}

func (z *Zone) findLocked(hostname string) *cfworkers.CustomHostname {
	for _, host := range z.hosts {
		if host.Hostname == hostname {
			found := host
			return &found
		}
	}
	return nil
}

func (z *Zone) CreateCustomHostname(_ context.Context, hostname, method string) (cfworkers.CustomHostname, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.FailCreate != nil {
		return cfworkers.CustomHostname{}, z.FailCreate
	}
	if z.findLocked(hostname) != nil {
		return cfworkers.CustomHostname{}, &cfworkers.APIError{Status: http.StatusConflict, Codes: []int{1406}, Message: "Duplicate custom hostname found."}
	}
	if z.FailCreateAfterStore == nil {
		z.created = append(z.created, hostname+"/"+method)
	}
	id := uuid.NewString()
	host := cfworkers.CustomHostname{
		ID: id, Hostname: hostname, Status: "pending",
		OwnershipVerification: cfworkers.OwnershipVerification{Type: "txt", Name: "_cf-custom-hostname." + hostname, Value: "own-" + id[:8]},
		SSL: cfworkers.HostnameSSL{Status: "pending_validation", Method: method,
			ValidationRecords: []cfworkers.ValidationRecord{{TXTName: "_acme-challenge." + hostname, TXTValue: "dcv-" + id[:8]}}},
	}
	z.hosts[id] = host
	if z.FailCreateAfterStore != nil {
		return cfworkers.CustomHostname{}, z.FailCreateAfterStore
	}
	return host, nil
}

// Forget drops a hostname from the zone without a trace, as a delete by hand in the dashboard would.
func (z *Zone) Forget(hostname string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	for id, host := range z.hosts {
		if host.Hostname == hostname {
			delete(z.hosts, id)
		}
	}
}

func (z *Zone) GetCustomHostname(_ context.Context, id string) (cfworkers.CustomHostname, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.reads++
	if z.FailGet != nil {
		return cfworkers.CustomHostname{}, z.FailGet
	}
	host, ok := z.hosts[id]
	if !ok {
		return cfworkers.CustomHostname{}, &cfworkers.APIError{Status: http.StatusNotFound, Message: "not found"}
	}
	return host, nil
}

func (z *Zone) RevalidateCustomHostname(ctx context.Context, id, _ string) (cfworkers.CustomHostname, error) {
	z.mu.Lock()
	z.revalidns++
	z.reads--
	fail, answer := z.FailRevalidate, z.RevalidateAnswer
	z.mu.Unlock()
	if fail != nil {
		return cfworkers.CustomHostname{}, fail
	}
	if answer != nil {
		return *answer, nil
	}
	return z.GetCustomHostname(ctx, id)
}

func (z *Zone) FindCustomHostname(_ context.Context, hostname string) (*cfworkers.CustomHostname, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	return z.findLocked(hostname), nil
}

func (z *Zone) DeleteCustomHostname(_ context.Context, id string) error {
	z.mu.Lock()
	defer z.mu.Unlock()
	if z.FailDelete != nil {
		return z.FailDelete
	}
	if host, ok := z.hosts[id]; ok {
		z.Log = append(z.Log, "delete "+host.Hostname)
		delete(z.hosts, id)
	}
	return nil
}
