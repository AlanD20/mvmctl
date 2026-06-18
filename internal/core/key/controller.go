package key

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// privateKeyPerm is the file permission for private key files (0600).
const privateKeyPerm os.FileMode = 0600

// Controller manages SSH key lifecycle for a specific key.
// Resolves the key eagerly at construction time and stores the resolved key for later use.
type Controller struct {
	key  *model.SSHKeyItem
	repo Repository
}

// NewController creates a KeyController bound to a resolved key.
func NewController(key *model.SSHKeyItem, repo Repository) *Controller {
	return &Controller{key: key, repo: repo}
}

// Export copies both public and private key files to a destination directory.
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
