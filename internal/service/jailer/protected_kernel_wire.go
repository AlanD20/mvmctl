package jailer

const (
	// ProtectedKernelImportAction explicitly admits a published kernel import.
	ProtectedKernelImportAction = "jailer-kernel-import"
	// ProtectedKernelRemoveAction retires an unreferenced protected kernel.
	ProtectedKernelRemoveAction = "jailer-kernel-remove"
)

// ProtectedKernelImportRequest selects a fixed kernel leaf, never a caller digest or generation.
type ProtectedKernelImportRequest struct {
	CacheRoot string `json:"cache_root"`
	KernelID  string `json:"kernel_id"`
}

// ProtectedKernelRemoveRequest grants no bypass of root VM and snapshot references.
type ProtectedKernelRemoveRequest struct {
	CacheRoot string `json:"cache_root"`
	KernelID  string `json:"kernel_id"`
}

// ProtectedKernelImportResult acknowledges an admitted protected kernel.
type ProtectedKernelImportResult struct {
	KernelID string `json:"kernel_id"`
	Imported bool   `json:"imported"`
}

// ProtectedKernelRemoveResult acknowledges durable retirement.
type ProtectedKernelRemoveResult struct {
	KernelID string `json:"kernel_id"`
	Removed  bool   `json:"removed"`
}

func validateProtectedKernelSelection(cacheRoot, id string) error {
	if !isCanonicalManagedResourceID(id, 64) {
		return managedVMResourceValidationError("kernel ID must be 64 lowercase hexadecimal characters")
	}
	_, err := parseManagedCacheLocator(cacheRoot)
	return err
}

// Validate checks the complete closed import selection.
func (request ProtectedKernelImportRequest) Validate() error {
	return validateProtectedKernelSelection(request.CacheRoot, request.KernelID)
}

// Validate checks the complete closed retirement selection.
func (request ProtectedKernelRemoveRequest) Validate() error {
	return validateProtectedKernelSelection(request.CacheRoot, request.KernelID)
}

// Validate binds the import acknowledgement to its selected kernel.
func (result ProtectedKernelImportResult) Validate(id string) error {
	if result.KernelID != id || !isCanonicalManagedResourceID(id, 64) || !result.Imported {
		return managedVMResourceValidationError("kernel import acknowledgement differs from selection")
	}
	return nil
}

// Validate binds the retirement acknowledgement to its selected kernel.
func (result ProtectedKernelRemoveResult) Validate(id string) error {
	if result.KernelID != id || !isCanonicalManagedResourceID(id, 64) || !result.Removed {
		return managedVMResourceValidationError("kernel retirement acknowledgement differs from selection")
	}
	return nil
}
