package infra

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

// SHA256Hash returns a 64-character lowercase hexadecimal SHA256 hash of data.
func SHA256Hash(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

// SHA256File returns a 64-character lowercase hexadecimal SHA256 hash of a file's contents.
func SHA256File(path string) (string, error) {
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

// SHA256FileHash returns a 64-character hex string for a file, using the same
// approach as Python's hashlib.sha256(file_path.read_bytes()).hexdigest().
// This reads the entire file into memory, matching Python behavior exactly.
func SHA256FileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

// HashReader returns a 64-char hex SHA256 hash of data read from an io.Reader.
func HashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// VerifyChecksum verifies that a file's SHA256 hash matches the expected value.
func VerifyChecksum(path, expected string) error {
	actual, err := SHA256File(path)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}

// ── HashGenerator — content-addressed SHA256 hashes for domain resources ──
// Matches Python's mvmctl.utils.crypto.HashGenerator exactly.

type HashGenerator struct{}

// Image generates a 64-char SHA256 hash for an image.
// Python: data = f"{type_}:{source}:{timestamp}"
func (HashGenerator) Image(type_, source, timestamp string) string {
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%s:%s:%s", type_, source, timestamp)))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Kernel generates a 64-char SHA256 hash for a kernel.
// Python: file_hash = hashlib.sha256(file_path.read_bytes()).hexdigest()
//
//	data = f"{file_hash}:{version}:{arch}:{timestamp}"
func (HashGenerator) Kernel(filePath, version, arch, timestamp string) (string, error) {
	fileHash, err := SHA256FileHash(filePath)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%s:%s:%s:%s", fileHash, version, arch, timestamp)))
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// Binary generates a 64-char SHA256 hash for a binary.
// Python: file_hash = hashlib.sha256(file_path.read_bytes()).hexdigest()
//
//	data = f"{file_hash}:{name}:{version}"
func (HashGenerator) Binary(filePath, name, version string) (string, error) {
	fileHash, err := SHA256FileHash(filePath)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%s:%s:%s", fileHash, name, version)))
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// VM generates a 32-char SHA256 hash for a VM.
// Python uses [:32] truncation because VM IDs become filesystem paths
// that must stay under the Unix domain socket path limit (~108 bytes).
// Python: data = f"{name}:{created_at}"
func (HashGenerator) VM(name, createdAt string) string {
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%s:%s", name, createdAt)))
	full := fmt.Sprintf("%x", h.Sum(nil))
	if len(full) > 32 {
		return full[:32]
	}
	return full
}

// Network generates a 64-char SHA256 hash for a network.
// Python: data = f"{name}:{subnet}:{created_at}"
func (HashGenerator) Network(name, subnet, createdAt string) string {
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%s:%s:%s", name, subnet, createdAt)))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Volume generates a SHA256 hash for a volume.
// Python: data = f"{name}:{created_at}"
func (HashGenerator) Volume(name, createdAt string) string {
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%s:%s", name, createdAt)))
	return fmt.Sprintf("%x", h.Sum(nil))
}

// ── UUID v4 ──
// NOTE: There's a duplicate copy in pkg/api/host.go:885 using /dev/urandom directly.
// If modifying, update both.

// UUIDV4 generates a random UUID v4 string matching Python's str(uuid.uuid4()).
// Uses crypto/rand (Go standard library) for cryptographically secure random bytes,
// matching Python's uuid.uuid4() which also uses os.urandom.
// Format: "550e8400-e29b-41d4-a716-446655440000"
func UUIDV4() string {
	u := make([]byte, 16)
	rand.Read(u)
	// Set version 4 (UUID v4: 4-bit version in byte 6, top bits = 0100)
	u[6] = (u[6] & 0x0f) | 0x40
	// Set variant (RFC 4122: 2-bit variant in byte 8, top bits = 10)
	u[8] = (u[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// ── Shorten ──
// Shorten returns first N characters of a hash for display.
// Python: def shorten(full_hash: str, length: int = 12) -> str
func (HashGenerator) Shorten(fullHash string, length ...int) (string, error) {
	n := 12
	if len(length) > 0 {
		n = length[0]
	}
	if len(fullHash) < n {
		return "", fmt.Errorf("Hash '%s' is shorter than requested length %d", fullHash, n)
	}
	return fullHash[:n], nil
}
