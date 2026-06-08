package key

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"unicode"

	"mvmctl/internal/lib/model"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

// readPubKeyFile reads and validates a public key file.
func readPubKeyFile(path string) (string, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", errs.New(errs.CodeKeyError, fmt.Sprintf("Public key file not found: %s", path))
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", errs.New(errs.CodeKeyError, fmt.Sprintf("Failed to read public key file: %v", err))
	}
	trimmed := strings.TrimSpace(string(content))
	if trimmed == "" {
		return "", errs.New(errs.CodeKeyError, fmt.Sprintf("Public key file is empty: %s", path))
	}
	return trimmed, nil
}

// checkDependencies checks that ssh-keygen is available.
func checkDependencies() error {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		return errs.New(errs.CodeKeyDependencyMissing,
			"ssh-keygen is not installed. Install openssh-client (apt install openssh-client / brew install openssh).")
	}
	return nil
}

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
				return nil, errs.New(errs.CodeKeyError, fmt.Sprintf("Failed to read public key file: %v", err))
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
		return "", errs.New(errs.CodeKeyError, "Invalid public key format")
	}
	keyBytes, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", errs.New(errs.CodeKeyError, "Invalid public key format")
	}
	digest := sha256.Sum256(keyBytes)
	fp := base64.StdEncoding.EncodeToString(digest[:])
	// Remove trailing padding, matching Python's rstrip(b"=")
	fp = strings.TrimRight(fp, "=")
	return "SHA256:" + fp, nil
}

// IsPrivateKey checks if content contains a PEM-encoded private key header.
func IsPrivateKey(content string) bool {
	return strings.Contains(content, "-----BEGIN") && strings.Contains(content, "PRIVATE KEY-----")
}

// ParseAlgorithm extracts the algorithm (first field) from SSH public key content.
// Matches Python's KeyService._parse_algorithm() which raises MVMKeyError on empty content.
func ParseAlgorithm(pubKeyContent string) (string, error) {
	parts := strings.Fields(pubKeyContent)
	if len(parts) == 0 {
		return "", errs.New(errs.CodeKeyError, "Invalid public key format")
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
	for range 2 {
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

// generateKeypair generates an SSH key pair using ssh-keygen subprocess.
func generateKeypair(
	ctx context.Context,
	privateKeyPath, pubKeyPath, comment, algorithm string,
	bits int,
) (string, error) {
	args := []string{"-t", algorithm, "-f", privateKeyPath, "-N", "", "-C", comment}
	if algorithm == "rsa" {
		if bits <= 0 {
			bits = 4096
		}
		args = append(args, "-b", strconv.Itoa(bits))
	}
	result := system.RunCmdCompat(ctx, append([]string{"ssh-keygen"}, args...), system.DefaultRunCmdOpts())
	if result.Err != nil {
		return "", errs.New(errs.CodeKeyError, fmt.Sprintf("ssh-keygen failed: %s", strings.TrimSpace(result.Stderr)))
	}

	pubContent, err := os.ReadFile(pubKeyPath)
	if err != nil {
		return "", errs.New(errs.CodeKeyError, fmt.Sprintf("Failed to read generated public key: %v", err))
	}
	return strings.TrimSpace(string(pubContent)), nil
}
