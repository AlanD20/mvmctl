// Package jailer provides the privileged, fixed-policy Firecracker Jailer service.
// Layer: Service — callers select typed operations, never raw Jailer arguments.
package jailer

// VolumeMount describes one VM volume exposed inside a jail.
type VolumeMount struct {
	DriveID  string `json:"drive_id"`
	HostPath string `json:"host_path"`
	ReadOnly bool   `json:"read_only"`
}

// LaunchManifest contains the exact per-VM resources needed by Firecracker.
type LaunchManifest struct {
	VMID         string        `json:"vm_id"`
	VMDir        string        `json:"vm_dir"`
	Version      string        `json:"version"`
	ConfigPath   string        `json:"config_path"`
	PIDPath      string        `json:"pid_path"`
	KernelPath   string        `json:"kernel_path"`
	RootfsPath   string        `json:"rootfs_path"`
	ISOPath      string        `json:"iso_path,omitempty"`
	SnapshotDir  string        `json:"snapshot_dir,omitempty"`
	Volumes      []VolumeMount `json:"volumes,omitempty"`
	APISocket    string        `json:"api_socket"`
	PCIEnabled   bool          `json:"pci_enabled"`
	SnapshotMode bool          `json:"snapshot_mode"`
}

// InstallResult identifies the immutable trusted paths installed for a release.
type InstallResult struct {
	FirecrackerPath string `json:"firecracker_path"`
	JailerPath      string `json:"jailer_path"`
}
