package jailer

import (
	"context"
	"errors"
	"io"
	"math"

	"golang.org/x/sys/unix"
)

// Anonymous incomplete bytes are destroyed when the receiver's last FD closes.
type vmRootfsExportOutput struct {
	entry    *vmRootfsExportStoreEntry
	fd       int
	identity unix.Stat_t
}

func prepareVMRootfsExportOutput(
	ctx context.Context,
	dirs *instanceUIDDirectories,
	progress vmRootfsExportProgress,
) (_ *vmRootfsExportOutput, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry, err := lockVMRootfsExportStoreEntry(ctx, dirs, progress.ImageID)
	if err != nil {
		return nil, err
	}
	output := &vmRootfsExportOutput{entry: entry, fd: -1}
	ownsAllocation := false
	defer func() {
		if returnErr != nil {
			if ownsAllocation {
				returnErr = errors.Join(returnErr, entry.releaseData(context.WithoutCancel(ctx)))
			}
			returnErr = errors.Join(returnErr, output.close(context.WithoutCancel(ctx)))
		}
	}()
	if entry.record != nil {
		return nil, instanceStateError(progress.VMID, "VM export image ID is already allocated or retired")
	}
	if err := entry.openDataParents(ctx, true); err != nil {
		return nil, err
	}
	root, err := exportNode(entry.dataRoot)
	if err != nil {
		return nil, err
	}
	uid, err := exportNode(entry.uidDirectory)
	if err != nil {
		return nil, err
	}
	entry.record = &vmRootfsExportCopyRecord{
		Schema:       1,
		Source:       progress,
		Phase:        "allocating",
		DataRoot:     root,
		UIDDirectory: uid,
	}
	ownsAllocation = true
	if err := entry.writeRecord(ctx); err != nil {
		return nil, err
	}
	if err := entry.createPresenceMarker(ctx); err != nil {
		return nil, err
	}
	if err := unix.Mkdirat(entry.uidDirectory, progress.ImageID, 0700); err != nil {
		return nil, err
	}
	if err := unix.Fsync(entry.uidDirectory); err != nil {
		return nil, err
	}
	entry.directory, err = openVMRootfsExportDataDirectory(ctx, entry.uidDirectory, progress.ImageID, false)
	if err != nil {
		return nil, err
	}
	entry.record.Directory, err = exportNode(entry.directory)
	if err != nil {
		return nil, err
	}
	// CRITICAL: A receiver dying before this checkpoint leaves an unknown
	// directory, never inferred deletion authority for a later receiver.
	if err := entry.writeRecord(ctx); err != nil {
		return nil, err
	}
	output.fd, err = unix.Openat(entry.directory, ".", unix.O_TMPFILE|unix.O_RDWR|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	// Both read ACL policies must be supported before source pause can be sent.
	if err := setVMRootfsExportReadACL(ctx, output.fd, dirs.uid, false); err != nil {
		return nil, err
	}
	if err := setVMRootfsExportReadACL(ctx, entry.directory, dirs.uid, true); err != nil {
		return nil, err
	}
	if err := errors.Join(unix.Fchmod(output.fd, 0600), unix.Fchmod(entry.directory, 0700)); err != nil {
		return nil, err
	}
	if err := unix.Fstat(output.fd, &output.identity); err != nil {
		return nil, err
	}
	if output.identity.Mode != unix.S_IFREG|0600 || output.identity.Uid != 0 || output.identity.Gid != 0 ||
		output.identity.Nlink != 0 ||
		output.identity.Size != 0 {
		return nil, instanceValidationError("VM export anonymous candidate is not private")
	}
	entry.record.File = vmRootfsExportNode{Device: uint64(output.identity.Dev), Inode: output.identity.Ino}
	entry.record.Phase = "allocated"
	if err := entry.writeRecord(ctx); err != nil {
		return nil, err
	}
	return output, nil
}

func copyVMRootfsExportBytes(ctx context.Context, source, target int, length uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if length == 0 || length > math.MaxInt64 {
		return instanceValidationError("VM export copy extent is invalid")
	}
	var before unix.Stat_t
	if err := unix.Fstat(source, &before); err != nil {
		return err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Size != int64(length) || before.Nlink != 1 {
		return instanceValidationError("VM export protected source extent is invalid")
	}
	if err := unix.Ftruncate(target, 0); err != nil {
		return err
	}
	buffer := make([]byte, 1<<20)
	for offset := uint64(0); offset < length; {
		if err := ctx.Err(); err != nil {
			return err
		}
		part := buffer[:min(uint64(len(buffer)), length-offset)]
		n, err := unix.Pread(source, part, int64(offset))
		if err != nil || n == 0 {
			return errors.Join(err, io.ErrUnexpectedEOF)
		}
		for written := 0; written < n; {
			if err := ctx.Err(); err != nil {
				return err
			}
			count, err := unix.Pwrite(target, part[written:n], int64(offset)+int64(written))
			if err != nil || count == 0 {
				return errors.Join(err, io.ErrNoProgress)
			}
			written += count
		}
		offset += uint64(n)
	}
	var probe [1]byte
	n, err := unix.Pread(source, probe[:], int64(length))
	if err != nil || n != 0 {
		return instanceAtomicError("VM export source extent changed", err)
	}
	var after unix.Stat_t
	if err := unix.Fstat(source, &after); err != nil || !sameProtectedReadStat(before, after) {
		return instanceAtomicError("VM export source changed during copy", err)
	}
	return unix.Fsync(target)
}

func (output *vmRootfsExportOutput) publish(ctx context.Context, length uint64) (VMRootfsExportResult, error) {
	if err := ctx.Err(); err != nil {
		return VMRootfsExportResult{}, err
	}
	output.entry.record.Phase = "publishing"
	if err := output.entry.writeRecord(ctx); err != nil {
		return VMRootfsExportResult{}, err
	}
	if err := errors.Join(unix.Fchmod(output.fd, 0400), unix.Fsync(output.fd)); err != nil {
		return VMRootfsExportResult{}, err
	}
	if err := unix.Fstat(output.fd, &output.identity); err != nil {
		return VMRootfsExportResult{}, err
	}
	if output.identity.Size != int64(length) || output.identity.Nlink != 0 ||
		output.identity.Mode != unix.S_IFREG|0400 ||
		output.identity.Uid != 0 ||
		output.identity.Gid != 0 {
		return VMRootfsExportResult{}, instanceValidationError("VM export output metadata changed")
	}
	if err := unix.Linkat(output.fd, "", output.entry.directory, "disk.raw", unix.AT_EMPTY_PATH); err != nil {
		return VMRootfsExportResult{}, err
	}
	if err := unix.Fsync(output.entry.directory); err != nil {
		return VMRootfsExportResult{}, err
	}
	var named unix.Stat_t
	if err := unix.Fstatat(output.entry.directory, "disk.raw", &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return VMRootfsExportResult{}, err
	}
	if named.Dev != output.identity.Dev || named.Ino != output.identity.Ino || named.Nlink != 1 ||
		named.Mode != unix.S_IFREG|0400 ||
		named.Size != int64(length) ||
		named.Uid != 0 ||
		named.Gid != 0 {
		return VMRootfsExportResult{}, instanceValidationError("VM export publication identity changed")
	}
	if err := setVMRootfsExportReadACL(ctx, output.fd, output.entry.dirs.uid, false); err != nil {
		return VMRootfsExportResult{}, err
	}
	if err := setVMRootfsExportReadACL(ctx, output.entry.directory, output.entry.dirs.uid, true); err != nil {
		return VMRootfsExportResult{}, err
	}
	output.entry.record.Phase = "ready"
	if err := output.entry.writeRecord(ctx); err != nil {
		return VMRootfsExportResult{}, err
	}
	return VMRootfsExportResult{
		OwnerUID: output.entry.dirs.uid,
		ImageID:  output.entry.imageID,
		Bytes:    length,
		Device:   uint64(output.identity.Dev),
		Inode:    output.identity.Ino,
	}, nil
}

func (output *vmRootfsExportOutput) discard(ctx context.Context) error {
	return output.entry.releaseData(ctx)
}

func (output *vmRootfsExportOutput) close(ctx context.Context) error {
	var result error
	if output.fd >= 0 {
		result = unix.Close(output.fd)
		output.fd = -1
	}
	return errors.Join(result, output.entry.close(context.WithoutCancel(ctx)))
}
