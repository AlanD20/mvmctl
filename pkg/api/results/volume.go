package results

// VolumeItemInfo groups volume metadata in an inspect response.
type VolumeItemInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	SizeBytes  int64  `json:"size_bytes"`
	Format     string `json:"format"`
	IsReadOnly bool   `json:"is_read_only"`
	Path       string `json:"path"`
	Status     string `json:"status"`
}

// VolumeAttachmentInfo groups volume attachment info in an inspect response.
type VolumeAttachmentInfo struct {
	VMID   *string `json:"vm_id"`
	VMName string  `json:"vm_name"`
}

// VolumeTimestampsInfo groups volume timestamps in an inspect response.
type VolumeTimestampsInfo struct {
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// VolumeInspect is the structured response for volume inspection.
type VolumeInspect struct {
	Volume     VolumeItemInfo       `json:"volume"`
	Attachment VolumeAttachmentInfo `json:"attachment"`
	DiskInfo   any                  `json:"disk_info"`
	Timestamps VolumeTimestampsInfo `json:"timestamps"`
}
