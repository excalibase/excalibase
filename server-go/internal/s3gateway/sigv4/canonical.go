package sigv4

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// awsEncode is SigV4's UriEncode: every byte but the unreserved characters is
// percent-encoded with upper-case hex; "/" is kept when encoding a path.
func awsEncode(value string, keepSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var out strings.Builder
	out.Grow(len(value))
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			out.WriteByte(c)
		case c == '/' && keepSlash:
			out.WriteByte(c)
		default:
			out.WriteByte('%')
			out.WriteByte(hexDigits[c>>4])
			out.WriteByte(hexDigits[c&15])
		}
	}
	return out.String()
}

// canonicalURI re-encodes the decoded path. S3 signs the path encoded once,
// whatever escaping the client chose on the wire, so the decoded form is the
// only reliable starting point.
func canonicalURI(u *url.URL) string {
	if u.Path == "" {
		return "/"
	}
	return awsEncode(u.Path, true)
}

// canonicalQuery sorts the raw query's parameters by encoded name, then value.
// The raw query is split by hand: url.ParseQuery would read "+" as a space,
// which is not what the client signed.
func canonicalQuery(rawQuery string) (string, bool) {
	if rawQuery == "" {
		return "", true
	}
	type pair struct{ name, value string }
	var pairs []pair
	for _, part := range strings.Split(rawQuery, "&") {
		if part == "" {
			continue
		}
		name, value, _ := strings.Cut(part, "=")
		decodedName, err := url.PathUnescape(name)
		if err != nil {
			return "", false
		}
		decodedValue, err := url.PathUnescape(value)
		if err != nil {
			return "", false
		}
		pairs = append(pairs, pair{awsEncode(decodedName, false), awsEncode(decodedValue, false)})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].name != pairs[j].name {
			return pairs[i].name < pairs[j].name
		}
		return pairs[i].value < pairs[j].value
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.name + "=" + p.value
	}
	return strings.Join(parts, "&"), true
}

// headerValues returns what the client sent under a header name. Go's server
// moves Host and Content-Length out of the header map, so those are read back
// from where it put them.
func headerValues(r *http.Request, name string) []string {
	switch name {
	case "host":
		return []string{r.Host}
	case "content-length":
		if values := r.Header.Values("Content-Length"); len(values) > 0 {
			return values
		}
		if r.ContentLength >= 0 {
			return []string{strconv.FormatInt(r.ContentLength, 10)}
		}
		return nil
	case "transfer-encoding":
		if len(r.TransferEncoding) > 0 {
			return r.TransferEncoding
		}
	}
	return r.Header.Values(name)
}

// canonicalHeaders lists the signed headers as "name:value\n", values trimmed
// with runs of spaces collapsed and repeats joined by commas.
func canonicalHeaders(r *http.Request, signed []string) string {
	var out strings.Builder
	for _, name := range signed {
		values := headerValues(r, name)
		cleaned := make([]string, len(values))
		for i, value := range values {
			cleaned[i] = strings.Join(strings.Fields(value), " ")
		}
		out.WriteString(name)
		out.WriteByte(':')
		out.WriteString(strings.Join(cleaned, ","))
		out.WriteByte('\n')
	}
	return out.String()
}
