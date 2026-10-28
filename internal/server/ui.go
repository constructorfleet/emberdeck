package server

import (
	"embed"
	"net/http"
)

//go:embed assets/*
var uiAssets embed.FS

// serveUI serves only the public console shell and its static assets. Job data
// remains available exclusively through the authenticated API.
func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	var name, contentType string
	switch r.URL.Path {
	case "/":
		name, contentType = "assets/index.html", "text/html; charset=utf-8"
	case "/app.js":
		name, contentType = "assets/app.js", "text/javascript; charset=utf-8"
	case "/controller-settings.js":
		name, contentType = "assets/controller-settings.js", "text/javascript; charset=utf-8"
	case "/style.css":
		name, contentType = "assets/style.css", "text/css; charset=utf-8"
	case "/job-history.js":
		name, contentType = "assets/job-history.js", "text/javascript; charset=utf-8"
	case "/job-history.css":
		name, contentType = "assets/job-history.css", "text/css; charset=utf-8"
	case "/favicon.ico":
		name, contentType = "assets/favicon.ico", "image/x-icon"
	default:
		return false
	}
	body, err := uiAssets.ReadFile(name)
	if err != nil {
		http.Error(w, "console asset unavailable", http.StatusInternalServerError)
		return true
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	_, _ = w.Write(body)
	return true
}
