package jailer

import (
	"context"
	"errors"
	"io"

	"golang.org/x/sys/unix"
)

func (lease *protectedBackingLease) completeImport(
	ctx context.Context, cache *managedCacheLease, expectedBytes uint64,
) (returnErr error) {
	if lease.record.Phase == "copied" {
		return lease.publishImport(ctx)
	}
	source, sourceFDs, err := openProtectedImportSource(ctx, cache, lease.record.Kind, lease.record.ID, expectedBytes)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, closeProtectedFDs(sourceFDs)) }()
	if expectedBytes == 0 {
		identity, err := protectedIdentity(source)
		if err != nil {
			return err
		}
		expectedBytes = uint64(identity.Size)
	}
	return lease.copyAndPublish(ctx, source, expectedBytes)
}

func openProtectedImportSource(
	ctx context.Context, cache *managedCacheLease, kind, id string, expectedBytes uint64,
) (_ int, _ []int, returnErr error) {
	components := []string{"volumes", id + ".raw"}
	if kind == protectedBackingRootfs {
		components = []string{"vms", id, "rootfs.img"}
	} else if kind == protectedBackingKernel {
		components = []string{"kernels", id}
	} else if kind == protectedBackingCloudInit {
		components = []string{"vms", id, "cloud-init.iso"}
	}
	parent := cache.cacheFD
	var retained []int
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, closeProtectedFDs(retained))
		}
	}()
	for _, component := range components[:len(components)-1] {
		fd, err := openManagedVMResourceDirectory(
			ctx,
			cache.deps,
			parent,
			component,
			"initial backing source",
			cache.ownerUID,
		)
		if err != nil {
			return -1, nil, err
		}
		retained = append(retained, fd)
		parent = fd
	}
	policy := managedVMRegularFilePolicy{
		name: "initial protected backing source", minimumBytes: expectedBytes, maximumBytes: expectedBytes,
		exactBytes: expectedBytes, requiredOwnerMode: 0400,
	}
	if kind == protectedBackingKernel {
		policy.minimumBytes, policy.maximumBytes, policy.allowExecute = managedVMKernelMinBytes, managedVMKernelMaxBytes, true
	} else if kind == protectedBackingCloudInit {
		policy.minimumBytes, policy.maximumBytes = managedVMCloudInitMinBytes, managedVMCloudInitMaxBytes
	}
	fd, err := openManagedVMResourceFileWithFlags(ctx, cache.deps, parent, components[len(components)-1],
		unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, policy, cache.ownerUID)
	if err != nil {
		return -1, nil, err
	}
	return fd, append(retained, fd), nil
}

func (lease *protectedBackingLease) copyAndPublish(
	ctx context.Context,
	source int,
	expectedBytes uint64,
) (returnErr error) {
	var before unix.Stat_t
	if err := unix.Fstat(source, &before); err != nil || before.Size != int64(expectedBytes) ||
		before.Mode&unix.S_IFMT != unix.S_IFREG || before.Nlink != 1 {
		return managedVMResourceAtomicError("inspect initial protected backing source", err)
	}
	sourceIdentity := protectedBackingIdentity{Device: uint64(before.Dev), Inode: before.Ino, Size: before.Size}
	if lease.record.Source.Inode != 0 && lease.record.Source != sourceIdentity {
		return managedVMResourceValidationError("initial protected backing source changed during import recovery")
	}
	lease.record.Source = sourceIdentity
	if lease.record.Kind == protectedBackingVolume && !lease.record.SnapshotSource {
		lease.record.SourceCTimeSec = before.Ctim.Sec
		lease.record.SourceCTimeNsec = before.Ctim.Nsec
	}
	if err := writeProtectedRecord(ctx, lease.metadataFD, "record.json", lease.record); err != nil {
		return err
	}
	pending, err := openProtectedFile(ctx, lease.directoryFD, "pending", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
	if errors.Is(err, unix.EEXIST) {
		pending, err = openProtectedFile(ctx, lease.directoryFD, "pending", unix.O_RDWR)
	}
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(pending)) }()
	identity, err := protectedIdentity(pending)
	if err != nil {
		return err
	}
	if lease.record.Backing.Inode == 0 {
		if identity.Size != 0 {
			return managedVMResourceValidationError("unrecorded protected import candidate is not empty")
		}
		lease.record.Backing = identity
		if err := writeProtectedRecord(ctx, lease.metadataFD, "record.json", lease.record); err != nil {
			return err
		}
	} else if !sameProtectedNode(identity, lease.record.Backing) {
		return managedVMResourceValidationError("protected import candidate identity changed")
	}
	// Prove ACL support on the new inode before copying any guest data. There is no group/world
	// fallback, and the operator's inode and retained writable descriptors are never chmodded.
	if err := setProtectedACL(ctx, pending, nil); err != nil {
		return err
	}
	if err := copyProtectedBytes(ctx, source, pending, int64(expectedBytes)); err != nil {
		return err
	}
	var after unix.Stat_t
	if err := unix.Fstat(source, &after); err != nil || !sameProtectedReadStat(before, after) {
		return managedVMResourceAtomicError("operator source changed while protecting backing", err)
	}
	lease.record.Backing, err = protectedIdentity(pending)
	if err != nil {
		return err
	}
	lease.record.Phase = "copied"
	if err := writeProtectedRecord(ctx, lease.metadataFD, "record.json", lease.record); err != nil {
		return err
	}
	return lease.publishImport(ctx)
}

func (lease *protectedBackingLease) publishImport(ctx context.Context) (returnErr error) {
	fd, err := openProtectedFile(ctx, lease.directoryFD, "disk", unix.O_RDWR)
	if errors.Is(err, unix.ENOENT) {
		pending, openErr := openProtectedFile(ctx, lease.directoryFD, "pending", unix.O_RDWR)
		if openErr != nil {
			return openErr
		}
		identity, statErr := protectedIdentity(pending)
		aclErr := verifyProtectedACL(ctx, pending, nil)
		if err := errors.Join(statErr, aclErr, unix.Close(pending)); err != nil {
			return err
		}
		if identity != lease.record.Backing {
			return managedVMResourceValidationError("completed protected import candidate differs from record")
		}
		if err := unix.Renameat2(lease.directoryFD, "pending", lease.directoryFD, "disk", unix.RENAME_NOREPLACE); err != nil {
			return managedVMResourceAtomicError("publish protected backing inode", err)
		}
		fd, err = openProtectedFile(ctx, lease.directoryFD, "disk", unix.O_RDWR)
	}
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	identity, err := protectedIdentity(fd)
	if err != nil || identity != lease.record.Backing {
		return managedVMResourceAtomicError("published protected backing identity changed", err)
	}
	if err := unix.Fsync(lease.directoryFD); err != nil {
		return managedVMResourceAtomicError("sync protected backing publication", err)
	}
	lease.record.Phase = "ready"
	return writeProtectedRecord(ctx, lease.metadataFD, "record.json", lease.record)
}

func copyProtectedBytes(ctx context.Context, source, target int, size int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Ftruncate(target, 0); err != nil {
		return managedVMResourceAtomicError("reset incomplete protected copy", err)
	}
	// Reflinks create independent COW inodes; unlike a hard link, source aliases cannot write
	// the protected object. Unsupported filesystems use a bounded-buffer sparse copy.
	if err := unix.IoctlFileClone(target, source); err == nil {
		return unix.Fsync(target)
	} else if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.EXDEV) &&
		!errors.Is(err, unix.ENOTTY) && !errors.Is(err, unix.EINVAL) {
		return managedVMResourceAtomicError("clone initial protected backing", err)
	}
	buffer := make([]byte, 1<<20)
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return err
		}
		dataOffset, err := unix.Seek(source, offset, unix.SEEK_DATA)
		if errors.Is(err, unix.ENXIO) {
			break
		}
		if err == nil {
			if dataOffset < offset || dataOffset >= size {
				return managedVMResourceValidationError("protected copy source extent changed")
			}
			offset = dataOffset
		} else if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOTSUP) {
			return managedVMResourceAtomicError("locate protected copy source extent", err)
		}
		length := min(int64(len(buffer)), size-offset)
		n, err := unix.Pread(source, buffer[:length], offset)
		if err != nil || n == 0 {
			return managedVMResourceAtomicError("read initial protected backing", errors.Join(err, io.ErrNoProgress))
		}
		for written := 0; written < n; {
			count, err := unix.Pwrite(target, buffer[written:n], offset+int64(written))
			if err != nil || count == 0 {
				return managedVMResourceAtomicError(
					"write independent protected backing",
					errors.Join(err, io.ErrNoProgress),
				)
			}
			written += count
		}
		offset += int64(n)
	}
	if err := unix.Ftruncate(target, size); err != nil {
		return managedVMResourceAtomicError("set protected backing logical length", err)
	}
	return unix.Fsync(target)
}
