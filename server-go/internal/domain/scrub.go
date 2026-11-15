package domain

import (
	"regexp"
	"strings"
)

var (
	// A cluster service name: <service>.<namespace>.svc[.cluster.local], with an optional port.
	clusterHost = regexp.MustCompile(`(?i)\b[a-z0-9][a-z0-9._-]*\.svc(?:\.cluster\.local)?(?::\d+)?\b`)
	// Private IPv4 (10/8, 172.16/12, 192.168/16) and IPv6 unique-local fd00::/8, with an optional port.
	privateAddress = regexp.MustCompile(`(?i)\[?\b(?:10(?:\.\d{1,3}){3}|172\.(?:1[6-9]|2\d|3[01])(?:\.\d{1,3}){2}|192\.168(?:\.\d{1,3}){2}|fd[0-9a-f]{2}(?::[0-9a-f]{0,4}){2,7})\b\]?(?::\d+)?`)
)

// A namespace the caller was not told about, as Kubernetes names it in an error.
var namedNamespace = regexp.MustCompile(`(?i)\bnamespaces?\s+["']?[a-z0-9](?:[a-z0-9-]*[a-z0-9])?["']?`)

const minNamespaceLength = 3

// ScrubClusterText replaces what names the cluster in an error or log line
// (service hosts, the project's namespaces, private addresses) with neutral
// words, for text a customer reads. Platform admins and the server log keep
// the raw text (EXC-759).
func ScrubClusterText(text string, namespaces ...string) string {
	if text == "" {
		return text
	}
	text = clusterHost.ReplaceAllString(text, "the database host")
	for _, namespace := range namespaces {
		if len(namespace) >= minNamespaceLength {
			text = strings.ReplaceAll(text, namespace, "the project")
		}
	}
	text = namedNamespace.ReplaceAllString(text, "the project")
	return privateAddress.ReplaceAllString(text, "an internal address")
}
