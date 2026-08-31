package jailer

import (
	"context"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
)

const (
	maxProcessStatBytes              = 16 * 1024
	maxProcessStatusBytes            = 1024 * 1024
	maxProcessCgroupBytes            = 64 * 1024
	maxProcessBootIDBytes            = 128
	maxProcessPIDFDInfoBytes         = 4 * 1024
	maxProcessSupplementaryGroups    = 256
	maxProcessSystemExecutableBytes  = 128 * 1024 * 1024
	processExecutableReadBufferBytes = 32 * 1024
)

const processBootIDPath = "/proc/sys/kernel/random/boot_id"

type processMetadataKind uint8

const (
	processStatMetadata processMetadataKind = iota + 1
	processStatusMetadata
	processCgroupMetadata
)

type processPinnedObject uint8

const (
	processMountNamespaceObject processPinnedObject = iota + 1
	processRootObject
	processExecutableObject
)

type processIdentityVerifierDeps struct {
	readBootID            func(context.Context) (string, error)
	pidfdAlive            func(context.Context, int) (bool, error)
	openProcessDirectory  func(context.Context, int) (int, error)
	readProcessMetadata   func(context.Context, int, processMetadataKind, int) ([]byte, error)
	pinProcessObject      func(context.Context, int, processPinnedObject) (int, error)
	pinHostMountNamespace func(context.Context) (int, error)
	pinHostRoot           func(context.Context) (int, error)
	descriptorFlags       func(context.Context, int) (int, error)
	fstat                 func(context.Context, int, *unix.Stat_t) error
	pread                 func(context.Context, int, []byte, int64) (int, error)
	close                 func(context.Context, int) error
	pinnedIdentity        func(context.Context, *system.PinnedExecutable) (system.PinnedExecutableIdentity, error)
	pinnedReinspect       func(context.Context, *system.PinnedExecutable) (system.PinnedExecutableIdentity, error)
}

type processObservation struct {
	startTimeTicks      uint64
	userIDs             processCredentialIdentity
	groupIDs            processCredentialIdentity
	supplementaryGroups []uint32
	cgroupPath          string
}

type processExecutableDescriptorIdentity struct {
	device    uint64
	inode     uint64
	sizeBytes uint64
	mode      uint32
	uid       uint32
	gid       uint32
	links     uint64
	modified  unix.Timespec
	changed   unix.Timespec
}
