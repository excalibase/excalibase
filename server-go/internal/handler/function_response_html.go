package handler

import (
	"context"
	"errors"
	"mime"
	"net/http"
	"strings"

	"github.com/excalibase/provisioning-poc/internal/features"
)

// EXC-810: a browser renders HTML, XHTML and SVG as a page. A function that
// answers with one on the shared API origin could serve a fake login form from
// the platform's own host, so on this origin those answers are served as plain
// text. Functions return data; pages belong to apps and sites.

const plainTextType = "text/plain; charset=utf-8"

// blockedMediaType says whether a browser could render the type as a document
// or as a stream of them: HTML and XHTML, every XML type (a browser reads XML as
// XHTML or SVG through a namespace it can reach by a character reference, an
// entity, a UTF-16 body or a stylesheet, so the body is never trusted), XSL
// and multipart (Firefox renders a text/html part of x-mixed-replace).
func blockedMediaType(mediaType string) bool {
	switch mediaType {
	case "text/html", "text/xml", "application/xml", "text/xsl":
		return true
	}
	return strings.HasSuffix(mediaType, "+xml") || strings.HasPrefix(mediaType, "multipart/")
}

// placeable says whether a media type names a real type. A wildcard, an
// "unknown" or something that does not parse leaves the browser to guess from
// the body, which the function controls, so it counts as no type at all.
func placeable(mediaType string) bool {
	switch mediaType {
	case "", "*/*", "unknown/unknown", "application/unknown":
		return false
	}
	return strings.Contains(mediaType, "/") && !strings.HasPrefix(mediaType, "*/") && !strings.HasSuffix(mediaType, "/*")
}

// htmlGuardOn is the EXC-810 switch. A handler built without flags keeps the
// guard on: a missing wiring must not open the origin.
func (h *FunctionHandler) htmlGuardOn(ctx context.Context) bool {
	return h.flags == nil || features.Enabled(ctx, h.flags, features.FunctionHTMLGuard)
}

// parseMediaType is the lower-case type/subtype of one Content-Type value, and
// false when the value is not a well-formed media type.
func parseMediaType(value string) (string, bool) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil && !errors.Is(err, mime.ErrInvalidMediaParameter) {
		return "", false
	}
	return mediaType, mediaType != ""
}

// splitTypes separates the values the runtime joined with commas, leaving a
// comma inside a quoted parameter alone.
func splitTypes(value string) []string {
	var parts []string
	start, quoted := 0, false
	for i := 0; i < len(value); i++ {
		switch {
		case value[i] == '\\' && quoted:
			i++
		case value[i] == '"':
			quoted = !quoted
		case value[i] == ',' && !quoted:
			parts = append(parts, value[start:i])
			start = i + 1
		}
	}
	return append(parts, value[start:])
}

// guardedContentType decides the Content-Type of a function's answer from every
// value it sent (the runtime joins repeated headers with commas, and a map may
// hold the name in more than one case). A blocked value anywhere makes the whole
// answer plain text. With no usable value the type is fixed here from the body,
// as Go would sniff it, so the browser has nothing left to sniff; a sniffed
// blocked type is plain text too.
func guardedContentType(values []string, body string) string {
	var first string
	for _, value := range values {
		for _, part := range splitTypes(value) {
			mediaType, ok := parseMediaType(part)
			if ok && blockedMediaType(mediaType) {
				return plainTextType
			}
			if first == "" && ok && placeable(mediaType) {
				first = strings.TrimSpace(part)
			}
		}
	}
	if first != "" {
		return first
	}
	sniffed := http.DetectContentType([]byte(body))
	if mediaType, ok := parseMediaType(sniffed); ok && blockedMediaType(mediaType) {
		return plainTextType
	}
	return sniffed
}
