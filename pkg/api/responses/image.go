package responses

// ImageItemInfo groups image metadata in an inspect response.
type ImageItemInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Arch      string `json:"arch"`
	IsDefault bool   `json:"is_default"`
	IsPresent bool   `json:"is_present"`
}

// ImageStorageInfo groups image storage info in an inspect response.
type ImageStorageInfo struct {
	Path           string `json:"path"`
	FSType         string `json:"fs_type"`
	FSUUID         string `json:"fs_uuid"`
	CompressedSize *int64 `json:"compressed_size"`
	OriginalSize   int64  `json:"original_size"`
}

// ImageCompressionInfo groups image compression info in an inspect response.
type ImageCompressionInfo struct {
	Format *string  `json:"format"`
	Ratio  *float64 `json:"ratio"`
}

// ImageRequirementsInfo groups image requirements in an inspect response.
type ImageRequirementsInfo struct {
	MinRootfsSizeMiB int `json:"minimum_rootfs_size_mib"`
}

// ImageTimestampsInfo groups image timestamps in an inspect response.
type ImageTimestampsInfo struct {
	PulledAt  string `json:"pulled_at"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ImageInspect is the structured response for image inspection.
type ImageInspect struct {
	Image        ImageItemInfo         `json:"image"`
	Storage      ImageStorageInfo      `json:"storage"`
	Compression  ImageCompressionInfo  `json:"compression"`
	Requirements ImageRequirementsInfo `json:"requirements"`
	Timestamps   ImageTimestampsInfo   `json:"timestamps"`
}
