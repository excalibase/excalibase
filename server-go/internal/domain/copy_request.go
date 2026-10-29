package domain

import (
	"errors"
	"fmt"
)

// Copy points (EXC-667): the database as it is now, its latest backup, or a
// time its backups reach.
const (
	CopyPointNow    = "now"
	CopyPointLatest = "latest"
	CopyPointTime   = "time"
)

// RestoreTargetNow is the target kind a copy of now records on its job.
const RestoreTargetNow = "now"

// ErrCopyRequestInvalid refuses a copy request the caller can correct.
var ErrCopyRequestInvalid = errors.New("copy request")

// CopyRequest is the body of a database copy: a new project in the source's
// organisation, filled from the source at a point. The caller names nothing
// else; the copy's id and organisation are the platform's to decide.
type CopyRequest struct {
	Name       string     `json:"name"`
	Point      string     `json:"point,omitempty"`
	TargetTime *ZonedTime `json:"targetTime,omitempty"`
}

// RestoreRequest is the restore into a new project that makes the copy.
func (c CopyRequest) RestoreRequest() (RestoreRequest, error) {
	name, ok := NormalizeProjectName(c.Name)
	if !ok {
		return RestoreRequest{}, fmt.Errorf("%w: name is required and must be %d characters or fewer", ErrCopyRequestInvalid, MaxProjectNameLength)
	}
	req := RestoreRequest{NewProjectName: name, Mode: RestoreModeNewProject}
	switch c.Point {
	case "", CopyPointNow:
		req.CopyNow = true
	case CopyPointLatest:
	case CopyPointTime:
		if c.TargetTime == nil {
			return RestoreRequest{}, fmt.Errorf("%w: targetTime is required for a copy at a time", ErrCopyRequestInvalid)
		}
		req.TargetTime = c.TargetTime
		return req, nil
	default:
		return RestoreRequest{}, fmt.Errorf("%w: point must be now, latest or time", ErrCopyRequestInvalid)
	}
	if c.TargetTime != nil {
		return RestoreRequest{}, fmt.Errorf("%w: targetTime is only for point time", ErrCopyRequestInvalid)
	}
	return req, nil
}
