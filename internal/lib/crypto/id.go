// Package crypto provides content-addressed ID generation and hashing utilities.
package crypto

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// SHA256FileHash computes the SHA-256 hex digest of a file.
func SHA256FileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// SHA256 returns the SHA-256 hex digest of data.
func SHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)
}

// ContentHash computes a SHA-256 hex digest of the concatenated string parts.
// Deterministic — same parts always produce the same hash.
func ContentHash(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// ImageID generates a 64-char SHA256 image ID from type, source, and timestamp.
func ImageID(type_, source, timestamp string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s:%s:%s", type_, source, timestamp)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// KernelID generates a 64-char SHA256 kernel ID from file content and metadata.
// Deterministic — same file + metadata always produces the same ID.
func KernelID(filePath, version, arch string) (string, error) {
	fileHash, err := SHA256FileHash(filePath)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s:%s:%s", fileHash, version, arch)
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// BinaryID generates a 64-char SHA256 binary ID from file content and metadata.
func BinaryID(filePath, typ, version string) (string, error) {
	fileHash, err := SHA256FileHash(filePath)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s:%s:%s", fileHash, typ, version)
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// VMID generates a 32-char SHA256 VM ID from name and creation timestamp.
// 32-char truncation for Unix domain socket path limit (~108 bytes).
func VMID(name, createdAt string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s:%s", name, createdAt)
	return Truncate(fmt.Sprintf("%x", h.Sum(nil)), 32)
}

// NetworkID generates a 64-char SHA256 network ID from name, subnet, and timestamp.
func NetworkID(name, subnet, createdAt string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s:%s:%s", name, subnet, createdAt)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// BatchID generates a 16-char SHA256 batch ID from name and creation timestamp.
// Short length keeps cache directory paths manageable.
func BatchID(name, createdAt string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s:%s", name, createdAt)
	return Truncate(fmt.Sprintf("%x", h.Sum(nil)), 16)
}

// SnapshotID generates a 64-char SHA256 snapshot ID from source VM ID and
// creation timestamp. Full 64 chars (no truncation) since snapshot IDs are
// not used in Unix domain socket paths.
func SnapshotID(sourceVMID, createdAt string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s:%s", sourceVMID, createdAt)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// VolumeID generates a SHA256 volume ID from name and creation timestamp.
func VolumeID(name, createdAt string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s:%s", name, createdAt)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// WorkflowID derives a deterministic workflow ID from a spec file path.
// Returns the first 16 characters of the SHA-256 hash of the resolved absolute
// path (8 bytes). Deterministic per file path.
func WorkflowID(path string) string {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	h := sha256.Sum256([]byte(absPath))
	return fmt.Sprintf("%x", h[:8])
}

// ShortenID returns the first N characters of an ID for display (default 12).
func ShortenID(id string, length ...int) (string, error) {
	n := 12
	if len(length) > 0 {
		n = length[0]
	}
	if len(id) < n {
		return "", fmt.Errorf("hash '%s' is shorter than requested length %d", id, n)
	}
	return id[:n], nil
}

// Truncate returns the first n characters of s.
// If s is shorter than n, returns s unchanged. Never errors.
func Truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
