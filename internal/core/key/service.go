package key

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
)

// Service provides stateless SSH key operations.
// Matches Python's KeyService exactly — stores repo and keysDir.
type Service struct {
	repo    Repository
	keysDir string
}

// NewService creates a KeyService.
func NewService(repo Repository, keysDir string) *Service {
	return &Service{
		repo:    repo,
		keysDir: keysDir,
	}
}

// readPubKeyFile reads and validates a public key file.
func (s *Service) readPubKeyFile(path string) (string, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", &keyError{err: errs.KeyFileError(fmt.Sprintf("Public key file not found: %s", path))}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", &keyError{err: errs.KeyFileError(fmt.Sprintf("Failed to read public key file: %v", err))}
	}
	trimmed := strings.TrimSpace(string(content))
	if trimmed == "" {
		return "", &keyError{err: errs.KeyFileError(fmt.Sprintf("Public key file is empty: %s", path))}
	}
	return trimmed, nil
}

// checkDependencies checks that ssh-keygen is available.
func (s *Service) checkDependencies() error {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		return &keyError{err: &errs.DomainError{
			Code:    errs.CodeKeyDependencyMissing,
			Op:      "key",
			Message: "ssh-keygen not found in PATH. Install OpenSSH client package (e.g., 'apt install openssh-client').",
			Class:   errs.ClassValidation,
		}}
	}
	return nil
}

// persistPublicKey writes a public key to disk as {keysDir}/{name}.pub.
func (s *Service) persistPublicKey(name, pubKeyContent, keysDir string) (string, error) {
	if err := os.MkdirAll(keysDir, os.ModePerm); err != nil {
		return "", fmt.Errorf("create keys dir: %w", err)
	}
	pubPath := filepath.Join(keysDir, name+".pub")
	if err := os.WriteFile(pubPath, []byte(pubKeyContent+"\n"), 0666); err != nil {
		return "", &keyError{err: errs.KeyFileError(fmt.Sprintf("Failed to write public key file: %v", err))}
	}
	return pubPath, nil
}

// generateKeypair generates an SSH key pair using ssh-keygen subprocess.
func (s *Service) generateKeypair(ctx context.Context, privateKeyPath, pubKeyPath, comment, algorithm string, bits int) (string, error) {
	args := []string{"-t", algorithm, "-f", privateKeyPath, "-N", "", "-C", comment}
	if algorithm == "rsa" {
		if bits <= 0 {
			bits = 4096
		}
		args = append(args, "-b", strconv.Itoa(bits))
	}
	result := system.RunCmdCompat(ctx, append([]string{"ssh-keygen"}, args...), system.DefaultRunCmdOptions())
	if result.Err != nil {
		return "", &keyError{err: errs.MVMKeyError(fmt.Sprintf("ssh-keygen failed: %s", strings.TrimSpace(result.Stderr)))}
	}

	pubContent, err := os.ReadFile(pubKeyPath)
	if err != nil {
		return "", &keyError{err: errs.KeyFileError(fmt.Sprintf("Failed to read generated public key: %v", err))}
	}
	return strings.TrimSpace(string(pubContent)), nil
}

// CreateKeypair generates a new SSH keypair.
// Matches Python's KeyService.create_keypair() exactly.
func (s *Service) CreateKeypair(ctx context.Context, params *CreateParams) (*model.SSHKeyItem, string, error) {
	if err := s.checkDependencies(); err != nil {
		return nil, "", err
	}

	outputDir := params.OutputDir
	if err := os.MkdirAll(outputDir, os.ModePerm); err != nil {
		return nil, "", fmt.Errorf("create output dir: %w", err)
	}

	privateKeyPath := filepath.Join(outputDir, params.Name)
	pubKeyPath := filepath.Join(outputDir, params.Name+".pub")

	// Check for existing key
	existing, _ := s.repo.GetByName(ctx, params.Name)
	if existing != nil {
		if params.Overwrite {
			if err := s.repo.Delete(ctx, existing.ID); err != nil {
				return nil, "", err
			}
		} else {
			return nil, "", &keyError{err: errs.MVMKeyError(
				fmt.Sprintf("Key '%s' already exists in cache. Remove it first.", params.Name),
			)}
		}
	}

	comment := params.Comment
	if comment == "" {
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "localhost"
		}
		comment = fmt.Sprintf("%s@%s", params.Name, hostname)
	}

	// Remove existing files if overwriting
	if params.Overwrite {
		os.Remove(privateKeyPath)
		os.Remove(pubKeyPath)
	}

	// Generate keypair via ssh-keygen subprocess
	pubContent, err := s.generateKeypair(ctx, privateKeyPath, pubKeyPath, comment, params.Algorithm, params.Bits)
	if err != nil {
		return nil, "", err
	}

	// persist public key (redundant write, matching Python behavior)
	s.persistPublicKey(params.Name, pubContent, outputDir)

	fingerprint, err := computeFingerprint(pubContent)
	if err != nil {
		return nil, "", err
	}

	alg, err := ParseAlgorithm(pubContent)
	if err != nil {
		return nil, "", err
	}

	now := time.Now().Format(time.RFC3339)
	privPathStr := privateKeyPath
	sshKey := &model.SSHKeyItem{
		ID:             fingerprint,
		Name:           params.Name,
		Fingerprint:    fingerprint,
		Algorithm:      alg,
		Comment:        ParseComment(pubContent),
		PrivateKeyPath: &privPathStr,
		PublicKeyPath:  pubKeyPath,
		IsDefault:      params.SetDefault,
		IsPresent:      true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := s.repo.Upsert(ctx, sshKey); err != nil {
		return nil, "", fmt.Errorf("upsert key: %w", err)
	}

	return sshKey, privateKeyPath, nil
}

// AddKey imports a public key into the cache.
func (s *Service) AddKey(ctx context.Context, name, pubKeyPath, pubKeyContent string, overwrite bool) (*model.SSHKeyItem, error) {
	// Check for existing key by name
	existing, _ := s.repo.GetByName(ctx, name)
	if existing != nil {
		if overwrite {
			oldPub := filepath.Join(s.keysDir, name+".pub")
			if _, err := os.Stat(oldPub); err == nil {
				os.Remove(oldPub)
			}
			if err := s.repo.Delete(ctx, existing.ID); err != nil {
				return nil, err
			}
		} else {
			return nil, &keyError{err: errs.MVMKeyError(
				fmt.Sprintf("Key '%s' already exists. Remove it first to replace.", name),
			)}
		}
	}

	// Persist public key to keys dir
	persistedPubPath, err := s.persistPublicKey(name, pubKeyContent, s.keysDir)
	if err != nil {
		return nil, err
	}

	// Discover private key: same stem without .pub suffix
	privateKeyPath := strings.TrimSuffix(pubKeyPath, ".pub")
	if privateKeyPath == pubKeyPath {
		privateKeyPath = strings.Replace(pubKeyPath, ".pub", "", 1)
	}
	privateKeyExists := false
	if _, err := os.Stat(privateKeyPath); err == nil && privateKeyPath != pubKeyPath {
		privateKeyExists = true
	}

	fingerprint, err := computeFingerprint(pubKeyContent)
	if err != nil {
		return nil, err
	}

	alg, err := ParseAlgorithm(pubKeyContent)
	if err != nil {
		return nil, err
	}

	now := time.Now().Format(time.RFC3339)
	sshKey := &model.SSHKeyItem{
		ID:            fingerprint,
		Name:          name,
		Fingerprint:   fingerprint,
		Algorithm:     alg,
		Comment:       ParseComment(pubKeyContent),
		PublicKeyPath: persistedPubPath,
		IsDefault:     false,
		IsPresent:     true,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if privateKeyExists {
		sshKey.PrivateKeyPath = &privateKeyPath
	}

	if err := s.repo.Upsert(ctx, sshKey); err != nil {
		return nil, fmt.Errorf("upsert key: %w", err)
	}

	return sshKey, nil
}

// GetPubkey returns the public key content for a key.
func (s *Service) GetPubkey(ctx context.Context, entity interface{}) (string, error) {
	switch e := entity.(type) {
	case *model.SSHKeyItem:
		pubPath := filepath.Join(s.keysDir, e.Name+".pub")
		return s.readPubKeyFile(pubPath)
	case string:
		sshKey, err := s.repo.GetByName(ctx, e)
		if err != nil {
			return "", err
		}
		if sshKey == nil {
			return "", &keyError{err: errs.MVMKeyError(fmt.Sprintf("Key '%s' not found in cache", e))}
		}
		pubPath := filepath.Join(s.keysDir, sshKey.Name+".pub")
		return s.readPubKeyFile(pubPath)
	default:
		return "", &keyError{err: errs.KeyFileError("Invalid key identifier")}
	}
}

// GetPubkeys returns public key contents for multiple keys.
func (s *Service) GetPubkeys(ctx context.Context, keys interface{}) ([]string, error) {
	switch k := keys.(type) {
	case []string:
		contents := make([]string, 0, len(k))
		for _, name := range k {
			content, err := s.GetPubkey(ctx, name)
			if err != nil {
				return nil, err
			}
			contents = append(contents, content)
		}
		return contents, nil
	case []*model.SSHKeyItem:
		contents := make([]string, 0, len(k))
		for _, key := range k {
			content, err := s.GetPubkey(ctx, key)
			if err != nil {
				return nil, err
			}
			contents = append(contents, content)
		}
		return contents, nil
	default:
		return nil, &keyError{err: errs.KeyFileError("invalid keys type: expected []string or []*SSHKeyItem")}
	}
}

// List returns all keys in the cache, optionally verifying filesystem presence.
func (s *Service) List(ctx context.Context, verify bool) ([]*model.SSHKeyItem, error) {
	keys, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	if !verify {
		return keys, nil
	}

	var missingIDs []string
	for _, key := range keys {
		pubPath := filepath.Join(s.keysDir, key.Name+".pub")
		if _, err := os.Stat(pubPath); os.IsNotExist(err) {
			missingIDs = append(missingIDs, key.ID)
		}
	}

	if len(missingIDs) > 0 {
		if err := s.repo.UpdateManyIsPresent(ctx, missingIDs, false); err != nil {
			return nil, err
		}
		keys, err = s.repo.List(ctx)
		if err != nil {
			return nil, err
		}
	}

	return keys, nil
}

// SetDefaultKeys sets multiple keys as default.
// Does NOT clear other defaults — matches Python's KeyService.set_default_keys().
// Silently skips non-existent keys (matching Python behavior: no warning, no error).
func (s *Service) SetDefaultKeys(ctx context.Context, names []string) error {
	allKeys, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	nameToKey := make(map[string]*model.SSHKeyItem, len(allKeys))
	for _, k := range allKeys {
		nameToKey[k.Name] = k
	}

	for _, name := range names {
		if k, ok := nameToKey[name]; ok {
			if err := s.repo.SetDefault(ctx, k.ID); err != nil {
				return err
			}
		}
		// Non-existent keys are silently skipped (matching Python exactly)
	}

	slog.Info("Set default SSH keys", "names", strings.Join(names, ", "))
	return nil
}

// ClearDefaultKeys clears all default SSH keys.
func (s *Service) ClearDefaultKeys(ctx context.Context) error {
	slog.Info("Cleared default SSH keys")
	return s.repo.ClearDefaults(ctx)
}


