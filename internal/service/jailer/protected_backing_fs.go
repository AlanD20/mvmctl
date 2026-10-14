package jailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"golang.org/x/sys/unix"
)

const protectedBackingResolve = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS |
	unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV

func openProtectedDirectory(ctx context.Context, parent int, name string, create, private bool) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, managedVMResourceAtomicError("open protected backing directory", err)
	}
	resolve := uint64(protectedBackingResolve)
	if !private {
		resolve &^= unix.RESOLVE_NO_XDEV
	}
	fd, err := unix.Openat2(parent, name, &unix.OpenHow{
		Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: resolve,
	})
	if errors.Is(err, unix.ENOENT) && create {
		if err := unix.Mkdirat(parent, name, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, managedVMResourceAtomicError("create protected backing directory", err)
		}
		if err := unix.Fsync(parent); err != nil {
			return -1, managedVMResourceAtomicError("sync protected backing directory parent", err)
		}
		fd, err = unix.Openat2(parent, name, &unix.OpenHow{
			Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: resolve,
		})
	}
	if err != nil {
		return -1, managedVMResourceAtomicError("pin protected backing directory", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return -1, errors.Join(managedVMResourceAtomicError("inspect protected backing directory", err), unix.Close(fd))
	}
	if stat.Uid != 0 || stat.Gid != 0 || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&07022 != 0 ||
		(private && stat.Mode&07777 != 0700) {
		return -1, errors.Join(
			managedVMResourceValidationError("protected backing directory has unsafe ownership or mode"),
			unix.Close(fd),
		)
	}
	return fd, nil
}

func openProtectedFile(ctx context.Context, directory int, name string, flags int) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, managedVMResourceAtomicError("open protected backing file", err)
	}
	mode := uint64(0)
	if flags&unix.O_CREAT != 0 {
		mode = 0600
	}
	fd, err := unix.Openat2(directory, name, &unix.OpenHow{
		Flags: uint64(flags | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK), Mode: mode,
		Resolve: protectedBackingResolve,
	})
	if err != nil {
		return -1, managedVMResourceAtomicError("open protected backing file", err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return -1, errors.Join(managedVMResourceAtomicError("inspect protected backing file", err), unix.Close(fd))
	}
	if stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
		stat.Mode&07007 != 0 || stat.Mode&0600 != 0600 {
		return -1, errors.Join(
			managedVMResourceValidationError("protected backing file has unsafe metadata"),
			unix.Close(fd),
		)
	}
	return fd, nil
}

func acquireProtectedLock(ctx context.Context, directory int) (int, error) {
	return acquireProtectedLockMode(ctx, directory, true)
}

func acquireProtectedLockMode(ctx context.Context, directory int, create bool) (int, error) {
	flags := unix.O_RDWR
	if create {
		flags |= unix.O_CREAT
	}
	fd, err := openProtectedFile(ctx, directory, "lock", flags)
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Size != 0 || stat.Mode&07777 != 0600 {
		return -1, errors.Join(managedVMResourceAtomicError("inspect protected backing lock", err), unix.Close(fd))
	}
	// Never wait while another object's lock may be held. The orchestrator orders complete sets
	// by kind/ID and retries contention without a cross-VM lock-order cycle.
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return -1, errors.Join(instanceLockContextError("protected backing object", err), unix.Close(fd))
	}
	return fd, nil
}

func protectedIdentity(fd int) (protectedBackingIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return protectedBackingIdentity{}, managedVMResourceAtomicError("inspect protected backing identity", err)
	}
	return protectedBackingIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Size: stat.Size}, nil
}

func sameProtectedNode(left, right protectedBackingIdentity) bool {
	return left.Device == right.Device && left.Inode == right.Inode && left.Inode != 0
}

func readProtectedRecord[
	T protectedBackingAnchor | protectedBackingRecord | protectedLogicalRecord | protectedKernelReceipt,
](
	ctx context.Context, directory int, name string, record *T,
) (returnErr error) {
	fd, err := openProtectedFile(ctx, directory, name, unix.O_RDONLY)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil || before.Size <= 0 || before.Size > protectedBackingRecordLimit ||
		before.Mode&07777 != 0600 {
		return managedVMResourceAtomicError("protected backing record is not bounded root-only metadata", err)
	}
	data := make([]byte, int(before.Size))
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := unix.Pread(fd, data[offset:], int64(offset))
		if err != nil || n == 0 {
			return managedVMResourceAtomicError("read protected backing record", errors.Join(err, io.ErrNoProgress))
		}
		offset += n
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || !sameProtectedReadStat(before, after) {
		return managedVMResourceAtomicError("protected backing record changed during read", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(record); err != nil {
		return managedVMResourceAtomicError("decode protected backing record", err)
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(data, append(canonical, '\n')) {
		return managedVMResourceAtomicError("protected backing record is not canonical", err)
	}
	return nil
}

func sameProtectedReadStat(before, after unix.Stat_t) bool {
	// Reading may advance atime; changes to content, ownership, linkage or identity may not.
	before.Atim = after.Atim
	return before == after
}

func writeProtectedRecord[
	T protectedBackingAnchor | protectedBackingRecord | protectedLogicalRecord | protectedKernelReceipt,
](
	ctx context.Context, directory int, name string, record T,
) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil || len(data)+1 > protectedBackingRecordLimit {
		return managedVMResourceAtomicError("encode bounded protected backing record", err)
	}
	next := name + ".next"
	// A prior interrupted atomic record write is removable only after strict admission as the
	// same closed record type in this locked root-owned object directory.
	var abandoned T
	if err := readProtectedRecord(ctx, directory, next, &abandoned); err == nil {
		if err := unix.Unlinkat(directory, next, 0); err != nil {
			return managedVMResourceAtomicError("remove admitted incomplete backing record", err)
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	fd, err := openProtectedFile(ctx, directory, next, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	data = append(data, '\n')
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := unix.Pwrite(fd, data[offset:], int64(offset))
		if err != nil || n == 0 {
			return managedVMResourceAtomicError("write protected backing record", errors.Join(err, io.ErrNoProgress))
		}
		offset += n
	}
	if err := unix.Fsync(fd); err != nil {
		return managedVMResourceAtomicError("sync protected backing record", err)
	}
	if err := unix.Renameat(directory, next, directory, name); err != nil {
		return managedVMResourceAtomicError("publish protected backing record", err)
	}
	if err := unix.Fsync(directory); err != nil {
		return managedVMResourceAtomicError("sync protected backing record directory", err)
	}
	return nil
}

func closeProtectedFDs(fds []int) error {
	var err error
	for index := len(fds) - 1; index >= 0; index-- {
		err = errors.Join(err, unix.Close(fds[index]))
	}
	return err
}
