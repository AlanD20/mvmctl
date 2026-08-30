package loopmount

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
)

const (
	rootfsBlkidPath                = "/usr/bin/blkid"
	rootfsBlkidNoSignatureExitCode = 2
	rootfsBlkidFailureExitCode     = 4
	rootfsBlkidAmbivalentExitCode  = 8
	maxRootfsBlkidStdoutBytes      = 32
	maxRootfsBlkidStderrBytes      = 512
)

type rootfsFilesystemProbeDeps struct {
	run   func(context.Context, []string, system.RunCmdOpts) (*system.RunResult, error)
	fstat func(context.Context, int, *unix.Stat_t) error
}

type rootfsProbeTarget struct {
	loopIndex int
	partition uint8
	fd        int
	identity  rootfsBlockIdentity
}

type rootfsSelectedSource struct {
	state *rootfsSelectedSourceState
}

type rootfsSelectedSourceState struct {
	mu sync.Mutex

	parent     *rootfsLoopDeviceState
	partitions *rootfsPartitionSetState
	kind       rootfsMountSourceKind
	fd         int
	identity   rootfsBlockIdentity
	partition  uint8
	filesystem Filesystem
	released   bool
	releaseErr error
}

// Release revokes this borrowed selection before its owning partition and loop capabilities are released.
// The selection owns no descriptor, but explicit revocation prevents stale copies from authorizing a later mount.
func (source *rootfsSelectedSource) Release(ctx context.Context) error {
	if source == nil {
		return nil
	}
	if source.state == nil {
		return rootfsAtomicError("selected rootfs source is not active", nil)
	}
	state := source.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if ctx == nil {
		state.releaseErr = rootfsAtomicError("selected rootfs source cleanup context is required", nil)
	}
	state.parent = nil
	state.partitions = nil
	state.kind = 0
	state.fd = -1
	state.identity = rootfsBlockIdentity{}
	state.partition = 0
	state.filesystem = ""
	state.released = true
	return state.releaseErr
}

// probeRootfsMountSource probes only receiver-derived loop nodes and binds the selected retained descriptor.
// The returned immutable capability borrows parent and partitions; their owner releases the partition set before the
// loop device. Failure allocates no resource and leaves both input capabilities owned by the caller.
func probeRootfsMountSource(
	ctx context.Context,
	deps rootfsFilesystemProbeDeps,
	expected Filesystem,
	parent *rootfsLoopDevice,
	partitions *rootfsPartitionSet,
) (*rootfsSelectedSource, error) {
	if ctx == nil {
		return nil, rootfsAtomicError("filesystem probe context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, rootfsAtomicError("filesystem probe context ended", err)
	}
	if deps.run == nil || deps.fstat == nil {
		return nil, rootfsAtomicError("filesystem probe dependencies are incomplete", nil)
	}
	if err := expected.Validate(); err != nil {
		return nil, rootfsAtomicError("requested rootfs filesystem is invalid", err)
	}
	if parent == nil || parent.state == nil {
		return nil, rootfsAtomicError("parent loop device is inactive", nil)
	}
	if partitions == nil || partitions.state == nil {
		return nil, rootfsAtomicError("partition device set is inactive", nil)
	}

	parentState := parent.state
	parentState.mu.Lock()
	defer parentState.mu.Unlock()
	partitionState := partitions.state
	partitionState.mu.Lock()
	defer partitionState.mu.Unlock()

	if err := validateRootfsFilesystemProbeCapabilities(ctx, parentState, partitionState); err != nil {
		return nil, err
	}

	rawFilesystem, err := probeRootfsDeviceFilesystem(
		ctx,
		deps,
		rootfsProbeTarget{
			loopIndex: parentState.index,
			fd:        parentState.fd,
			identity:  parentState.identity,
		},
	)
	if err != nil {
		return nil, err
	}
	rawProbe := rootfsFilesystemProbe{identity: parentState.identity, filesystem: rawFilesystem}

	partitionProbes := make([]rootfsFilesystemProbe, 0, len(partitionState.devices))
	for _, device := range partitionState.devices {
		if err := ctx.Err(); err != nil {
			return nil, rootfsAtomicError("filesystem probe context ended", err)
		}
		filesystem, probeErr := probeRootfsDeviceFilesystem(ctx, deps, rootfsProbeTarget{
			loopIndex: parentState.index,
			partition: device.number,
			fd:        device.fd,
			identity:  device.identity,
		})
		if probeErr != nil {
			return nil, probeErr
		}
		partitionProbes = append(partitionProbes, rootfsFilesystemProbe{
			identity:   device.identity,
			partition:  device.number,
			filesystem: filesystem,
		})
	}

	selection, err := selectRootfsMountSource(expected, rawProbe, partitionProbes)
	if err != nil {
		return nil, err
	}
	selectedFD, err := selectedRootfsDescriptor(parentState, partitionState, selection)
	if err != nil {
		return nil, err
	}
	return &rootfsSelectedSource{state: &rootfsSelectedSourceState{
		parent:     parentState,
		partitions: partitionState,
		kind:       selection.kind,
		fd:         selectedFD,
		identity:   selection.identity,
		partition:  selection.partition,
		filesystem: expected,
	}}, nil
}

func validateRootfsFilesystemProbeCapabilities(
	ctx context.Context,
	parent *rootfsLoopDeviceState,
	partitions *rootfsPartitionSetState,
) error {
	if err := ctx.Err(); err != nil {
		return rootfsAtomicError("filesystem probe context ended", err)
	}
	if parent.released || parent.fd < 0 || !validRootfsLoopDeviceIndex(parent.index) ||
		parent.identity != (rootfsBlockIdentity{major: rootfsLoopBlockMajor, minor: uint32(parent.index)}) {
		return rootfsAtomicError("parent loop device is inactive", nil)
	}
	if parent.access != rootfsReadOnly && parent.access != rootfsReadWrite {
		return rootfsAtomicError("parent loop device access is invalid", nil)
	}
	if partitions.released {
		return rootfsAtomicError("partition device set is inactive", nil)
	}
	if partitions.parent != parent || partitions.parentIndex != parent.index ||
		partitions.parentIdentity != parent.identity {
		return rootfsAtomicError("partition device set does not belong to the parent loop device", nil)
	}
	if len(partitions.devices) > maxRootfsPartitionCount {
		return rootfsAtomicError("partition device count exceeds the supported bound", nil)
	}
	seenIdentities := make(map[rootfsBlockIdentity]struct{}, len(partitions.devices))
	var previousPartition uint8
	for _, device := range partitions.devices {
		if device.number == 0 || device.number > maxRootfsPartitionCount || device.number <= previousPartition {
			return rootfsAtomicError("partition device order or number is invalid", nil)
		}
		if device.fd < 0 || !validRootfsBlockIdentity(device.identity) || device.identity == parent.identity {
			return rootfsAtomicError("partition device identity is invalid", nil)
		}
		if _, exists := seenIdentities[device.identity]; exists {
			return rootfsAtomicError("partition device identity is duplicated", nil)
		}
		seenIdentities[device.identity] = struct{}{}
		previousPartition = device.number
	}
	return nil
}

func probeRootfsDeviceFilesystem(
	ctx context.Context,
	deps rootfsFilesystemProbeDeps,
	target rootfsProbeTarget,
) (Filesystem, error) {
	path, err := rootfsProbeTargetPath(target)
	if err != nil {
		return "", err
	}
	if err := inspectRootfsProbeIdentity(ctx, deps, target.fd, target.identity, "before"); err != nil {
		return "", err
	}

	// util-linux blkid low-level probing opens the fixed device read-only and returns 2 for no detected signature.
	// See https://github.com/util-linux/util-linux/blob/master/misc-utils/blkid.c
	result, runErr := deps.run(ctx, []string{
		rootfsBlkidPath,
		"--probe",
		"--no-part-details",
		"--usages", "filesystem",
		"--match-tag", "TYPE",
		"--output", "value",
		"--",
		path,
	}, system.RunCmdOpts{
		Check:          false,
		Capture:        true,
		Env:            map[string]string{"LANG": "C", "LC_ALL": "C"},
		ReplaceEnv:     true,
		MaxStdoutBytes: maxRootfsBlkidStdoutBytes,
		MaxStderrBytes: maxRootfsBlkidStderrBytes,
	})
	if err := ctx.Err(); err != nil {
		return "", rootfsAtomicError("filesystem probe context ended", err)
	}
	if err := inspectRootfsProbeIdentity(ctx, deps, target.fd, target.identity, "after"); err != nil {
		return "", err
	}
	if runErr != nil {
		return "", rootfsAtomicError("execute filesystem probe", runErr)
	}
	return parseRootfsFilesystemProbeResult(result)
}

func rootfsProbeTargetPath(target rootfsProbeTarget) (string, error) {
	if target.partition == 0 {
		return rootfsLoopDevicePath(target.loopIndex)
	}
	return rootfsLoopPartitionDevicePath(target.loopIndex, target.partition)
}

func inspectRootfsProbeIdentity(
	ctx context.Context,
	deps rootfsFilesystemProbeDeps,
	fd int,
	expected rootfsBlockIdentity,
	phase string,
) error {
	if err := ctx.Err(); err != nil {
		return rootfsAtomicError("filesystem probe context ended", err)
	}
	if fd < 0 || !validRootfsBlockIdentity(expected) {
		return rootfsAtomicError("filesystem probe device identity is invalid", nil)
	}
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return rootfsAtomicError("inspect filesystem probe device "+phase+" probe", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFBLK ||
		unix.Major(stat.Rdev) != expected.major || unix.Minor(stat.Rdev) != expected.minor {
		return rootfsAtomicError("filesystem probe device identity changed "+phase+" probe", nil)
	}
	return nil
}

func parseRootfsFilesystemProbeResult(result *system.RunResult) (Filesystem, error) {
	if result == nil {
		return "", rootfsAtomicError("filesystem probe result is missing", nil)
	}
	if len(result.Stdout) > maxRootfsBlkidStdoutBytes {
		return "", rootfsAtomicError("filesystem probe stdout exceeds the supported byte bound", nil)
	}
	if len(result.Stderr) > maxRootfsBlkidStderrBytes {
		return "", rootfsAtomicError("filesystem probe stderr exceeds the supported byte bound", nil)
	}

	switch result.ExitCode {
	case 0:
		if result.Stderr != "" {
			return "", rootfsAtomicError("filesystem probe emitted unexpected stderr", nil)
		}
		// blkid documents that --match-tag filters output without changing success status. A partition table with no
		// filesystem TYPE can therefore produce successful empty output and is an empty filesystem probe, not ext4.
		// See https://github.com/util-linux/util-linux/blob/master/misc-utils/blkid.8.adoc
		if result.Stdout == "" {
			return "", nil
		}
		return parseRootfsFilesystemToken(result.Stdout)
	case rootfsBlkidNoSignatureExitCode:
		if result.Stdout != "" || result.Stderr != "" {
			return "", rootfsAtomicError("filesystem probe no-signature result emitted output", nil)
		}
		return "", nil
	case rootfsBlkidAmbivalentExitCode:
		return "", rootfsAtomicError("filesystem probe detected an ambivalent signature", nil)
	case rootfsBlkidFailureExitCode:
		return "", rootfsAtomicError("filesystem probe failed with exit code 4", nil)
	default:
		return "", rootfsAtomicError(
			fmt.Sprintf("filesystem probe failed with exit code %d", result.ExitCode),
			nil,
		)
	}
}

func parseRootfsFilesystemToken(output string) (Filesystem, error) {
	if len(output) < 2 || output[len(output)-1] != '\n' || output[:len(output)-1] == "" {
		return "", rootfsAtomicError("filesystem probe output is malformed", nil)
	}
	token := output[:len(output)-1]
	for _, character := range token {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return "", rootfsAtomicError("filesystem probe output is malformed", nil)
		}
	}
	filesystem := Filesystem(token)
	if err := filesystem.Validate(); err != nil {
		return "", rootfsAtomicError("filesystem probe returned an unsupported filesystem", err)
	}
	return filesystem, nil
}

func selectedRootfsDescriptor(
	parent *rootfsLoopDeviceState,
	partitions *rootfsPartitionSetState,
	selection rootfsMountSource,
) (int, error) {
	if selection.kind == rootfsMountSourceRaw {
		if selection.identity != parent.identity || selection.partition != 0 {
			return -1, rootfsAtomicError("selected raw source does not match its retained descriptor", nil)
		}
		return parent.fd, nil
	}
	if selection.kind != rootfsMountSourcePartition {
		return -1, rootfsAtomicError("selected source kind is invalid", nil)
	}
	for _, device := range partitions.devices {
		if device.number == selection.partition && device.identity == selection.identity {
			return device.fd, nil
		}
	}
	return -1, rootfsAtomicError("selected partition source does not match a retained descriptor", nil)
}

func systemRootfsFilesystemProbeDeps() rootfsFilesystemProbeDeps {
	return rootfsFilesystemProbeDeps{
		run: system.DefaultRunner.Run,
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error {
			return unix.Fstat(fd, stat)
		},
	}
}
