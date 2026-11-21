package handler

import (
	"context"
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

// pageMediaTypes are the types a browser renders as a document.
var pageMediaTypes = map[string]bool{
	"text/html":             true,
	"application/xhtml+xml": true,
	"image/svg+xml":         true,
}

// pageNamespaces make an XML document an XHTML or SVG page when the document's root element is
// in that namespace, whatever the type says about the content.
var pageNamespaces = []string{"http://www.w3.org/1999/xhtml", "http://www.w3.org/2000/svg"}

// htmlGuardOn is the EXC-810 switch. A handler built without flags keeps the
// guard on: a missing wiring must not open the origin.
func (h *FunctionHandler) htmlGuardOn(ctx context.Context) bool {
	return h.flags == nil || features.Enabled(ctx, h.flags, features.FunctionHTMLGuard)
}

// mediaTypeOf is the lower-case type/subtype of one Content-Type value.
func mediaTypeOf(value string) string {
	if mediaType, _, err := mime.ParseMediaType(value); err == nil {
		return mediaType
	}
	mediaType, _, _ := strings.Cut(value, ";")
	return strings.ToLower(strings.TrimSpace(mediaType))
}

func isXMLMediaType(mediaType string) bool {
	return mediaType == "text/xml" || mediaType == "application/xml" || strings.HasSuffix(mediaType, "+xml")
}

func inPageNamespace(body string) bool {
	for _, namespace := range pageNamespaces {
		if strings.Contains(body, namespace) {
			return true
		}
	}
	return false
}

// rendersAsPage says whether a browser would show body, served as mediaType, as a document.
func rendersAsPage(mediaType, body string) bool {
	return pageMediaTypes[mediaType] || (isXMLMediaType(mediaType) && inPageNamespace(body))
}

// guardedContentType decides the Content-Type of a function's answer from every
// value it sent (the runtime joins repeated headers with commas, and a map may
// hold the name in more than one case). A renderable value anywhere makes the
// whole answer plain text. With no value the type is fixed here from the body,
// exactly as Go would sniff it, so the browser has nothing left to sniff.
func guardedContentType(values []string, body string) string {
	var first string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			mediaType := mediaTypeOf(part)
			if mediaType == "" {
				continue
			}
			if rendersAsPage(mediaType, body) {
				return plainTextType
			}
			if first == "" {
				first = strings.TrimSpace(part)
			}
		}
	}
	if first != "" {
		return first
	}
	sniffed := http.DetectContentType([]byte(body))
	if rendersAsPage(mediaTypeOf(sniffed), body) {
		return plainTextType
	}
	return sniffed
}
