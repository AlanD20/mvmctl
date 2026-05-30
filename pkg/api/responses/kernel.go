package responses

// KernelItemInfo groups kernel metadata in an inspect response.
type KernelItemInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	BaseName  string `json:"base_name"`
	Version   string `json:"version"`
	Arch      string `json:"arch"`
	Type      string `json:"type"`
	IsDefault bool   `json:"is_default"`
	IsPresent bool   `json:"is_present"`
}

// KernelStorageInfo groups kernel storage info in an inspect response.
type KernelStorageInfo struct {
	Path string `json:"path"`
}

// KernelTimestampsInfo groups kernel timestamps in an inspect response.
type KernelTimestampsInfo struct {
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// KernelInspect is the structured response for kernel inspection.
type KernelInspect struct {
	Kernel     KernelItemInfo       `json:"kernel"`
	Storage    KernelStorageInfo    `json:"storage"`
	Timestamps KernelTimestampsInfo `json:"timestamps"`
}
