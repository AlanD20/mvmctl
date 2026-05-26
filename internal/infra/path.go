package infra

import (
	"path/filepath"
	"strings"
)

// IsSubDir checks whether path is under parent using proper path hierarchy comparison.
// Uses filepath.Rel() to avoid false positives with string prefix matching
// (e.g., "/home/user1" incorrectly matching "/home/user").
// Returns true when path == parent (exact match counts as "under").
func IsSubDir(path, parent string) bool {
	rel, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".."
}
