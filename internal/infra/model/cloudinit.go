package model

// ── CloudInitMode ──

// CloudInitMode represents cloud-init configuration mode.
type CloudInitMode string

const (
	CloudInitModeOFF    CloudInitMode = "off"
	CloudInitModeINJECT CloudInitMode = "inject"
	CloudInitModeNET    CloudInitMode = "net"
	CloudInitModeISO    CloudInitMode = "iso"
)

// ── CloudInitStatus ──

// CloudInitStatus represents cloud-init execution status.
type CloudInitStatus string

const (
	CloudInitStatusPending CloudInitStatus = "PENDING"
	CloudInitStatusRunning CloudInitStatus = "RUNNING"
	CloudInitStatusDone    CloudInitStatus = "DONE"
	CloudInitStatusError   CloudInitStatus = "ERROR"
)

// ── ProvisionConfig ──

// ProvisionConfig holds all cloud-init provisioning parameters.
type ProvisionConfig struct {
	Mode CloudInitMode

	VMName       string
	VMID         string
	VMDir        string
	CloudInitDir string

	// Network identity fields for firewall rule creation.
	NetworkID   string
	NetworkName string

	GuestIP          string
	User             string
	TapName          string
	IPv4Gateway      string
	NetworkPrefixLen int
	SkipNetworkConfig bool

	SSHPubkeys []string

	// Resolved from defaults.cloudinit
	CloudInitISOName      string
	NocloudPortRangeStart int
	NocloudPortRangeEnd   int
	NocloudMaxPortRetries int

	CustomUserDataPath *string

	// Optional overrides
	NocloudNetPort   *int
	CloudInitISOPath *string
	KeepCloudInitISO bool
}

// ── ProvisionResult ──

// ProvisionResult holds the result of cloud-init provisioning.
type ProvisionResult struct {
	Mode CloudInitMode

	ISOPath    *string
	NocloudURL *string
	NocloudPort int

	NocloudPID  *int
	NocloudNetManager any           `json:"-"` // Runtime lifecycle manager — not a data field
	NocloudNetRules   []FirewallRule // Firewall rules created
}
