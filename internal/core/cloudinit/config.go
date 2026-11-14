package cloudinit

import (
	"time"

	"mvmctl/internal/lib/model"
)

// Config holds all cloud-init provisioning parameters.
type Config struct {
	Mode model.CloudInitMode

	VMName       string
	VMID         string
	VMDir        string
	CloudInitDir string

	// Network identity for the root-authorized NoCloud listener.
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
	UserPassword string // from defaults.vm.user_password

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
