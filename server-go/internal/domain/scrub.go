package domain

import (
	"regexp"
	"strings"
)

var (
	// A cluster service name: <service>.<namespace>.svc.cluster.local, or the
	// shorter <service>.<namespace>.svc, with an optional port. A single label
	// before .svc ("billing.svc") is a customer's own name and is left alone.
	clusterHost = regexp.MustCompile(`(?i)\b(?:[a-z0-9][a-z0-9-]*\.)+svc\.cluster\.local(?::\d+)?\b|\b[a-z0-9][a-z0-9-]*\.[a-z0-9][a-z0-9-]*\.svc(?::\d+)?\b`)
	// Private addresses with an optional CIDR suffix or port: 10/8, 172.16/12,
	// 192.168/16, CGNAT 100.64/10 and IPv6 unique-local fc00::/7. A CIDR is a
	// range a customer wrote down (an allow-list), so the match is left as it is.
	privateAddress = regexp.MustCompile(`(?i)\[?\b(?:10(?:\.\d{1,3}){3}|172\.(?:1[6-9]|2\d|3[01])(?:\.\d{1,3}){2}|192\.168(?:\.\d{1,3}){2}|100\.(?:6[4-9]|[7-9]\d|1[01]\d|12[0-7])(?:\.\d{1,3}){2}|f[cd][0-9a-f]{2}(?::[0-9a-f]{0,4}){2,7})\b\]?(/\d{1,3}|:\d+)?`)
	// Kubernetes names a namespace in a URL path, and in quotes after the word.
	namespaceInPath = regexp.MustCompile(`(?i)(/namespaces/)[a-z0-9][a-z0-9-]*`)
	namespaceQuoted = regexp.MustCompile(`(?i)\bnamespaces?\s+"[^"]+"`)
	// A project namespace is {org}-{project ref}; it appears bare as
	// <namespace>/<resource> in our own errors.
	projectNamespace = regexp.MustCompile(`\b[a-z0-9][a-z0-9-]*-proj-[a-z0-9]+\b(?:/[a-z0-9][a-z0-9._-]*)?`)
)

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
	text = namespaceInPath.ReplaceAllString(text, "${1}project")
	text = namespaceQuoted.ReplaceAllString(text, "the project")
	text = projectNamespace.ReplaceAllString(text, "the project")
	return privateAddress.ReplaceAllStringFunc(text, func(match string) string {
		if cidr := privateAddress.FindStringSubmatch(match); len(cidr) > 1 && strings.HasPrefix(cidr[1], "/") {
			return match
		}
		return "an internal address"
	})
}
