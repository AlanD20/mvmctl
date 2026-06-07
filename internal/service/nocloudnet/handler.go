package nocloudnet

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// cloudInitRequestHandler serves cloud-init files.
//
// In single-dir mode (singleDir=true): the URL path /<file> is served directly
// from <baseDir>/<file>. Used for single-VM respawns and legacy paths.
//
// In batch mode (singleDir=false): the URL path /<vm-id>/<file> is served from
// <baseDir>/<vm-id>/<file>. Falls back to <baseDir>/common/<file> if the
// VM-specific file is absent. Used for shared batch nocloud servers.
type cloudInitRequestHandler struct {
	baseDir   string
	singleDir bool
}

func (h *cloudInitRequestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")

	if h.singleDir {
		h.serveSingleDir(w, r)
	} else {
		h.serveMultiDir(w, r)
	}
}

// serveSingleDir serves /<file> from <baseDir>/<file>.
func (h *cloudInitRequestHandler) serveSingleDir(w http.ResponseWriter, r *http.Request) {
	requestedPath := strings.TrimPrefix(r.URL.Path, "/")
	requestedPath = filepath.Clean(requestedPath)
	if strings.Contains(requestedPath, "..") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	fullPath := filepath.Join(h.baseDir, requestedPath)
	fullPathAbs, err := filepath.Abs(fullPath)
	if err != nil || !strings.HasPrefix(fullPathAbs, h.baseDir) {
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

// serveMultiDir serves /<vm-id>/<file> from <baseDir>/<vm-id>/<file>,
// falling back to <baseDir>/common/<file> for shared files.
func (h *cloudInitRequestHandler) serveMultiDir(w http.ResponseWriter, r *http.Request) {
	// Parse path: /<vm-identifier>/<file>
	path := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.NotFound(w, r)
		return
	}
	vmID, fileName := parts[0], parts[1]

	// Security: prevent path traversal in both components.
	if strings.Contains(vmID, "..") || strings.Contains(fileName, "..") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	vmDir := filepath.Join(h.baseDir, vmID, fileName)
	vmDir = filepath.Clean(vmDir)
	if !strings.HasPrefix(vmDir, h.baseDir) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	info, err := os.Stat(vmDir)
	if err == nil && !info.IsDir() {
		data, err := os.ReadFile(vmDir)
		if err == nil {
			_, _ = w.Write(data)
			return
		}
	}

	// Fall back to common/<file> for files shared across all VMs.
	commonPath := filepath.Join(h.baseDir, "common", fileName)
	commonPath = filepath.Clean(commonPath)
	if !strings.HasPrefix(commonPath, h.baseDir) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}

	info, err = os.Stat(commonPath)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(commonPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(data)
}
