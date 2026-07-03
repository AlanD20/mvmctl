// Package api provides the public orchestration layer for all operations.
package api

import (
	"context"
	"fmt"
	"mvmctl/internal/core/key"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/results"
	"mvmctl/pkg/errs"
	"os"
	"strings"
)

// KeyAPI defines the public interface for SSH key operations.
type KeyAPI interface {
	KeyListAll(ctx context.Context) ([]*model.SSHKeyItem, error)
	KeyGet(ctx context.Context, input inputs.KeyInput) (*model.SSHKeyItem, error)
	KeyCreate(ctx context.Context, input inputs.KeyCreateInput) (*model.SSHKeyItem, error)
	KeyImport(ctx context.Context, input inputs.KeyImportInput) (*model.SSHKeyItem, error)
	KeyRemove(ctx context.Context, input inputs.KeyInput, force bool) *errs.BatchResult
	KeyInspect(ctx context.Context, input inputs.KeyInput) (*results.KeyInspect, error)
	KeyExport(ctx context.Context, input inputs.KeyInput, destination string, overwrite bool) ([]string, error)
	KeySetDefaults(ctx context.Context, input inputs.KeyInput) error
	KeyGetDefaults(ctx context.Context) ([]*model.SSHKeyItem, error)
	KeyClearDefaults(ctx context.Context) error
}

// KeyListAll lists all SSH keys.
// passes keys_dir only,
// no verify parameter call).
func (op *Operation) KeyListAll(ctx context.Context) ([]*model.SSHKeyItem, error) {
	return op.Services.Key.List(ctx, false)
}

// Get returns a single key by name or ID.
// uses KeyInput.Resolve pipeline.
func (op *Operation) KeyGet(ctx context.Context, input inputs.KeyInput) (*model.SSHKeyItem, error) {
	keys, err := input.Resolve(ctx, op.Repos.Key)
	if err != nil {
		return nil, err
	}
	// Validate exactly one key matched
	if len(keys) != 1 {
		return nil, fmt.Errorf("expected exactly one key, got %d", len(keys))
	}
	return keys[0], nil
}

// KeyCreate creates a new SSH keypair.
// Calls checkDependencies first, then uses KeyCreateInput.Resolve pipeline.
// Top-level panic recovery catches unexpected errors.
func (op *Operation) KeyCreate(ctx context.Context, input inputs.KeyCreateInput) (*model.SSHKeyItem, error) {
	// CreateKeypair calls checkDependencies internally — no need to duplicate here.
	resolved, err := input.Resolve()
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeKeyCreateFailed, err.Error(), err)
	}
	// Use resolved fields (name, algorithm, output_dir, comment, etc.)
	bits := 0
	if resolved.Bits != nil {
		bits = *resolved.Bits
	}
	params := &key.CreateParams{
		Name:       resolved.Name,
		Algorithm:  resolved.Algorithm,
		Bits:       bits,
		OutputDir:  resolved.OutputDir,
		Comment:    resolved.Comment,
		Overwrite:  resolved.Overwrite,
		SetDefault: resolved.SetDefault,
	}
	keyItem, err := op.Services.Key.CreateKeypair(ctx, params)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeKeyCreateFailed, fmt.Sprintf("Key creation failed: %v", err), err)
	}
	op.AuditLog.LogOperation("key.create", map[string]any{
		"name":      keyItem.Name,
		"algorithm": keyItem.Algorithm,
	}, "")
	return keyItem, nil
}

// KeyImport imports an existing public key to the cache.
func (op *Operation) KeyImport(ctx context.Context, input inputs.KeyImportInput) (*model.SSHKeyItem, error) {
	// Does inline validation at the API layer before calling service
	if _, err := os.Stat(input.PubKeyPath); os.IsNotExist(err) {
		return nil, errs.New(errs.CodeKeyAddFailed, fmt.Sprintf("Public key file not found: %s", input.PubKeyPath))
	}
	data, err := os.ReadFile(input.PubKeyPath)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeKeyAddFailed, fmt.Sprintf("Failed to read public key file: %v", err), err)
	}
	pubKeyContent := strings.TrimSpace(string(data))
	if pubKeyContent == "" {
		return nil, errs.New(errs.CodeKeyAddFailed, fmt.Sprintf("Public key file is empty: %s", input.PubKeyPath))
	}
	// Detect if user accidentally passed a private key file
	if key.IsPrivateKey(pubKeyContent) {
		altPath := input.PubKeyPath + ".pub"
		if _, err := os.Stat(altPath); err == nil {
			return nil, errs.New(
				errs.CodeKeyAddFailed,
				fmt.Sprintf(
					"'%s' looks like a private key.\nUse the public key instead: mvm key import %s %s",
					input.PubKeyPath,
					input.Name,
					altPath,
				),
			)
		}
		return nil, errs.New(
			errs.CodeKeyAddFailed,
			fmt.Sprintf(
				"'%s' looks like a private key.\nPass the corresponding .pub file instead: mvm key import %s <path>.pub",
				input.PubKeyPath,
				input.Name,
			),
		)
	}
	keyItem, err := op.Services.Key.Import(
		ctx,
		input.Name,
		input.PubKeyPath,
		pubKeyContent,
		input.Overwrite,
		input.SetDefault,
	)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeKeyAddFailed, fmt.Sprintf("Failed to add key: %v", err), err)
	}
	op.AuditLog.LogOperation("key.add", map[string]any{"name": keyItem.Name}, "")
	return keyItem, nil
}

// KeyRemove removes keys by name or ID.
// uses KeyInput.Resolve pipeline.
func (op *Operation) KeyRemove(ctx context.Context, input inputs.KeyInput, force bool) *errs.BatchResult {
	keys, err := input.Resolve(ctx, op.Repos.Key)
	if err != nil {
		return &errs.BatchResult{Items: []errs.OperationResult{{
			Status: "error", Code: "key.remove_failed", Message: err.Error(), Exception: err,
		}}}
	}
	results := make([]errs.OperationResult, 0)
	for _, key := range keys {
		// Check if any VMs reference this key
		vms, _ := op.Repos.VM.FindBySSHKeyID(ctx, key.ID)
		if len(vms) > 0 && !force {
			vmNames := make([]string, len(vms))
			for i, vm := range vms {
				vmNames[i] = vm.Name
			}
			results = append(results, errs.OperationResult{
				Status: "error",
				Code:   "key.remove_failed",
				Message: fmt.Sprintf(
					"Key '%s' is used by VM(s): %s. Use --force to remove anyway.",
					key.Name,
					strings.Join(vmNames, ", "),
				),
			})
			continue
		}
		// File cleanup is done at the API layer before DB deletion.
		// Use the actual paths from the DB, not reconstructed ones.
		if key.PublicKeyPath != "" {
			os.Remove(key.PublicKeyPath)
		}
		if key.PrivateKeyPath != nil && *key.PrivateKeyPath != "" {
			os.Remove(*key.PrivateKeyPath)
		}
		if err := op.Repos.Key.Delete(ctx, key.ID); err != nil {
			results = append(results, errs.OperationResult{
				Status:    "error",
				Code:      "key.remove_failed",
				Message:   fmt.Sprintf("Failed to remove key '%s': %v", key.Name, err),
				Exception: err,
			})
			continue
		}
		op.AuditLog.LogOperation("key.remove", map[string]any{"name": key.Name}, "")
		results = append(results, errs.OperationResult{
			Status: "success",
			Code:   "key.removed",
			Item:   key,
		})
	}
	return &errs.BatchResult{Items: results}
}

// KeyInspect returns detailed key info.
func (op *Operation) KeyInspect(ctx context.Context, input inputs.KeyInput) (*results.KeyInspect, error) {
	k, err := op.KeyGet(ctx, input)
	if err != nil {
		return nil, err
	}
	return &results.KeyInspect{
		Key: results.KeyInfo{
			ID: k.ID, Name: k.Name, Fingerprint: k.Fingerprint,
			Algorithm: k.Algorithm, Comment: k.Comment,
			IsDefault: k.IsDefault, IsPresent: k.IsPresent,
		},
		Files: results.KeyFilesInfo{
			PublicKeyPath:  k.PublicKeyPath,
			PrivateKeyPath: k.PrivateKeyPath,
		},
		Timestamps: results.KeyTimestampsInfo{
			CreatedAt: k.CreatedAt,
			UpdatedAt: k.UpdatedAt,
		},
	}, nil
}

// KeyExport exports a keypair to a destination directory.
// Uses KeyInput.Resolve and KeyController.export().
func (op *Operation) KeyExport(
	ctx context.Context,
	input inputs.KeyInput,
	destination string,
	overwrite bool,
) ([]string, error) {
	keys, err := input.Resolve(ctx, op.Repos.Key)
	if err != nil {
		return nil, errs.New(errs.CodeKeyExportFailed, fmt.Sprintf("Key not found: %s", err.Error()))
	}
	if len(keys) != 1 {
		return nil, errs.New(
			errs.CodeKeyExportFailed,
			fmt.Sprintf("expected exactly one key, got %d", len(keys)),
		)
	}
	keyItem := keys[0]
	// Export via KeyController.
	ctrl := key.NewController(keyItem, op.Repos.Key)
	destPriv, destPub, err := ctrl.Export(ctx, destination, overwrite)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeKeyExportFailed, err.Error(), err)
	}
	return []string{destPriv, destPub}, nil
}

// KeySetDefaults sets one or more keys as default.
// uses KeyInput.Resolve pipeline.
func (op *Operation) KeySetDefaults(ctx context.Context, input inputs.KeyInput) error {
	keys, err := input.Resolve(ctx, op.Repos.Key)
	if err != nil || len(keys) == 0 {
		return errs.New(errs.CodeKeyDefaultSetFailed, "Key not found")
	}
	// Pass resolved key items directly — SetDefaultKeys no longer re-lists the DB.
	if err := op.Services.Key.SetDefaults(ctx, keys); err != nil {
		return errs.WrapMsg(errs.CodeKeyDefaultSetFailed, fmt.Sprintf("Failed to set default key: %v", err), err)
	}
	for _, k := range keys {
		op.AuditLog.LogOperation("key.set_default", map[string]any{"name": k.Name}, "")
	}
	return nil
}

// KeyGetDefaults returns all default keys.
func (op *Operation) KeyGetDefaults(ctx context.Context) ([]*model.SSHKeyItem, error) {
	return op.Repos.Key.GetDefaults(ctx)
}

// KeyClearDefaults clears all default keys.
func (op *Operation) KeyClearDefaults(ctx context.Context) error {
	if err := op.Services.Key.ClearDefaultKeys(ctx); err != nil {
		return errs.WrapMsg(errs.CodeKeyDefaultsClearFailed, fmt.Sprintf("Failed to clear defaults: %v", err), err)
	}
	op.AuditLog.LogOperation("key.clear_defaults", nil, "")
	return nil
}
