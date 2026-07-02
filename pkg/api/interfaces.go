// Package api provides the public orchestration layer for all operations.
package api

// API is the composite interface for all mvmctl operations.
// It embeds all per-domain interfaces and is satisfied by *Operation.
type API interface {
	VMAPI
	ImageAPI
	NetworkAPI
	VolumeAPI
	KernelAPI
	KeyAPI
	BinaryAPI
	HostAPI
	ConsoleAPI
	ExecAPI
	SSHAPI
	ConfigAPI
	CacheAPI
	LogAPI
	CPAPI
	InitAPI
	SnapshotAPI
	UpdateAPI
}
