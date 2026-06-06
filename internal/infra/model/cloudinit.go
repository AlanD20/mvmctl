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

// ── Cloud-init lifecycle interfaces ──

// CloudInitFirewallRule is a firewall rule created during cloud-init provisioning.
type CloudInitFirewallRule interface {
	Remove() error
}

// ── CloudInitResult ──

// CloudInitResult holds the result of cloud-init provisioning.
type CloudInitResult struct {
	Mode CloudInitMode

	ISOPath     *string
	NocloudURL  *string
	NocloudPort int

	NocloudPID        *int
	NocloudNetRules   []FirewallRule // Firewall rules created
}
