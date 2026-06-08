package key

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/errs"
)

// privateKeyPerm matches Python's CONST_FILE_PERMS_PRIVATE_KEY = 0o600.
const privateKeyPerm os.FileMode = 0600

// Controller manages SSH key lifecycle for a specific key.
// Matches Python's KeyController exactly — resolves key eagerly at
// construction time and stores the resolved key for later use.
type Controller struct {
	key  *model.SSHKeyItem
	repo Repository
}

// NewController creates a new KeyController.
// If entity is an SSHKeyItem, it uses it directly.
// If entity is a string (name or ID prefix), it resolves it eagerly
// at construction time (matching Python's __init__ behavior).
func NewController(ctx context.Context, entity any, repo Repository) (*Controller, error) {
	var key *model.SSHKeyItem
	switch e := entity.(type) {
	case *model.SSHKeyItem:
		key = e
	case string:
		resolver := NewResolver(repo)
		var err error
		key, err = resolver.Resolve(ctx, e)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errs.NotFound(errs.CodeKeyNotFound, "invalid entity type")
	}
	return &Controller{key: key, repo: repo}, nil
}

// Export copies both public and private key files to a destination directory.
// Matches Python's KeyController.export() exactly.
// Uses the actual paths stored in the DB model, not reconstructed paths.
func (c *Controller) Export(ctx context.Context, destDir string, overwrite bool) (string, string, error) {
	_ = ctx // context is unused here, kept for API consistency

	sourcePublic := c.key.PublicKeyPath
	if sourcePublic == "" {
		return "", "", errs.New(errs.CodeKeyExportFailed,
			"Public key path not set for '"+c.key.Name+"'",
			errs.WithEntity(c.key.Name))
	}
	sourcePrivate := ""
	if c.key.PrivateKeyPath != nil {
		sourcePrivate = *c.key.PrivateKeyPath
	}

	if _, err := os.Stat(sourcePublic); os.IsNotExist(err) {
		return "", "", errs.New(errs.CodeKeyExportFailed,
			"Public key not found at '"+sourcePublic+"'",
			errs.WithEntity(c.key.Name))
	}
	if sourcePrivate != "" {
		if _, err := os.Stat(sourcePrivate); os.IsNotExist(err) {
			return "", "", errs.New(errs.CodeKeyExportFailed,
				"Private key not found at '"+sourcePrivate+"'",
				errs.WithEntity(c.key.Name))
		}
	}

	if err := os.MkdirAll(destDir, os.ModePerm); err != nil {
		return "", "", err
	}

	destPrivate := filepath.Join(destDir, c.key.Name)
	destPublic := filepath.Join(destDir, c.key.Name+".pub")

	if !overwrite {
		var existing []string
		if _, err := os.Stat(destPrivate); err == nil {
			existing = append(existing, destPrivate)
		}
		if _, err := os.Stat(destPublic); err == nil {
			existing = append(existing, destPublic)
		}
		if len(existing) > 0 {
			return "", "", errs.New(errs.CodeKeyExportFailed,
				"Key file(s) already exist: "+strings.Join(existing, ", ")+". Use --overwrite to replace.")
		}
	}

	if sourcePrivate != "" {
		if err := infra.CopyPreservingMetadata(sourcePrivate, destPrivate); err != nil {
			return "", "", fmt.Errorf("copy private key: %w", err)
		}
		if err := os.Chmod(destPrivate, privateKeyPerm); err != nil {
			return "", "", fmt.Errorf("chmod private key: %w", err)
		}
	}

	if err := infra.CopyPreservingMetadata(sourcePublic, destPublic); err != nil {
		return "", "", fmt.Errorf("copy public key: %w", err)
	}

	return destPrivate, destPublic, nil
}
