// Package api provides the public orchestration layer for all operations.
// Matches src/mvmctl/api/key_operations.py exactly.
package api

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mvmctl/internal/core/key"
	"mvmctl/internal/infra/errs"
	"mvmctl/internal/infra/logging"
	"mvmctl/internal/infra/model"
	"mvmctl/pkg/api/inputs"
)

// KeyCreateInput holds options for key creation.
type KeyCreateInput struct {
	Name       string
	Algorithm  string
	Bits       int
	OutputDir  string
	Comment    string
	Overwrite  bool
	SetDefault bool
}

// KeyAddInput holds options for adding an existing key.
type KeyAddInput struct {
	Name          string
	PubKeyPath    string
	PubKeyContent string
	Overwrite     bool
}

// KeyInput matches Python's KeyInput(name: list[str], id: list[str]).
// Used for identifying existing SSH keys.
type KeyInput struct {
	Names []string
	IDs   []string
}

// resolveKeys resolves KeyInput identifiers via KeyResolver, matching Python's
// KeyRequest.resolve() path.
func (op *Operation) resolveKeys(ctx context.Context, input *KeyInput) ([]*model.SSHKeyItem, []string) {
	identifiers := append(input.Names, input.IDs...)
	if len(identifiers) == 0 {
		return nil, []string{"No key identifiers provided"}
	}

	resolver := key.NewResolver(op.Repos.Key)
	result, err := resolver.ResolveMany(ctx, identifiers)
	if err != nil {
		return nil, []string{err.Error()}
	}

	return result.Items, result.Errors
}

// KeyListAll lists all SSH keys.
// Matches Python's KeyOperation.list_all() exactly — passes keys_dir only,
// no verify parameter (matching Python's service.list_all(keys_dir) call).
func (op *Operation) KeyListAll(ctx context.Context) ([]*model.SSHKeyItem, error) {
	return op.Services.Key.List(ctx, false)
}

// Get returns a single key by name or ID.
// Matches Python's KeyOperation.get() exactly — uses KeyRequest resolution pipeline
// with KeyInput matching Python's KeyInput(name, id) pattern.
func (op *Operation) Get(ctx context.Context, input *KeyInput) (*model.SSHKeyItem, error) {
	// Match Python: KeyRequest(inputs=inputs, db=db).resolve()
	items, errs := op.resolveKeys(ctx, input)
	if len(items) == 0 {
		msg := "key not found"
		if len(errs) > 0 {
			msg = strings.Join(errs, "; ")
		}
		return nil, fmt.Errorf("key not found: %s", msg)
	}
	// Match Python: if len(resolved.keys) != 1: raise MVMKeyError(...)
	if len(items) != 1 {
		return nil, fmt.Errorf("Expected exactly one key, got %d", len(items))
	}
	return items[0], nil
}

// KeyCreate creates a new SSH keypair.
// Matches Python's KeyOperation.create() exactly — calls check_dependencies() first,
// then uses KeyCreateRequest resolution pipeline.
// Python wraps check_dependencies in try/except Exception — top-level panic recovery matches this.
func (op *Operation) KeyCreate(ctx context.Context, input *KeyCreateInput) *errs.OperationResult {
	// Python: service.check_dependencies() called separately before resolution.
	// Go: CreateKeypair calls checkDependencies internally, but we call it explicitly
	// to match Python's exact ordering (check happens before resolution).
	if err := op.checkDependencies(); err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "key.create_failed",
			Message:   err.Error(),
			Exception: err,
		}
	}

	// Python: request = KeyCreateRequest(inputs=inputs); resolved = request.resolve()
	// Convert api.KeyCreateInput to inputs.KeyCreateInput (pointer fields)
	inp := inputs.KeyCreateInput{
		Name:       input.Name,
		Overwrite:  input.Overwrite,
		SetDefault: input.SetDefault,
	}
	if input.Algorithm != "" {
		inp.Algorithm = &input.Algorithm
	}
	if input.Bits > 0 {
		inp.Bits = &input.Bits
	}
	if input.OutputDir != "" {
		inp.OutputDir = &input.OutputDir
	}
	if input.Comment != "" {
		inp.Comment = &input.Comment
	}

	req := inputs.NewKeyCreateRequest(inp)
	resolved, err := req.Resolve()
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "key.create_failed",
			Message:   err.Error(),
			Exception: err,
		}
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
	keyItem, _, err := op.Services.Key.CreateKeypair(ctx, params)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "key.create_failed",
			Message:   fmt.Sprintf("Key creation failed: %v", err),
			Exception: err,
		}
	}

	auditLog := logging.NewAuditLog(op.CacheDir)
	_ = auditLog.LogOperation("key.create", map[string]interface{}{
		"name":      keyItem.Name,
		"algorithm": keyItem.Algorithm,
	}, "")

	return &errs.OperationResult{
		Status: "success",
		Code:   "key.created",
		Item:   keyItem,
	}
}

// KeyAdd adds an existing public key to the cache.
// Matches Python's KeyOperation.add() exactly — passes overwrite parameter.
// Python wraps the entire flow in try/except Exception — top-level panic recovery matches this.
func (op *Operation) KeyAdd(ctx context.Context, name string, pubKeyPath string, overwrite bool) *errs.OperationResult {
	// Python does inline validation at the API level before calling service
	if _, err := os.Stat(pubKeyPath); os.IsNotExist(err) {
		return &errs.OperationResult{
			Status:  "error",
			Code:    "key.add_failed",
			Message: fmt.Sprintf("Public key file not found: %s", pubKeyPath),
		}
	}

	data, err := os.ReadFile(pubKeyPath)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "key.add_failed",
			Message:   fmt.Sprintf("Failed to read public key file: %v", err),
			Exception: err,
		}
	}
	pubKeyContent := strings.TrimSpace(string(data))
	if pubKeyContent == "" {
		return &errs.OperationResult{
			Status:  "error",
			Code:    "key.add_failed",
			Message: fmt.Sprintf("Public key file is empty: %s", pubKeyPath),
		}
	}

	// Detect if user accidentally passed a private key file
	if isPrivateKey(pubKeyContent) {
		altPath := pubKeyPath + ".pub"
		if _, err := os.Stat(altPath); err == nil {
			return &errs.OperationResult{
				Status:  "error",
				Code:    "key.add_failed",
				Message: fmt.Sprintf("'%s' looks like a private key.\nUse the public key instead: mvm key add %s %s", pubKeyPath, name, altPath),
			}
		}
		return &errs.OperationResult{
			Status:  "error",
			Code:    "key.add_failed",
			Message: fmt.Sprintf("'%s' looks like a private key.\nPass the corresponding .pub file instead: mvm key add %s <path>.pub", pubKeyPath, name),
		}
	}

	keyItem, err := op.Services.Key.AddKey(ctx, name, pubKeyPath, pubKeyContent, overwrite)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "key.add_failed",
			Message:   fmt.Sprintf("Failed to add key: %v", err),
			Exception: err,
		}
	}

	auditLog := logging.NewAuditLog(op.CacheDir)
	_ = auditLog.LogOperation("key.add", map[string]interface{}{"name": keyItem.Name}, "")

	return &errs.OperationResult{
		Status: "success",
		Code:   "key.added",
		Item:   keyItem,
	}
}

// KeyRemove removes keys by name or ID.
// Matches Python's KeyOperation.remove() exactly — uses KeyRequest resolution pipeline.
func (op *Operation) KeyRemove(ctx context.Context, input *KeyInput, force bool) *errs.BatchResult {
	// Match Python: KeyRequest(inputs=inputs, db=db).resolve()
	items, _ := op.resolveKeys(ctx, input)

	results := make([]errs.OperationResult, 0)

	for _, key := range items {
		// Check if any VMs reference this key
		vms, _ := op.Repos.VM.FindBySSHKeyID(ctx, key.ID)
		if len(vms) > 0 && !force {
			vmNames := make([]string, len(vms))
			for i, vm := range vms {
				vmNames[i] = vm.Name
			}
			results = append(results, errs.OperationResult{
				Status:  "error",
				Code:    "key.remove_failed",
				Message: fmt.Sprintf("Key '%s' is used by VM(s): %s. Use --force to remove anyway.", key.Name, strings.Join(vmNames, ", ")),
			})
			continue
		}

		// File cleanup is done at the API layer before DB deletion (matching Python)
		keysDir := filepath.Join(op.CacheDir, "keys")
		pubFile := filepath.Join(keysDir, key.Name+".pub")
		privFile := filepath.Join(keysDir, key.Name)
		if _, err := os.Stat(pubFile); err == nil {
			os.Remove(pubFile)
		}
		if _, err := os.Stat(privFile); err == nil {
			os.Remove(privFile)
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

		auditLog := logging.NewAuditLog(op.CacheDir)
		_ = auditLog.LogOperation("key.remove", map[string]interface{}{"name": key.Name}, "")

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
func (op *Operation) KeyInspect(ctx context.Context, input *KeyInput) (map[string]interface{}, error) {
	key, err := op.Get(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("key not found: %v", err)
	}
	return map[string]interface{}{
		"key": map[string]interface{}{
			"id":          key.ID,
			"name":        key.Name,
			"fingerprint": key.Fingerprint,
			"algorithm":   key.Algorithm,
			"comment":     key.Comment,
			"is_default":  key.IsDefault,
			"is_present":  key.IsPresent,
		},
		"files": map[string]interface{}{
			"public_key_path":  key.PublicKeyPath,
			"private_key_path": key.PrivateKeyPath,
		},
		"timestamps": map[string]interface{}{
			"created_at": key.CreatedAt,
			"updated_at": key.UpdatedAt,
		},
	}, nil
}

// KeyExport exports a keypair to a destination directory.
// Matches Python's KeyOperation.export() exactly — uses KeyRequest resolution
// and KeyController.export(). Python wraps controller.export() in try/except Exception.
func (op *Operation) KeyExport(ctx context.Context, input *KeyInput, destination string, overwrite bool) *errs.OperationResult {
	// Python: request = KeyRequest(inputs=inputs, db=db); resolved = request.resolve()
	items, resolveErrs := op.resolveKeys(ctx, input)
	if len(items) == 0 {
		msg := "key not found"
		if len(resolveErrs) > 0 {
			msg = strings.Join(resolveErrs, "; ")
		}
		return &errs.OperationResult{
			Status:  "error",
			Code:    "key.export_failed",
			Message: fmt.Sprintf("Key not found: %s", msg),
		}
	}
	// Python: if len(resolved.keys) != 1: return error
	if len(items) != 1 {
		return &errs.OperationResult{
			Status:  "error",
			Code:    "key.export_failed",
			Message: fmt.Sprintf("Expected exactly one key, got %d", len(items)),
		}
	}

	keyItem := items[0]

	// Use KeyController.export() matching Python:
	// controller = KeyController(resolved.keys[0], repo)
	// keys_dir is a per-call parameter of export(), NOT stored in the controller.
	// paths = controller.export(destination=destination, keys_dir=keys_dir, overwrite=overwrite)
	keysDir := filepath.Join(op.CacheDir, "keys")
	ctrl, err := key.NewController(ctx, keyItem, op.Repos.Key)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "key.export_failed",
			Message:   fmt.Sprintf("Failed to create key controller: %v", err),
			Exception: err,
		}
	}

	destPriv, destPub, err := ctrl.Export(ctx, destination, keysDir, overwrite)
	if err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "key.export_failed",
			Message:   err.Error(),
			Exception: err,
		}
	}

	return &errs.OperationResult{
		Status: "success",
		Code:   "key.exported",
		Item:   []string{destPriv, destPub},
	}
}

// KeySetDefault sets a key as default.
// Matches Python's KeyOperation.set_default() exactly — uses KeyRequest resolution.
// Python wraps service.set_default_keys() in try/except Exception.
func (op *Operation) KeySetDefault(ctx context.Context, input *KeyInput) *errs.OperationResult {
	// Python: request = KeyRequest(inputs=inputs, db=db); resolved = request.resolve()
	items, _ := op.resolveKeys(ctx, input)
	if len(items) == 0 {
		return &errs.OperationResult{
			Status:  "error",
			Code:    "key.default_set_failed",
			Message: "Key not found",
		}
	}

	// Python: names = [k.name for k in resolved.keys]
	names := make([]string, len(items))
	for i, k := range items {
		names[i] = k.Name
	}

	if err := op.Services.Key.SetDefaultKeys(ctx, names); err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "key.default_set_failed",
			Message:   fmt.Sprintf("Failed to set default key: %v", err),
			Exception: err,
		}
	}

	for _, name := range names {
		auditLog := logging.NewAuditLog(op.CacheDir)
		_ = auditLog.LogOperation("key.set_default", map[string]interface{}{"name": name}, "")
	}

	var item interface{} = nil
	if len(items) > 0 {
		item = items[0]
	}

	return &errs.OperationResult{
		Status: "success",
		Code:   "key.default_set",
		Item:   item,
	}
}

// GetDefaults returns all default keys.
// Matches Python's KeyOperation.get_defaults() exactly.
func (op *Operation) GetDefaults(ctx context.Context) ([]*model.SSHKeyItem, error) {
	return op.Repos.Key.GetDefaults(ctx)
}

// KeyClearDefaults clears all default keys.
// Matches Python's KeyOperation.clear_defaults() exactly.
// Python wraps service.clear_default_keys() in try/except Exception.
func (op *Operation) KeyClearDefaults(ctx context.Context) *errs.OperationResult {
	if err := op.Services.Key.ClearDefaultKeys(ctx); err != nil {
		return &errs.OperationResult{
			Status:    "error",
			Code:      "key.defaults_clear_failed",
			Message:   fmt.Sprintf("Failed to clear defaults: %v", err),
			Exception: err,
		}
	}

	auditLog := logging.NewAuditLog(op.CacheDir)
	_ = auditLog.LogOperation("key.clear_defaults", nil, "")

	return &errs.OperationResult{
		Status: "success",
		Code:   "key.defaults_cleared",
	}
}

func isPrivateKey(content string) bool {
	return strings.Contains(content, "-----BEGIN") && strings.Contains(content, "PRIVATE KEY-----")
}

// checkDependencies checks that ssh-keygen is available, matching Python's
// KeyService.check_dependencies().
func (op *Operation) checkDependencies() error {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		return fmt.Errorf("ssh-keygen not found in PATH. Install OpenSSH client package (e.g., 'apt install openssh-client').")
	}
	return nil
}

// Compile-time check
