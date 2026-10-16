package jailer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
)

func realProcessIdentityVerifierDeps() processIdentityVerifierDeps {
	return processIdentityVerifierDeps{
		readBootID:    readCurrentProcessBootID,
		pidfdAlive:    processPIDFDAlive,
		deriveLocator: deriveProcessLocatorFromPIDFD,
		now:           time.Now,
		wait: func(ctx context.Context, wait time.Duration) error {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
		openProcessDirectory:  openFixedProcessDirectory,
		readProcessMetadata:   readFixedProcessMetadata,
		pinProcessObject:      pinFixedProcessObject,
		pinHostMountNamespace: pinFixedHostMountNamespace,
		pinHostRoot:           pinFixedHostRoot,
		descriptorFlags:       processDescriptorFlags,
		fstat:                 processDescriptorStat,
		pread:                 readProcessDescriptorAt,
		close:                 closeProcessDescriptor,
		pinnedIdentity: func(
			ctx context.Context,
			executable *system.PinnedExecutable,
		) (system.PinnedExecutableIdentity, error) {
			return executable.Identity(ctx)
		},
		pinnedReinspect: func(
			ctx context.Context,
			executable *system.PinnedExecutable,
		) (system.PinnedExecutableIdentity, error) {
			return executable.Reinspect(ctx)
		},
	}
}

func readCurrentProcessBootID(ctx context.Context) (_ string, returnErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fd, err := unix.Open(processBootIDPath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer func() {
		returnErr = appendProcessVerificationError(
			returnErr,
			"close host boot ID descriptor",
			unix.Close(fd),
			"",
		)
	}()
	raw, err := readBoundedProcessDescriptor(ctx, fd, maxProcessBootIDBytes)
	if err != nil {
		return "", err
	}
	bootID, err := parseSingleProcessMetadataLine(raw, maxProcessBootIDBytes)
	if err != nil || !hostBootIDPattern.MatchString(bootID) {
		return "", fmt.Errorf("host boot ID is not canonical")
	}
	return bootID, nil
}

func openFixedProcessDirectory(ctx context.Context, pid int) (resultFD int, returnErr error) {
	resultFD = -1
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	directoryFlags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	procFD, err := unix.Open("/proc", directoryFlags, 0)
	if err != nil {
		return -1, err
	}
	processFD := -1
	defer func() {
		closeErr := unix.Close(procFD)
		if closeErr == nil {
			return
		}
		if processFD >= 0 {
			closeErr = errors.Join(closeErr, unix.Close(processFD))
			processFD = -1
		}
		resultFD = -1
		returnErr = appendProcessVerificationError(returnErr, "close proc root descriptor", closeErr, "")
	}()
	processFD, err = unix.Openat(procFD, strconv.Itoa(pid), directoryFlags, 0)
	if err != nil {
		return -1, err
	}
	resultFD = processFD
	return resultFD, nil
}

func readFixedProcessMetadata(
	ctx context.Context,
	processDirectoryFD int,
	kind processMetadataKind,
	limit int,
) (_ []byte, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var leaf string
	switch kind {
	case processStatMetadata:
		leaf = "stat"
	case processStatusMetadata:
		leaf = "status"
	case processCgroupMetadata:
		leaf = "cgroup"
	default:
		return nil, fmt.Errorf("process metadata selection is invalid")
	}
	fd, err := unix.Openat(processDirectoryFD, leaf, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		returnErr = appendProcessVerificationError(
			returnErr,
			"close runtime process metadata descriptor",
			unix.Close(fd),
			"",
		)
	}()
	return readBoundedProcessDescriptor(ctx, fd, limit)
}

func pinFixedProcessObject(
	ctx context.Context,
	processDirectoryFD int,
	object processPinnedObject,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	var leaf string
	switch object {
	case processMountNamespaceObject:
		leaf = "ns/mnt"
	case processNetworkNamespaceObject:
		leaf = "ns/net"
	case processRootObject:
		leaf = "root"
	case processExecutableObject:
		leaf = "exe"
	default:
		return -1, fmt.Errorf("process object selection is invalid")
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC
	if object == processRootObject {
		flags |= unix.O_DIRECTORY
	}
	return unix.Openat(processDirectoryFD, leaf, flags, 0)
}

func pinFixedHostMountNamespace(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	return unix.Open("/proc/self/ns/mnt", unix.O_RDONLY|unix.O_CLOEXEC, 0)
}

func pinFixedHostRoot(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	return unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}

func processDescriptorStat(ctx context.Context, fd int, stat *unix.Stat_t) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return unix.Fstat(fd, stat)
}

func readProcessDescriptorAt(ctx context.Context, fd int, buffer []byte, offset int64) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return unix.Pread(fd, buffer, offset)
}

func closeProcessDescriptor(_ context.Context, fd int) error {
	return unix.Close(fd)
}

func readBoundedProcessDescriptor(ctx context.Context, fd, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("process metadata limit is invalid")
	}
	result := make([]byte, 0, min(limit, 4*1024))
	buffer := make([]byte, min(limit+1, 32*1024))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := unix.Read(fd, buffer)
		if count < 0 || count > len(buffer) {
			return nil, fmt.Errorf("process metadata read count is invalid")
		}
		if count > 0 {
			if len(result)+count > limit {
				return nil, fmt.Errorf("process metadata exceeds limit")
			}
			result = append(result, buffer[:count]...)
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if count == 0 {
			return result, nil
		}
	}
}
