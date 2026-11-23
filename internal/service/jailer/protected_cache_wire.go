package jailer

// ProtectedCacheCleanAction retires only admitted empty protected parents in a selected cache.
const ProtectedCacheCleanAction = "jailer-protected-cache-clean"

// ProtectedCacheQuarantineDirectory is the fixed empty suffix removed by the ordinary caller.
const ProtectedCacheQuarantineDirectory = ".mvm-protected-retirement"

// ProtectedCacheCleanRequest supplies a managed-cache locator, never deletion paths or force.
type ProtectedCacheCleanRequest struct {
	CacheRoot string `json:"cache_root"`
}

// Validate requires the canonical managed-cache selector.
func (request ProtectedCacheCleanRequest) Validate() error {
	_, err := parseManagedCacheLocator(request.CacheRoot)
	return err
}

// ProtectedCacheCleanResult confirms store retirement; the caller removes the fixed empty quarantine.
type ProtectedCacheCleanResult struct {
	Cleaned bool `json:"cleaned"`
}

// Validate refuses an uncertain or incomplete cleanup acknowledgement.
func (result ProtectedCacheCleanResult) Validate() error {
	if !result.Cleaned {
		return managedVMResourceValidationError("protected cache cleanup is incomplete")
	}
	return nil
}
