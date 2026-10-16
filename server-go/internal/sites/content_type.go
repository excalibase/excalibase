package sites

import (
	"mime"
	"path"
	"strings"
)

// webTypes pins the types a built frontend ships, so serving does not depend
// on which mime.types file the control-plane host happens to carry.
var webTypes = map[string]string{
	".html":        "text/html; charset=utf-8",
	".htm":         "text/html; charset=utf-8",
	".css":         "text/css; charset=utf-8",
	".js":          "text/javascript; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".webmanifest": "application/manifest+json",
	".txt":         "text/plain; charset=utf-8",
	".xml":         "application/xml",
	".svg":         "image/svg+xml",
	".png":         "image/png",
	".jpg":         "image/jpeg",
	".jpeg":        "image/jpeg",
	".gif":         "image/gif",
	".webp":        "image/webp",
	".avif":        "image/avif",
	".ico":         "image/x-icon",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".ttf":         "font/ttf",
	".otf":         "font/otf",
	".wasm":        "application/wasm",
	".mp4":         "video/mp4",
	".webm":        "video/webm",
	".mp3":         "audio/mpeg",
	".ogg":         "audio/ogg",
	".wav":         "audio/wav",
	".pdf":         "application/pdf",
}

const fallbackContentType = "application/octet-stream"

func contentTypeFor(sitePath string) string {
	extension := strings.ToLower(path.Ext(sitePath))
	if known, ok := webTypes[extension]; ok {
		return known
	}
	if guessed := mime.TypeByExtension(extension); guessed != "" {
		return guessed
	}
	return fallbackContentType
}
