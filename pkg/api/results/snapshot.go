package results

import (
	"mvmctl/internal/lib/model"
)

// SnapshotInspect holds detailed information about a snapshot for display.
type SnapshotInspect struct {
	Snapshot  SnapshotItemInfo      `json:"snapshot"`
	Assets    SnapshotAssetsInfo    `json:"assets"`
	Resources SnapshotResourcesInfo `json:"resources"`
}

// SnapshotItemInfo holds the basic snapshot metadata.
type SnapshotItemInfo struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	SourceVMID   string `json:"source_vm_id"`
	SourceVMName string `json:"source_vm_name"`
	BaseDir      string `json:"base_dir"`
	CreatedAt    string `json:"created_at"`
}

// SnapshotAssetsInfo holds the resolved asset references from enrichment.
type SnapshotAssetsInfo struct {
	Image   *model.ImageItem   `json:"image,omitempty"`
	Kernel  *model.KernelItem  `json:"kernel,omitempty"`
	Network *model.NetworkItem `json:"network,omitempty"`
	Binary  *model.BinaryItem  `json:"binary,omitempty"`
}

// SnapshotResourcesInfo holds the captured resource configuration.
type SnapshotResourcesInfo struct {
	VCPU int `json:"vcpu"`
	Mem  int `json:"mem"`
	Disk int `json:"disk"`
}
