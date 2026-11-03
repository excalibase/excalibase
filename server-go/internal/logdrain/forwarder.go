package logdrain

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Internal routes on provisioning's in-cluster listener.
const (
	PullPath = "/internal/log-forwarder/pull"
	AckPath  = "/internal/log-forwarder/ack"
	// TokenHeader carries the forwarder's shared token, apart from the
	// Authorization header that platform tokens use.
	TokenHeader = "X-Excalibase-Forwarder-Token"
)

const (
	// idleWait is the pause between pulls.
	idleWait        = 2 * time.Second
	maxPlaneBackoff = 30 * time.Second
	// maxPullBytes bounds one pull answer; provisioning keeps it under MaxPullBytes.
	maxPullBytes  = 2 * MaxPullBytes
	planeTimeout  = 30 * time.Second
	unknownFailed = "could not deliver the batch"
	// sendWorkers bounds concurrent sends, so one slow destination holds one
	// worker for at most a send timeout and never the others' batches.
	sendWorkers = 8
)

// MaxDrainsPerPull bounds the batches one pull returns.
const MaxDrainsPerPull = sendWorkers

// MaxPullBytes is provisioning's budget for one pull answer.
const MaxPullBytes = 4 << 20

// PullRequest asks for at most Max batches.
type PullRequest struct {
	Max int `json:"max"`
}

// PullResponse is what provisioning answers a pull with.
type PullResponse struct {
	Batches []Batch `json:"batches"`
}

// Ack reports one batch: delivered when Error is empty.
type Ack struct {
	DrainID string `json:"drainId"`
	From    int64  `json:"from,string"`
	To      int64  `json:"to,string"`
	Lines   int    `json:"lines"`
	Error   string `json:"error,omitempty"`
}

// ControlPlane hands out batches and takes their outcome.
type ControlPlane interface {
	Pull(ctx context.Context, limit int) ([]Batch, error)
	Ack(ctx context.Context, ack Ack) error
}

// BatchSender delivers one batch.
type BatchSender interface {
	Send(ctx context.Context, batch Batch) error
}

// Forwarder pulls batches for its free workers, sends each on its own
// worker and acks each outcome.
type Forwarder struct {
	plane  ControlPlane
	sender BatchSender
	slots  chan struct{}
	// freed is signalled when a worker finishes.
	freed chan struct{}
	// sleep waits d or until ctx ends, reporting whether to go on.
	sleep func(ctx context.Context, d time.Duration) bool
}

func NewForwarder(plane ControlPlane, sender BatchSender) *Forwarder {
	return &Forwarder{plane: plane, sender: sender, slots: make(chan struct{}, sendWorkers), freed: make(chan struct{}, 1), sleep: sleepContext}
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Run pulls until ctx ends. After a full pull it pulls again as soon as a
// worker is free; otherwise it pauses idleWait, backing off up to
// maxPlaneBackoff while provisioning does not answer. Sends in flight finish
// before it returns.
func (f *Forwarder) Run(ctx context.Context) {
	var inFlight sync.WaitGroup
	defer inFlight.Wait()
	wait := idleWait
	for ctx.Err() == nil {
		asked := cap(f.slots) - len(f.slots)
		got, err := f.dispatch(ctx, &inFlight)
		if err != nil {
			log.Printf("log forwarder: pull: %v", err)
			if !f.sleep(ctx, wait) {
				return
			}
			wait = min(wait*2, maxPlaneBackoff)
			continue
		}
		wait = idleWait
		if asked > 0 && got >= asked {
			continue
		}
		if asked == 0 {
			if !f.waitForWorker(ctx) {
				return
			}
			continue
		}
		if !f.sleep(ctx, idleWait) {
			return
		}
	}
}

func (f *Forwarder) waitForWorker(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-f.freed:
		return true
	}
}

// RunOnce handles one pull to the end and returns how many batches it handled.
func (f *Forwarder) RunOnce(ctx context.Context) (int, error) {
	var inFlight sync.WaitGroup
	n, err := f.dispatch(ctx, &inFlight)
	inFlight.Wait()
	return n, err
}

// dispatch pulls for the free workers and starts a send for each batch.
func (f *Forwarder) dispatch(ctx context.Context, inFlight *sync.WaitGroup) (int, error) {
	free := cap(f.slots) - len(f.slots)
	if free == 0 {
		return 0, nil
	}
	batches, err := f.plane.Pull(ctx, free)
	if err != nil {
		return 0, err
	}
	if len(batches) > free {
		batches = batches[:free]
	}
	for _, batch := range batches {
		f.slots <- struct{}{}
		inFlight.Add(1)
		go func(batch Batch) {
			defer func() {
				<-f.slots
				select {
				case f.freed <- struct{}{}:
				default:
				}
				inFlight.Done()
			}()
			f.handle(ctx, batch)
		}(batch)
	}
	return len(batches), nil
}

func (f *Forwarder) handle(ctx context.Context, batch Batch) {
	ack := Ack{DrainID: batch.DrainID, From: batch.From, To: batch.To, Lines: len(batch.Lines)}
	if err := f.deliver(ctx, batch); err != nil {
		ack.Error = failureMessage(err)
	}
	if err := f.plane.Ack(ctx, ack); err != nil {
		log.Printf("log forwarder: ack of drain %s: %v", batch.DrainID, err)
	}
}

func (f *Forwarder) deliver(ctx context.Context, batch Batch) error {
	if len(batch.Lines) > MaxBatchLines {
		return sendFailure("the batch is larger than %d lines", MaxBatchLines)
	}
	return f.sender.Send(ctx, batch)
}

// failureMessage passes on a SendError, written to be shown; anything else
// could carry a URL or key, so only a fixed message goes back.
func failureMessage(err error) string {
	var sendErr *SendError
	if errors.As(err, &sendErr) {
		return sendErr.Error()
	}
	return unknownFailed
}

// HTTPControlPlane is provisioning's internal forwarder API.
type HTTPControlPlane struct {
	base   string
	token  string
	client *http.Client
}

func NewHTTPControlPlane(base, token string, client *http.Client) *HTTPControlPlane {
	return &HTTPControlPlane{base: strings.TrimRight(base, "/"), token: token, client: client}
}

func (p *HTTPControlPlane) Pull(ctx context.Context, limit int) ([]Batch, error) {
	asked, err := json.Marshal(PullRequest{Max: limit})
	if err != nil {
		return nil, err
	}
	resp, err := p.post(ctx, PullPath, asked)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provisioning answered %d", resp.StatusCode)
	}
	var body PullResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxPullBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode pull: %w", err)
	}
	return body.Batches, nil
}

func (p *HTTPControlPlane) Ack(ctx context.Context, ack Ack) error {
	payload, err := json.Marshal(ack)
	if err != nil {
		return err
	}
	resp, err := p.post(ctx, AckPath, payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxAnswerToRead))
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("provisioning answered %d", resp.StatusCode)
	}
	return nil
}

func (p *HTTPControlPlane) post(ctx context.Context, path string, payload []byte) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(ctx, planeTimeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+path, bytes.NewReader(payload))
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set(TokenHeader, p.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	resp.Body = cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
