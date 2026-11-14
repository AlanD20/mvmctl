package loopmount

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const (
	rootfsSysfsBlockPath                = "/sys/class/block"
	rootfsSysfsDirectoryOpenFlags       = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_DIRECTORY
	rootfsSysfsAttributeOpenFlags       = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	rootfsPartitionDirectoryEntryLimit  = 65_536
	maxRootfsSysfsAttributeBytes        = 32
	rootfsPartitionDirectoryBufferBytes = 8 * 1024
	maxRootfsPartitionCleanupDetail     = 512
)

type rootfsPartitionDiscoveryDeps struct {
	open  func(context.Context, string, int, uint32) (int, error)
	list  func(context.Context, int, int) ([]string, error)
	read  func(context.Context, int, []byte) (int, error)
	fstat func(context.Context, int, *unix.Stat_t) error
	close func(context.Context, int) error
}

type rootfsPartitionDevice struct {
	number   uint8
	fd       int
	identity rootfsBlockIdentity
}

type rootfsPartitionSet struct {
	state *rootfsPartitionSetState
}

type rootfsPartitionSetState struct {
	// mu guards one reverse-order descriptor close and the cached cleanup result.
	mu sync.Mutex

	deps           rootfsPartitionDiscoveryDeps
	parent         *rootfsLoopDeviceState
	parentIndex    int
	parentIdentity rootfsBlockIdentity
	devices        []rootfsPartitionDevice
	released       bool
	releaseErr     error
}

// discoverRootfsPartitionDevices admits only kernel-derived partition nodes for one active loop capability.
// The parent lock prevents detach from racing sysfs evidence collection and device-descriptor admission.
func discoverRootfsPartitionDevices(
	ctx context.Context,
	deps rootfsPartitionDiscoveryDeps,
	parent *rootfsLoopDevice,
) (result *rootfsPartitionSet, returnErr error) {
	if ctx == nil {
		return nil, rootfsAtomicError("partition discovery context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, rootfsAtomicError("partition discovery context ended", err)
	}
	if err := validateRootfsPartitionDiscoveryDeps(deps); err != nil {
		return nil, err
	}
	if parent == nil {
		return nil, rootfsAtomicError("parent loop device is missing", nil)
	}
	if parent.state == nil {
		return nil, rootfsAtomicError("parent loop device is inactive", nil)
	}

	parentState := parent.state
	parentState.mu.Lock()
	defer parentState.mu.Unlock()
	if err := validateActiveRootfsPartitionParent(ctx, parentState); err != nil {
		return nil, err
	}

	directoryFD, err := openRootfsSysfsBlockDirectory(ctx, deps)
	if err != nil {
		return nil, err
	}
	directoryFDs := []int{directoryFD}
	retained := make([]rootfsPartitionDevice, 0, maxRootfsPartitionCount)
	defer func() {
		cleanupCtx := context.WithoutCancel(ctx)
		if returnErr != nil && result == nil {
			returnErr = closeRootfsPartitionDevices(cleanupCtx, deps, retained, returnErr)
		}
		for index := len(directoryFDs) - 1; index >= 0; index-- {
			returnErr = appendRootfsPartitionCleanupError(
				returnErr,
				"close sysfs block directory",
				deps.close(cleanupCtx, directoryFDs[index]),
			)
		}
	}()

	rawName := "loop" + strconv.Itoa(parentState.index)
	partitionNumbers, err := listRootfsLoopPartitionNumbers(ctx, deps, directoryFD, rawName)
	if err != nil {
		return nil, err
	}

	rawIdentity, err := readRootfsSysfsBlockIdentity(ctx, deps, rawName)
	if err != nil {
		return nil, err
	}
	if rawIdentity != parentState.identity {
		return nil, rootfsAtomicError("sysfs raw loop identity does not match the admitted parent", nil)
	}

	seenIdentities := make(map[rootfsBlockIdentity]struct{}, len(partitionNumbers))
	for _, partition := range partitionNumbers {
		if err := ctx.Err(); err != nil {
			return nil, rootfsAtomicError("partition discovery context ended", err)
		}
		deviceName := rawName + "p" + strconv.Itoa(int(partition))
		evidencePartition, err := readRootfsSysfsPartitionNumber(ctx, deps, deviceName)
		if err != nil {
			return nil, err
		}
		if evidencePartition != partition {
			return nil, rootfsAtomicError("sysfs partition attribute does not match its kernel name", nil)
		}
		evidenceIdentity, err := readRootfsSysfsBlockIdentity(ctx, deps, deviceName)
		if err != nil {
			return nil, err
		}
		if evidenceIdentity == rawIdentity {
			return nil, rootfsAtomicError("sysfs partition identity matches the raw loop device", nil)
		}
		if _, exists := seenIdentities[evidenceIdentity]; exists {
			return nil, rootfsAtomicError("sysfs partition identity is duplicated", nil)
		}
		seenIdentities[evidenceIdentity] = struct{}{}

		devicePath, err := rootfsLoopPartitionDevicePath(parentState.index, partition)
		if err != nil {
			return nil, err
		}
		deviceFD, err := deps.open(ctx, devicePath, rootfsPartitionDeviceOpenFlags(parentState.access), 0)
		if err != nil {
			return nil, rootfsAtomicError("open admitted partition device", err)
		}
		device := rootfsPartitionDevice{number: partition, fd: deviceFD, identity: evidenceIdentity}
		retained = append(retained, device)

		var deviceStat unix.Stat_t
		if err := deps.fstat(ctx, deviceFD, &deviceStat); err != nil {
			return nil, rootfsAtomicError("inspect admitted partition device", err)
		}
		if err := validateRootfsPartitionDeviceIdentity(deviceStat, evidenceIdentity); err != nil {
			return nil, err
		}

		observedPartition, err := readRootfsSysfsPartitionNumber(ctx, deps, deviceName)
		if err != nil {
			return nil, err
		}
		observedIdentity, err := readRootfsSysfsBlockIdentity(ctx, deps, deviceName)
		if err != nil {
			return nil, err
		}
		if observedPartition != evidencePartition || observedIdentity != evidenceIdentity {
			return nil, rootfsAtomicError("sysfs partition evidence changed after open", nil)
		}
		observedRawIdentity, err := readRootfsSysfsBlockIdentity(ctx, deps, rawName)
		if err != nil {
			return nil, err
		}
		if observedRawIdentity != rawIdentity {
			return nil, rootfsAtomicError("sysfs raw loop evidence changed after open", nil)
		}
	}

	if len(partitionNumbers) == 0 {
		observedRawIdentity, err := readRootfsSysfsBlockIdentity(ctx, deps, rawName)
		if err != nil {
			return nil, err
		}
		if observedRawIdentity != rawIdentity {
			return nil, rootfsAtomicError("sysfs raw loop evidence changed after discovery", nil)
		}
	}

	verificationDirectoryFD, err := openRootfsSysfsBlockDirectory(ctx, deps)
	if err != nil {
		return nil, err
	}
	directoryFDs = append(directoryFDs, verificationDirectoryFD)
	observedPartitions, err := listRootfsLoopPartitionNumbers(ctx, deps, verificationDirectoryFD, rawName)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(partitionNumbers, observedPartitions) {
		return nil, rootfsAtomicError("sysfs loop partition membership changed during discovery", nil)
	}

	result = &rootfsPartitionSet{state: &rootfsPartitionSetState{
		deps:           deps,
		parent:         parentState,
		parentIndex:    parentState.index,
		parentIdentity: parentState.identity,
		devices:        retained,
	}}
	return result, nil
}

func openRootfsSysfsBlockDirectory(
	ctx context.Context,
	deps rootfsPartitionDiscoveryDeps,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, rootfsAtomicError("partition discovery context ended", err)
	}
	directoryFD, err := deps.open(ctx, rootfsSysfsBlockPath, rootfsSysfsDirectoryOpenFlags, 0)
	if err != nil {
		return -1, rootfsAtomicError("open sysfs block directory", err)
	}

	var directoryStat unix.Stat_t
	if err := deps.fstat(ctx, directoryFD, &directoryStat); err != nil {
		primary := rootfsAtomicError("inspect sysfs block directory", err)
		return -1, appendRootfsPartitionCleanupError(
			primary,
			"close rejected sysfs block directory",
			deps.close(context.WithoutCancel(ctx), directoryFD),
		)
	}
	if directoryStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		primary := rootfsAtomicError("opened sysfs block path is not a directory", nil)
		return -1, appendRootfsPartitionCleanupError(
			primary,
			"close rejected sysfs block directory",
			deps.close(context.WithoutCancel(ctx), directoryFD),
		)
	}
	return directoryFD, nil
}

func listRootfsLoopPartitionNumbers(
	ctx context.Context,
	deps rootfsPartitionDiscoveryDeps,
	directoryFD int,
	rawName string,
) ([]uint8, error) {
	if err := ctx.Err(); err != nil {
		return nil, rootfsAtomicError("partition discovery context ended", err)
	}
	names, err := deps.list(ctx, directoryFD, rootfsPartitionDirectoryEntryLimit)
	if err != nil {
		return nil, rootfsAtomicError("enumerate sysfs block devices", err)
	}
	if len(names) > rootfsPartitionDirectoryEntryLimit {
		return nil, rootfsAtomicError("sysfs block directory exceeds the supported entry bound", nil)
	}
	return parseRootfsLoopPartitionNames(rawName, names)
}

// Release closes every admitted partition descriptor in reverse order without releasing the parent loop capability.
func (set *rootfsPartitionSet) Release(ctx context.Context) error {
	if set == nil {
		return nil
	}
	if set.state == nil {
		return rootfsAtomicError("partition device set is not active", nil)
	}
	state := set.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}

	cleanupCtx := context.Background()
	var result error
	if ctx == nil {
		result = rootfsAtomicError("partition device cleanup context is required", nil)
	} else {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	result = closeRootfsPartitionDevices(cleanupCtx, state.deps, state.devices, result)
	state.parent = nil
	state.parentIndex = -1
	state.parentIdentity = rootfsBlockIdentity{}
	state.devices = nil
	state.released = true
	state.releaseErr = result
	return state.releaseErr
}

func rootfsLoopPartitionDevicePath(loopIndex int, partition uint8) (string, error) {
	if !validRootfsLoopDeviceIndex(loopIndex) {
		return "", rootfsAtomicError("loop device index is outside the supported range", nil)
	}
	if partition == 0 || partition > maxRootfsPartitionCount {
		return "", rootfsAtomicError("partition number is outside the supported bound", nil)
	}
	return rootfsLoopDevicePrefix + strconv.Itoa(loopIndex) + "p" + strconv.Itoa(int(partition)), nil
}

func validateRootfsPartitionDiscoveryDeps(deps rootfsPartitionDiscoveryDeps) error {
	if deps.open == nil || deps.list == nil || deps.read == nil || deps.fstat == nil || deps.close == nil {
		return rootfsAtomicError("partition discovery dependencies are incomplete", nil)
	}
	return nil
}

func validateActiveRootfsPartitionParent(ctx context.Context, state *rootfsLoopDeviceState) error {
	if err := ctx.Err(); err != nil {
		return rootfsAtomicError("partition discovery context ended", err)
	}
	if state.released {
		return rootfsAtomicError("parent loop device is inactive", nil)
	}
	if state.fd < 0 || !validRootfsLoopDeviceIndex(state.index) ||
		state.identity != (rootfsBlockIdentity{major: rootfsLoopBlockMajor, minor: uint32(state.index)}) {
		return rootfsAtomicError("parent loop device state is invalid", nil)
	}
	if state.access != rootfsReadOnly && state.access != rootfsReadWrite {
		return rootfsAtomicError("parent loop device access is invalid", nil)
	}
	return nil
}

func parseRootfsLoopPartitionNames(rawName string, names []string) ([]uint8, error) {
	rawCount := 0
	partitionPrefix := rawName + "p"
	seen := make(map[uint8]struct{}, maxRootfsPartitionCount)
	partitions := make([]uint8, 0, maxRootfsPartitionCount)
	for _, name := range names {
		if name == rawName {
			rawCount++
			if rawCount > 1 {
				return nil, rootfsAtomicError("raw loop device name is duplicated in sysfs", nil)
			}
			continue
		}
		if !strings.HasPrefix(name, partitionPrefix) {
			continue
		}
		suffix := strings.TrimPrefix(name, partitionPrefix)
		value, err := parseRootfsCanonicalDecimal(suffix)
		if err != nil {
			return nil, rootfsAtomicError("loop partition name is malformed", err)
		}
		if value == 0 || value > maxRootfsPartitionCount {
			return nil, rootfsAtomicError("loop partition number is outside the supported bound", nil)
		}
		partition := uint8(value)
		if _, exists := seen[partition]; exists {
			return nil, rootfsAtomicError("loop partition number is duplicated in sysfs", nil)
		}
		seen[partition] = struct{}{}
		partitions = append(partitions, partition)
	}
	if rawCount == 0 {
		return nil, rootfsAtomicError("raw loop device name is missing from sysfs", nil)
	}
	slices.Sort(partitions)
	return partitions, nil
}

func rootfsPartitionDeviceOpenFlags(access rootfsAccess) int {
	if access == rootfsReadOnly {
		return rootfsReadOnlyOpenFlags
	}
	return rootfsReadWriteOpenFlags
}

func readRootfsSysfsPartitionNumber(
	ctx context.Context,
	deps rootfsPartitionDiscoveryDeps,
	deviceName string,
) (uint8, error) {
	path := rootfsSysfsBlockPath + "/" + deviceName + "/partition"
	content, err := readRootfsSysfsAttribute(ctx, deps, path)
	if err != nil {
		return 0, err
	}
	decimal, err := parseRootfsCanonicalDecimalLine(content)
	if err != nil || decimal == 0 || decimal > maxRootfsPartitionCount {
		return 0, rootfsAtomicError("sysfs partition attribute is invalid", err)
	}
	return uint8(decimal), nil
}

func readRootfsSysfsBlockIdentity(
	ctx context.Context,
	deps rootfsPartitionDiscoveryDeps,
	deviceName string,
) (rootfsBlockIdentity, error) {
	path := rootfsSysfsBlockPath + "/" + deviceName + "/dev"
	content, err := readRootfsSysfsAttribute(ctx, deps, path)
	if err != nil {
		return rootfsBlockIdentity{}, err
	}
	if len(content) < 4 || content[len(content)-1] != '\n' {
		return rootfsBlockIdentity{}, rootfsAtomicError("sysfs block identity attribute is malformed", nil)
	}
	line := string(content[:len(content)-1])
	if strings.Count(line, ":") != 1 {
		return rootfsBlockIdentity{}, rootfsAtomicError("sysfs block identity attribute is malformed", nil)
	}
	fields := strings.SplitN(line, ":", 2)
	major, majorErr := parseRootfsCanonicalDecimal(fields[0])
	minor, minorErr := parseRootfsCanonicalDecimal(fields[1])
	if majorErr != nil || minorErr != nil || major > uint64(^uint32(0)) || minor > uint64(^uint32(0)) {
		return rootfsBlockIdentity{}, rootfsAtomicError(
			"sysfs block identity attribute is malformed",
			errors.Join(majorErr, minorErr),
		)
	}
	identity := rootfsBlockIdentity{major: uint32(major), minor: uint32(minor)}
	if !validRootfsBlockIdentity(identity) {
		return rootfsBlockIdentity{}, rootfsAtomicError("sysfs block identity is invalid", nil)
	}
	return identity, nil
}

func readRootfsSysfsAttribute(
	ctx context.Context,
	deps rootfsPartitionDiscoveryDeps,
	path string,
) (content []byte, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, rootfsAtomicError("partition discovery context ended", err)
	}
	fd, err := deps.open(ctx, path, rootfsSysfsAttributeOpenFlags, 0)
	if err != nil {
		return nil, rootfsAtomicError("open sysfs block attribute", err)
	}
	defer func() {
		returnErr = appendRootfsPartitionCleanupError(
			returnErr,
			"close sysfs block attribute",
			deps.close(context.WithoutCancel(ctx), fd),
		)
	}()

	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return nil, rootfsAtomicError("inspect sysfs block attribute", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, rootfsAtomicError("sysfs block attribute is not regular", nil)
	}

	buffer := make([]byte, maxRootfsSysfsAttributeBytes+1)
	readBytes := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, rootfsAtomicError("partition discovery context ended", err)
		}
		count, readErr := deps.read(ctx, fd, buffer[readBytes:])
		if count < 0 || count > len(buffer)-readBytes {
			return nil, rootfsAtomicError("read sysfs block attribute made invalid progress", nil)
		}
		readBytes += count
		if readBytes > maxRootfsSysfsAttributeBytes {
			return nil, rootfsAtomicError("sysfs block attribute exceeds the supported byte bound", nil)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return append([]byte(nil), buffer[:readBytes]...), nil
			}
			return nil, rootfsAtomicError("read sysfs block attribute", readErr)
		}
		if count == 0 {
			return nil, rootfsAtomicError("read sysfs block attribute made no progress", nil)
		}
	}
}

func parseRootfsCanonicalDecimalLine(content []byte) (uint64, error) {
	if len(content) < 2 || content[len(content)-1] != '\n' {
		return 0, errors.New("canonical decimal line is missing its newline")
	}
	return parseRootfsCanonicalDecimal(string(content[:len(content)-1]))
}

func parseRootfsCanonicalDecimal(value string) (uint64, error) {
	if value == "" {
		return 0, errors.New("canonical decimal is empty")
	}
	if len(value) > 1 && value[0] == '0' {
		return 0, errors.New("canonical decimal has a leading zero")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, errors.New("canonical decimal contains a non-digit")
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse canonical decimal: %w", err)
	}
	return parsed, nil
}

func validateRootfsPartitionDeviceIdentity(stat unix.Stat_t, identity rootfsBlockIdentity) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFBLK ||
		unix.Major(stat.Rdev) != identity.major || unix.Minor(stat.Rdev) != identity.minor {
		return rootfsAtomicError("opened device is not the admitted partition block device", nil)
	}
	return nil
}

func closeRootfsPartitionDevices(
	ctx context.Context,
	deps rootfsPartitionDiscoveryDeps,
	devices []rootfsPartitionDevice,
	primary error,
) error {
	cleanupCtx := context.WithoutCancel(ctx)
	for index := len(devices) - 1; index >= 0; index-- {
		device := devices[index]
		if device.fd < 0 {
			primary = appendRootfsPartitionCleanupError(
				primary,
				"close admitted partition device",
				errors.New("partition device descriptor is invalid"),
			)
			continue
		}
		if deps.close == nil {
			primary = appendRootfsPartitionCleanupError(
				primary,
				"close admitted partition device",
				errors.New("partition device close dependency is missing"),
			)
			continue
		}
		primary = appendRootfsPartitionCleanupError(
			primary,
			"close admitted partition device",
			deps.close(cleanupCtx, device.fd),
		)
	}
	return primary
}

func appendRootfsPartitionCleanupError(primary error, message string, cause error) error {
	if cause == nil {
		return primary
	}
	detail := boundedRootfsPartitionCleanupDetail(message, cause)
	if primary == nil {
		return newRootfsPartitionCleanupError(detail, cause)
	}
	if domainErr := errs.AsDomainError(primary); domainErr != nil {
		domainErr.Message += "; " + detail
		domainErr.Err = errors.Join(domainErr.Err, cause)
		if domainErr.Details == nil {
			domainErr.Details = make(map[string]any)
		}
		domainErr.Details["loopmount_partition_cleanup_failed"] = true
		domainErr.Details["loopmount_partition_cleanup_detail"] = detail
		return domainErr
	}
	return newRootfsPartitionCleanupError(detail, errors.Join(primary, cause))
}

func newRootfsPartitionCleanupError(detail string, cause error) *errs.DomainError {
	domainErr := rootfsAtomicError(detail, cause)
	domainErr.Details = map[string]any{
		"loopmount_partition_cleanup_failed": true,
		"loopmount_partition_cleanup_detail": detail,
	}
	return domainErr
}

func boundedRootfsPartitionCleanupDetail(message string, cause error) string {
	detail := fmt.Sprintf("%s: %v", message, cause)
	if len(detail) <= maxRootfsPartitionCleanupDetail {
		return detail
	}
	return detail[:maxRootfsPartitionCleanupDetail-3] + "..."
}

func systemRootfsPartitionDiscoveryDeps() rootfsPartitionDiscoveryDeps {
	return rootfsPartitionDiscoveryDeps{
		open: func(_ context.Context, path string, flags int, mode uint32) (int, error) {
			return unix.Open(path, flags, mode)
		},
		list: func(ctx context.Context, fd int, limit int) ([]string, error) {
			return listRootfsSysfsDirectory(ctx, fd, limit)
		},
		read: func(_ context.Context, fd int, buffer []byte) (int, error) {
			count, err := unix.Read(fd, buffer)
			if count == 0 && err == nil {
				return 0, io.EOF
			}
			return count, err
		},
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error {
			return unix.Fstat(fd, stat)
		},
		close: func(_ context.Context, fd int) error {
			return unix.Close(fd)
		},
	}
}

func listRootfsSysfsDirectory(ctx context.Context, fd int, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, errors.New("directory entry limit is invalid")
	}
	buffer := make([]byte, rootfsPartitionDirectoryBufferBytes)
	names := make([]string, 0, min(limit, 256))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := unix.Getdents(fd, buffer)
		if err != nil {
			return nil, err
		}
		if count == 0 {
			return names, nil
		}
		_, _, names = unix.ParseDirent(buffer[:count], -1, names)
		if len(names) > limit {
			return nil, errors.New("directory entry limit exceeded")
		}
	}
}
