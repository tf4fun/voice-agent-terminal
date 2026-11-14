package httpapi

import (
	_ "embed"
	"net/http"
	"strconv"
)

//go:embed web/index.html
var managementHTML []byte

//go:embed web/app.css
var managementCSS []byte

//go:embed web/app.js
var managementJS []byte

func managementAsset(content []byte, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Length", strconv.Itoa(len(content)))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
		if r.Method != "HEAD" {
			_, _ = w.Write(content)
		}
	}
}

// The public shell contains no session data or credentials. API requests retain
// their bearer authentication and same-origin mutation checks.
func managementPageRequest(r *http.Request) bool {
	if r.Method != "GET" && r.Method != "HEAD" {
		return false
	}
	return r.URL.Path == "/" || r.URL.Path == "/ui/app.css" || r.URL.Path == "/ui/app.js"
}
