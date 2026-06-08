package cloudinit

import (
	"time"

	"mvmctl/internal/lib/model"
)

// Config holds all cloud-init provisioning parameters.
// Matches Python's CloudInitConfig dataclass.
type Config struct {
	Mode model.CloudInitMode

	VMName       string
	VMID         string
	VMDir        string
	CloudInitDir string

	// Network identity fields for firewall rule creation.
	NetworkID   string
	NetworkName string

	GuestIP           string
	User              string
	TapName           string
	IPv4Gateway       string
	NetworkPrefixLen  int
	SkipNetworkConfig bool

	SSHPubkeys []string

	// Resolved from defaults
	UserPassword          string // from defaults.vm.user_password
	CloudInitISOName      string // from defaults.cloudinit.iso_name
	NocloudPortRangeStart int    // from defaults.cloudinit.nocloud_port_range_start
	NocloudPortRangeEnd   int    // from defaults.cloudinit.nocloud_port_range_end
	NocloudMaxPortRetries int    // from defaults.cloudinit.nocloud_max_port_retries

	CustomCloudInitConfig *string

	// Pre-allocated nocloud server (shared across batch VMs).
	// When set, provisionNet() skips spawning and uses these directly.
	NoCloudURL  string
	NoCloudPort int
	NoCloudPID  int

	// Optional overrides
	NocloudNetPort   *int
	CloudInitISOPath *string
	KeepCloudInitISO bool

	// Auto-kill timeout for spawned nocloud server subprocess.
	// 0 means no auto-kill (keep running until explicitly stopped).
	KillAfter time.Duration
}
