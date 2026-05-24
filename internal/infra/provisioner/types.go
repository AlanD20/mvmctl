package provisioner

type ProvisionerType string

const (
	ProvisionerLoopMount ProvisionerType = "loop_mount"
	ProvisionerGuestFS   ProvisionerType = "guestfs"
)
