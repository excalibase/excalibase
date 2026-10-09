package handler

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/excalibase/provisioning-poc/internal/apphost"
)

// POST .../deploy?wait=true answers once the deploy has finished, so a CI step
// is one `curl --fail`: 200 live, 422 failed, 409 superseded, and 504 when the
// wait ran out first (the deploy goes on; poll GET .../deploys/{id}). Each poll
// reads the store once, so the wait holds no connection.
const (
	// defaultDeployWait outlasts the rollout timeout (10 minutes), so a waited
	// deploy normally finishes inside it. The edge must allow longer.
	defaultDeployWait = 11 * time.Minute
	defaultWaitPoll   = 2 * time.Second
)

// SetDeployWait bounds how long ?wait=true holds a request.
func (h *AppDeployHandler) SetDeployWait(longest time.Duration) { h.waitLongest = longest }

// deployWaitRequest reads ?wait and ?timeout (seconds, held to the server's bound).
func (h *AppDeployHandler) deployWaitRequest(r *http.Request) (time.Duration, error) {
	query := r.URL.Query()
	wait, timeout := query.Get("wait"), query.Get("timeout")
	switch wait {
	case "", "false":
		if timeout != "" {
			return 0, errors.New("timeout is the longest ?wait=true holds the request; it needs wait=true")
		}
		return 0, nil
	case "true":
	default:
		return 0, errors.New("wait must be true or false")
	}
	longest := h.waitLongest
	if longest <= 0 {
		longest = defaultDeployWait
	}
	if timeout == "" {
		return longest, nil
	}
	seconds, err := strconv.Atoi(timeout)
	if err != nil || seconds < 1 {
		return 0, errors.New("timeout must be a whole number of seconds, at least 1")
	}
	// Compared in seconds first: a huge count would overflow a Duration.
	if requested := time.Duration(seconds) * time.Second; seconds < int(longest/time.Second) && requested < longest {
		return requested, nil
	}
	return longest, nil
}

var errDeployWaitEnded = errors.New("the wait for the deploy ended")

// awaitDeploy polls the deploy until it finishes, the wait runs out
// (errDeployWaitEnded) or the caller leaves (the context's error).
func (h *AppDeployHandler) awaitDeploy(ctx context.Context, projectID, appID string, deploy *apphost.Deploy, wait time.Duration) (*apphost.Deploy, error) {
	poll := h.waitPoll
	if poll <= 0 {
		poll = defaultWaitPoll
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for !deployFinished(deploy.Status) {
		select {
		case <-ctx.Done():
			return deploy, ctx.Err()
		case <-deadline.C:
			return deploy, errDeployWaitEnded
		case <-ticker.C:
		}
		latest, err := h.deploys.GetDeploy(projectID, appID, deploy.ID)
		switch {
		case errors.Is(err, apphost.ErrDeployNotFound), errors.Is(err, apphost.ErrAppNotFound):
			return deploy, err
		case err != nil:
			// A brief store failure is retried on the next poll, not reported.
			log.Printf("deploy wait %s: %v", deploy.ID, err)
		default:
			deploy = latest
		}
	}
	return deploy, nil
}

func deployFinished(status string) bool {
	switch status {
	case apphost.DeployStatusSucceeded, apphost.DeployStatusFailed, apphost.DeployStatusSuperseded:
		return true
	}
	return false
}

// waitedDeployView is a deploy that did not end live, with why.
type waitedDeployView struct {
	deployView
	Error string `json:"error"`
}

// answerWaited writes the deploy the wait ended on.
func (h *AppDeployHandler) answerWaited(w http.ResponseWriter, r *http.Request, deploy *apphost.Deploy, err error) {
	view := newDeployView(deploy)
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return
	case errors.Is(err, errDeployWaitEnded):
		writeJSONStatus(w, http.StatusGatewayTimeout, waitedDeployView{view, fmt.Sprintf(
			"deploy %s is still %s; it carries on: poll %s/deploys/%s", deploy.ID, deploy.Status, appPathOf(r), deploy.ID)})
	case err != nil:
		h.writeError(w, err)
	case deploy.Status == apphost.DeployStatusSucceeded:
		writeJSON(w, view)
	case deploy.Status == apphost.DeployStatusSuperseded:
		writeJSONStatus(w, http.StatusConflict, waitedDeployView{view,
			fmt.Sprintf("deploy %s was superseded: a newer deploy of the app replaced it", deploy.ID)})
	default:
		writeJSONStatus(w, http.StatusUnprocessableEntity, waitedDeployView{view,
			fmt.Sprintf("deploy %s failed: %s", deploy.ID, deploy.FailureReason)})
	}
}

// appPathOf is the app's API path, as the caller reached it.
func appPathOf(r *http.Request) string {
	return strings.TrimSuffix(r.URL.Path, "/deploy")
}
