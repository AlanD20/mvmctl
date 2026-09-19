package model

// --- PruneAllResult ---

// PruneAllResult holds the result of a full cache prune across all resource types.
type PruneAllResult struct {
	PrunedIDs     []string `json:"pruned_ids"`
	FailedIDs     []string `json:"failed_ids"`
	HadRunningVMs bool     `json:"had_running_vms"`
}

// --- CleanResult ---

// CleanResult holds the result of a complete cache clean operation.
type CleanResult struct {
	PruneResult     PruneAllResult `json:"prune_result"`
	CacheDirRemoved bool           `json:"cache_dir_removed"`
	CacheDir        string         `json:"cache_dir"`
}
