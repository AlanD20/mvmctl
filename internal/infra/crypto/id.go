// Package crypto provides content-addressed ID generation and hashing utilities.
package crypto

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
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

// ImageID generates a 64-char SHA256 image ID from type, source, and timestamp.
func ImageID(type_, source, timestamp string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s:%s:%s", type_, source, timestamp)
	return fmt.Sprintf("%x", h.Sum(nil))
}

// KernelID generates a 64-char SHA256 kernel ID from file content and metadata.
func KernelID(filePath, version, arch, timestamp string) (string, error) {
	fileHash, err := SHA256FileHash(filePath)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s:%s:%s:%s", fileHash, version, arch, timestamp)
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// BinaryID generates a 64-char SHA256 binary ID from file content and metadata.
func BinaryID(filePath, name, version string) (string, error) {
	fileHash, err := SHA256FileHash(filePath)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s:%s:%s", fileHash, name, version)
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

// VolumeID generates a SHA256 volume ID from name and creation timestamp.
func VolumeID(name, createdAt string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s:%s", name, createdAt)
	return fmt.Sprintf("%x", h.Sum(nil))
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
