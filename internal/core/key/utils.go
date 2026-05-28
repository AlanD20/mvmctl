package key

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"unicode"

	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
)

// ReadPubKeyContents extracts public key content strings from a list of SSHKeyItem.
// Matches Python's KeyService.read_pubkey_contents() (a @staticmethod).
func ReadPubKeyContents(keys []*model.SSHKeyItem) ([]string, error) {
	var contents []string
	for _, k := range keys {
		if k.PublicKeyPath == "" {
			continue
		}
		if _, err := os.Stat(k.PublicKeyPath); err == nil {
			data, err := os.ReadFile(k.PublicKeyPath)
			if err != nil {
				return nil, errs.KeyFileError(fmt.Sprintf("Failed to read public key file: %v", err))
			}
			contents = append(contents, strings.TrimSpace(string(data)))
		}
	}
	return contents, nil
}

// computeFingerprint computes SHA256 fingerprint from public key content.
// Matches Python's KeyService._compute_fingerprint() exactly:
//
//	base64 decode key bytes → SHA256 → base64 encode (no padding) → "SHA256:..."
func computeFingerprint(pubKeyContent string) (string, error) {
	parts := strings.Fields(pubKeyContent)
	if len(parts) < 2 {
		return "", &keyError{err: errs.MVMKeyError("Invalid public key format")}
	}
	keyBytes, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", &keyError{err: errs.MVMKeyError("Invalid public key format")}
	}
	digest := sha256.Sum256(keyBytes)
	fp := base64.StdEncoding.EncodeToString(digest[:])
	// Remove trailing padding, matching Python's rstrip(b"=")
	fp = strings.TrimRight(fp, "=")
	return "SHA256:" + fp, nil
}

// isPrivateKey checks if content contains a PEM-encoded private key header.
// TODO(verdict#33): belongs in infra/crypto or similar shared utility
func isPrivateKey(content string) bool {
	return strings.Contains(content, "-----BEGIN") && strings.Contains(content, "PRIVATE KEY-----")
}

// ParseAlgorithm extracts the algorithm (first field) from SSH public key content.
// Matches Python's KeyService._parse_algorithm() which raises MVMKeyError on empty content.
func ParseAlgorithm(pubKeyContent string) (string, error) {
	parts := strings.Fields(pubKeyContent)
	if len(parts) == 0 {
		return "", &keyError{err: errs.MVMKeyError("Invalid public key format")}
	}
	return parts[0], nil
}

// ParseComment extracts the comment (third+ field) from SSH public key content.
// Matches Python's KeyService._parse_comment() which uses split(None, 2),
// preserving internal whitespace in parts[2].
func ParseComment(pubKeyContent string) string {
	trimmed := strings.TrimSpace(pubKeyContent)
	fields := strings.Fields(trimmed)
	if len(fields) < 3 {
		return ""
	}
	// Python's split(None, 2) preserves original whitespace in parts[2].
	pos := 0
	for i := 0; i < 2; i++ {
		for pos < len(trimmed) && !unicode.IsSpace(rune(trimmed[pos])) {
			pos++
		}
		for pos < len(trimmed) && unicode.IsSpace(rune(trimmed[pos])) {
			pos++
		}
	}
	return trimmed[pos:]
}

// CreateParams holds parameters for key generation.
type CreateParams struct {
	Name       string
	Algorithm  string
	Bits       int
	Comment    string
	OutputDir  string
	Overwrite  bool
	SetDefault bool
}
