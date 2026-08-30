package jailer

const (
	maxProcessStatBytes           = 16 * 1024
	maxProcessStatusBytes         = 1024 * 1024
	maxProcessCgroupBytes         = 64 * 1024
	maxProcessBootIDBytes         = 128
	maxProcessSupplementaryGroups = 256
)

type processObservation struct {
	startTimeTicks      uint64
	userIDs             processCredentialIdentity
	groupIDs            processCredentialIdentity
	supplementaryGroups []uint32
	cgroupPath          string
}
