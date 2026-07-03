package results

import (
	"time"

	"mvmctl/internal/lib/model"
)

// VMVolume is a volume entry in the VM inspect response.
type VMVolume struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Format string `json:"format"`
	Status string `json:"status"`
}

// VMItemInfo groups VM metadata in an inspect response.
type VMItemInfo struct {
	Name           string   `json:"name"`
	ID             string   `json:"id"`
	Status         string   `json:"status"`
	PID            int      `json:"pid"`
	ExitCode       *int     `json:"exit_code"`
	SSHKeys        []string `json:"ssh_keys"`
	SSHUser        *string  `json:"ssh_user"`
	CloudInitMode  string   `json:"cloud_init_mode"`
	NocloudNetPort *int     `json:"nocloud_net_port"`
	NocloudNetPID  *int     `json:"nocloud_net_pid"`
	PCIEnabled     bool     `json:"pci_enabled"`
	NestedVirt     bool     `json:"nested_virt"`
	EnableConsole  bool     `json:"enable_console"`
	EnableLogging  bool     `json:"enable_logging"`
	EnableMetrics  bool     `json:"enable_metrics"`
	AllowRemoteExec bool    `json:"allow_remote_exec"`
	CreatedAt      string   `json:"created_at"`
	UpdatedAt      string   `json:"updated_at"`
}

// VMResourcesInfo groups VM resource allocation in an inspect response.
type VMResourcesInfo struct {
	VCPU int `json:"vcpu"`
	Mem  int `json:"mem"`
	Disk int `json:"disk"`
}

// VMNetworkingInfo groups VM networking info in an inspect response.
type VMNetworkingInfo struct {
	IPv4      string             `json:"ipv4"`
	MAC       string             `json:"mac"`
	Network   *model.NetworkItem `json:"network,omitempty"`
	TapDevice string             `json:"tap_device"`
}

// VMAssetsInfo groups VM asset references in an inspect response.
type VMAssetsInfo struct {
	Image  *model.ImageItem  `json:"image,omitempty"`
	Kernel *model.KernelItem `json:"kernel,omitempty"`
	Binary *model.BinaryItem `json:"binary,omitempty"`
}

// VMFilesystemInfo groups VM filesystem paths in an inspect response.
type VMFilesystemInfo struct {
	VMDir            string  `json:"vm_dir"`
	RootfsPath       string  `json:"rootfs_path"`
	ConfigPath       *string `json:"config_path"`
	LogPath          *string `json:"log_path"`
	SerialOutputPath *string `json:"serial_output_path"`
}

// VMConsoleInfo groups VM console relay info in an inspect response.
type VMConsoleInfo struct {
	RelayRunning    bool    `json:"relay_running"`
	RelayPID        *int    `json:"relay_pid"`
	RelaySocketPath *string `json:"relay_socket_path"`
}

// VMVsockInfo groups agent info in an inspect response.
// Token is intentionally excluded to avoid leaking the auth secret.
type VMVsockInfo struct {
	ID               string     `json:"id"`
	GuestCID         int        `json:"guest_cid"`
	UDSPath          string     `json:"uds_path"`
	Port             int        `json:"port"`
	AgentVersion     string     `json:"agent_version"`
	Upgrading        bool       `json:"upgrading"`
	UpgradeStartedAt *time.Time `json:"upgrade_started_at,omitempty"`
}

// VMInspect is the structured response for VM inspection.
type VMInspect struct {
	VM         VMItemInfo       `json:"vm"`
	Resources  VMResourcesInfo  `json:"resources"`
	Networking VMNetworkingInfo `json:"networking"`
	Assets     VMAssetsInfo     `json:"assets"`
	Filesystem VMFilesystemInfo `json:"filesystem"`
	Console    VMConsoleInfo    `json:"console"`
	Volumes    []VMVolume       `json:"volumes"`
	Vsock      *VMVsockInfo     `json:"vsock,omitempty"`
}
