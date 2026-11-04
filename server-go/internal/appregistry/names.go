package appregistry

import (
	"errors"
	"regexp"
)

// segment is one lowercase path component the OCI spec and a DNS label both accept.
var segment = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// ErrInvalidName refuses a project or app the registry cannot name safely.
var ErrInvalidName = errors.New("the project or app cannot name an internal registry repository")

// Repository is where an app's built images live: one repository per app,
// under its project's namespace, which is all a project's token can reach.
func Repository(projectID, appName string) (string, error) {
	if !segment.MatchString(projectID) || !segment.MatchString(appName) {
		return "", ErrInvalidName
	}
	return projectID + "/" + appName, nil
}

// OwnedBy answers whether repository is one of the project's: exactly
// <project>/<app>, nothing nested and no other project's prefix.
func OwnedBy(repository, projectID string) bool {
	project, _, ok := SplitRepository(repository)
	return ok && project == projectID
}
