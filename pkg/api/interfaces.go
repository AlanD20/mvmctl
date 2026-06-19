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
	SSHAPI
	ConfigAPI
	CacheAPI
	LogAPI
	CPAPI
	InitAPI
	SnapshotAPI
}

// Compile-time check that *Operation satisfies the API interface.
var _ API = (*Operation)(nil)
