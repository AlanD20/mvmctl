package nocloudnet

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// cloudInitRequestHandler is the custom HTTP handler for cloud-init files.
// Matches Python's _CloudInitRequestHandler.
type cloudInitRequestHandler struct {
	cloudInitDir string
}

func (h *cloudInitRequestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Suppress HTTP request logging (matches Python's log_message override)
	// Add headers to prevent caching (matches Python's end_headers)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	// Security: prevent path traversal
	requestedPath := strings.TrimPrefix(r.URL.Path, "/")
	requestedPath = filepath.Clean(requestedPath)
	if strings.Contains(requestedPath, "..") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	fullPath := filepath.Join(h.cloudInitDir, requestedPath)
	fullPathAbs, err := filepath.Abs(fullPath)
	if err != nil || !strings.HasPrefix(fullPathAbs, h.cloudInitDir) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	info, err := os.Stat(fullPathAbs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if info.IsDir() {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(fullPathAbs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(data)
}
