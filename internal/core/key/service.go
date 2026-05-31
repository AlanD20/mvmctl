package key

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/model"
)

// publicKeyPerm matches Python's 0o666 for public key files.
const publicKeyPerm os.FileMode = 0666

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

// CreateKeypair generates a new SSH keypair.
// Matches Python's KeyService.create_keypair() exactly.
func (s *Service) CreateKeypair(ctx context.Context, params *CreateParams) (*model.SSHKeyItem, error) {
	if err := checkDependencies(); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(params.OutputDir, os.ModePerm); err != nil {
		return nil, fmt.Errorf("create output dir: %w", err)
	}

	privateKeyPath := filepath.Join(params.OutputDir, params.Name)
	pubKeyPath := filepath.Join(params.OutputDir, params.Name+".pub")

	// Check for existing key
	existing, _ := s.repo.GetByName(ctx, params.Name)
	if existing != nil {
		if params.Overwrite {
			if err := s.repo.Delete(ctx, existing.ID); err != nil {
				return nil, err
			}
		} else {
			return nil, &keyError{err: errs.MVMKeyError(
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
	pubContent, err := generateKeypair(ctx, privateKeyPath, pubKeyPath, comment, params.Algorithm, params.Bits)
	if err != nil {
		return nil, err
	}

	// persist public key (redundant write, matching Python behavior)
	os.WriteFile(pubKeyPath, []byte(pubContent+"\n"), publicKeyPerm)

	fingerprint, err := computeFingerprint(pubContent)
	if err != nil {
		return nil, err
	}

	alg, err := ParseAlgorithm(pubContent)
	if err != nil {
		return nil, err
	}

	now := time.Now().Format(time.RFC3339)
	sshKey := &model.SSHKeyItem{
		ID:             fingerprint,
		Name:           params.Name,
		Fingerprint:    fingerprint,
		Algorithm:      alg,
		Comment:        ParseComment(pubContent),
		PrivateKeyPath: &privateKeyPath,
		PublicKeyPath:  pubKeyPath,
		IsDefault:      params.SetDefault,
		IsPresent:      true,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := s.repo.Upsert(ctx, sshKey); err != nil {
		return nil, fmt.Errorf("upsert key: %w", err)
	}

	return sshKey, nil
}

// Import imports a public key into the cache.
func (s *Service) Import(
	ctx context.Context,
	name, pubKeyPath, pubKeyContent string,
	overwrite, setDefault bool,
) (*model.SSHKeyItem, error) {
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
	pubPath := filepath.Join(s.keysDir, name+".pub")
	if err := os.WriteFile(pubPath, []byte(pubKeyContent+"\n"), publicKeyPerm); err != nil {
		return nil, &keyError{err: errs.KeyFileError(fmt.Sprintf("Failed to write public key file: %v", err))}
	}
	persistedPubPath := pubPath

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
		IsDefault:     setDefault,
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
func (s *Service) GetPubkey(ctx context.Context, entity any) (string, error) {
	switch e := entity.(type) {
	case *model.SSHKeyItem:
		return readPubKeyFile(e.PublicKeyPath)
	case string:
		resolver := NewResolver(s.repo)
		sshKey, err := resolver.Resolve(ctx, e)
		if err != nil {
			return "", err
		}
		return readPubKeyFile(sshKey.PublicKeyPath)
	default:
		return "", &keyError{err: errs.KeyFileError("Invalid key identifier")}
	}
}

// GetPubkeys returns public key contents for multiple keys.
func (s *Service) GetPubkeys(ctx context.Context, keys any) ([]string, error) {
	switch k := keys.(type) {
	case []string:
		// Use ResolveMany to handle names, ID prefixes, and .pub file paths.
		resolver := NewResolver(s.repo)
		result, err := resolver.ResolveMany(ctx, k)
		if err != nil {
			return nil, err
		}
		if len(result.Errors) > 0 && len(result.Items) == 0 {
			return nil, &keyError{err: errs.MVMKeyError(result.Errors[0])}
		}
		contents := make([]string, 0, len(result.Items))
		for _, key := range result.Items {
			content, err := readPubKeyFile(key.PublicKeyPath)
			if err != nil {
				return nil, err
			}
			contents = append(contents, content)
		}
		// If there were partial errors, log them but still return the content we have.
		for _, errMsg := range result.Errors {
			slog.Warn("SSH key resolution", "error", errMsg)
		}
		return contents, nil
	case []*model.SSHKeyItem:
		contents := make([]string, 0, len(k))
		for _, key := range k {
			content, err := readPubKeyFile(key.PublicKeyPath)
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
		if _, err := os.Stat(key.PublicKeyPath); os.IsNotExist(err) {
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

// SetDefaults sets multiple keys as default.
// Does NOT clear other defaults — matches Python's KeyService.set_default_keys().
func (s *Service) SetDefaults(ctx context.Context, keys []*model.SSHKeyItem) error {
	for _, k := range keys {
		if err := s.repo.SetDefault(ctx, k.ID); err != nil {
			return err
		}
	}

	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = k.Name
	}
	slog.Info("Set default SSH keys", "names", strings.Join(names, ", "))
	return nil
}

// ClearDefaultKeys clears all default SSH keys.
func (s *Service) ClearDefaultKeys(ctx context.Context) error {
	slog.Info("Cleared default SSH keys")
	return s.repo.ClearDefaults(ctx)
}
