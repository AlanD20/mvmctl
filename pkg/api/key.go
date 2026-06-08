// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/key_operations.py exactly.
package api

import (
	"context"
	"fmt"
	"os"
	"strings"

	"mvmctl/internal/core/key"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/api/inputs"
	"mvmctl/pkg/api/responses"
	"mvmctl/pkg/errs"
)

// KeyListAll lists all SSH keys.
// Matches Python's KeyOperation.list_all() exactly — passes keys_dir only,
// no verify parameter (matching Python's service.list_all(keys_dir) call).
func (op *Operation) KeyListAll(ctx context.Context) ([]*model.SSHKeyItem, error) {
	return op.Services.Key.List(ctx, false)
}

// Get returns a single key by name or ID.
// Matches Python's KeyOperation.get() exactly — uses KeyRequest resolution pipeline.
func (op *Operation) KeyGet(ctx context.Context, input inputs.KeyInput) (*model.SSHKeyItem, error) {
	req := inputs.NewKeyRequest(input, op.Repos.Key)
	resolved, err := req.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	// Match Python: if len(resolved.keys) != 1: raise MVMKeyError(...)
	if len(resolved.Keys) != 1 {
		return nil, fmt.Errorf("Expected exactly one key, got %d", len(resolved.Keys))
	}
	return resolved.Keys[0], nil
}

// KeyCreate creates a new SSH keypair.
// Matches Python's KeyOperation.create() exactly — calls check_dependencies() first,
// then uses KeyCreateRequest resolution pipeline.
// Python wraps check_dependencies in try/except Exception — top-level panic recovery matches this.
func (op *Operation) KeyCreate(ctx context.Context, input inputs.KeyCreateInput) (*model.SSHKeyItem, error) {
	// Python: service.check_dependencies() called separately before resolution.
	// Go: CreateKeypair calls checkDependencies internally — no need to duplicate here.

	// Python: request = KeyCreateRequest(inputs=inputs); resolved = request.resolve()
	req := inputs.NewKeyCreateRequest(input)
	resolved, err := req.Resolve()
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
	// Python does inline validation at the API layer before calling service
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
// Matches Python's KeyOperation.remove() exactly — uses KeyRequest resolution pipeline.
func (op *Operation) KeyRemove(ctx context.Context, input inputs.KeyInput, force bool) *errs.BatchResult {
	// Match Python: KeyRequest(inputs=inputs, db=db).resolve()
	req := inputs.NewKeyRequest(input, op.Repos.Key)
	resolved, err := req.Resolve(ctx)
	if err != nil {
		return &errs.BatchResult{Items: []errs.OperationResult{{
			Status: "error", Code: "key.remove_failed", Message: err.Error(),
		}}}
	}

	results := make([]errs.OperationResult, 0)

	for _, key := range resolved.Keys {
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

		// File cleanup is done at the API layer before DB deletion (matching Python).
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
// Matches Python's KeyOperation.inspect() exactly — uses KeyRequest resolution,
// returns raw dict (not wrapped in OperationResult).
func (op *Operation) KeyInspect(ctx context.Context, input inputs.KeyInput) (*responses.KeyInspect, error) {
	k, err := op.KeyGet(ctx, input)
	if err != nil {
		return nil, err
	}
	return &responses.KeyInspect{
		Key: responses.KeyInfo{
			ID: k.ID, Name: k.Name, Fingerprint: k.Fingerprint,
			Algorithm: k.Algorithm, Comment: k.Comment,
			IsDefault: k.IsDefault, IsPresent: k.IsPresent,
		},
		Files: responses.KeyFilesInfo{
			PublicKeyPath:  k.PublicKeyPath,
			PrivateKeyPath: k.PrivateKeyPath,
		},
		Timestamps: responses.KeyTimestampsInfo{
			CreatedAt: k.CreatedAt,
			UpdatedAt: k.UpdatedAt,
		},
	}, nil
}

// KeyExport exports a keypair to a destination directory.
// Matches Python's KeyOperation.export() exactly — uses KeyRequest resolution
// and KeyController.export(). Python wraps controller.export() in try/except Exception.
func (op *Operation) KeyExport(
	ctx context.Context,
	input inputs.KeyInput,
	destination string,
	overwrite bool,
) ([]string, error) {
	// Python: request = KeyRequest(inputs=inputs, db=db); resolved = request.resolve()
	req := inputs.NewKeyRequest(input, op.Repos.Key)
	resolved, err := req.Resolve(ctx)
	if err != nil {
		return nil, errs.New(errs.CodeKeyExportFailed, fmt.Sprintf("Key not found: %s", err.Error()))
	}
	// Python: if len(resolved.keys) != 1: return error
	if len(resolved.Keys) != 1 {
		return nil, errs.New(
			errs.CodeKeyExportFailed,
			fmt.Sprintf("Expected exactly one key, got %d", len(resolved.Keys)),
		)
	}

	keyItem := resolved.Keys[0]

	// Use KeyController.export() matching Python:
	// controller = KeyController(resolved.keys[0], repo)
	ctrl := key.NewController(keyItem, op.Repos.Key)

	destPriv, destPub, err := ctrl.Export(ctx, destination, overwrite)
	if err != nil {
		return nil, errs.WrapMsg(errs.CodeKeyExportFailed, err.Error(), err)
	}

	return []string{destPriv, destPub}, nil
}

// KeySetDefaults sets one or more keys as default.
// Matches Python's KeyOperation.set_default() — uses KeyRequest resolution.
func (op *Operation) KeySetDefaults(ctx context.Context, input inputs.KeyInput) error {
	// Python: request = KeyRequest(inputs=inputs, db=db); resolved = request.resolve()
	req := inputs.NewKeyRequest(input, op.Repos.Key)
	resolved, err := req.Resolve(ctx)
	if err != nil || len(resolved.Keys) == 0 {
		return errs.New(errs.CodeKeyDefaultSetFailed, "Key not found")
	}

	// Pass resolved key items directly — SetDefaultKeys no longer re-lists the DB.
	if err := op.Services.Key.SetDefaults(ctx, resolved.Keys); err != nil {
		return errs.WrapMsg(errs.CodeKeyDefaultSetFailed, fmt.Sprintf("Failed to set default key: %v", err), err)
	}

	for _, k := range resolved.Keys {

		op.AuditLog.LogOperation("key.set_default", map[string]any{"name": k.Name}, "")
	}

	return nil
}

// KeyGetDefaults returns all default keys.
// Matches Python's KeyOperation.get_defaults() exactly.
func (op *Operation) KeyGetDefaults(ctx context.Context) ([]*model.SSHKeyItem, error) {
	return op.Repos.Key.GetDefaults(ctx)
}

// KeyClearDefaults clears all default keys.
// Matches Python's KeyOperation.clear_defaults() exactly.
// Python wraps service.clear_default_keys() in try/except Exception.
func (op *Operation) KeyClearDefaults(ctx context.Context) error {
	if err := op.Services.Key.ClearDefaultKeys(ctx); err != nil {
		return errs.WrapMsg(errs.CodeKeyDefaultsClearFailed, fmt.Sprintf("Failed to clear defaults: %v", err), err)
	}

	op.AuditLog.LogOperation("key.clear_defaults", nil, "")

	return nil
}
