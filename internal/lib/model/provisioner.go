package model

// --- ProvisionerType ---

// ProvisionerType identifies the image provisioner implementation.
type ProvisionerType string

const (
	ProvisionerLoopMount ProvisionerType = "loop_mount"
	ProvisionerGuestFS   ProvisionerType = "guestfs"
)
