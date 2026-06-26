package model

// --- PruneAllResult ---

// PruneAllResult holds the result of a full cache prune across all resource types.
type PruneAllResult struct {
	PrunedIDs     []string
	FailedIDs     []string
	HadRunningVMs bool
}

// --- CleanResult ---

// CleanResult holds the result of a complete cache clean operation.
type CleanResult struct {
	PruneResult     PruneAllResult
	CacheDirRemoved bool
	CacheDir        string
}
