package jailer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"

	"golang.org/x/sys/unix"
)

func hashProtectedKernel(ctx context.Context, fd int, expected protectedBackingIdentity) (string, error) {
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return "", err
	}
	identity := protectedBackingIdentity{Device: uint64(before.Dev), Inode: before.Ino, Size: before.Size}
	if identity != expected || before.Uid != 0 || before.Gid != 0 || before.Nlink != 1 ||
		before.Mode&unix.S_IFMT != unix.S_IFREG || before.Size <= 0 {
		return "", managedVMResourceValidationError("protected kernel digest input differs from root authority")
	}
	hash := sha256.New()
	buffer := make([]byte, 1<<20)
	for offset := int64(0); offset < before.Size; {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		length := min(int64(len(buffer)), before.Size-offset)
		n, err := unix.Pread(fd, buffer[:length], offset)
		if err != nil || n != int(length) {
			return "", managedVMResourceAtomicError(
				"hash pinned protected kernel",
				errors.Join(err, io.ErrUnexpectedEOF),
			)
		}
		_, _ = hash.Write(buffer[:n]) // SHA-256 writes cannot fail.
		offset += int64(n)
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || !sameProtectedReadStat(before, after) {
		return "", managedVMResourceAtomicError("protected kernel changed while hashing", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (lease *protectedBackingLease) beginKernelReimport(ctx context.Context) error {
	if lease.record.Kind != protectedBackingKernel || lease.record.Phase != "removed" ||
		!isCanonicalManagedResourceID(lease.record.KernelDigest, 64) || lease.record.Generation == math.MaxUint64 ||
		len(lease.record.References) != 0 || len(lease.record.NextReferences) != 0 {
		return managedVMResourceValidationError("kernel retirement cannot authorize reimport")
	}
	var stat unix.Stat_t
	for _, leaf := range []string{"disk", "pending"} {
		if err := unix.Fstatat(lease.directoryFD, leaf, &stat, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
			return managedVMResourceAtomicError("retired kernel has unexpected backing", err)
		}
	}
	lease.record.Source = protectedBackingIdentity{}
	lease.record.Backing = protectedBackingIdentity{}
	lease.record.SnapshotSource = false
	lease.record.Phase = "kernel-reimport-copying"
	return writeProtectedRecord(ctx, lease.metadataFD, "record.json", lease.record)
}

func (lease *protectedBackingLease) requireRetiredKernelDigest(ctx context.Context, candidate int) error {
	if lease.record.Kind != protectedBackingKernel || !isCanonicalManagedResourceID(lease.record.KernelDigest, 64) ||
		lease.record.Generation == math.MaxUint64 {
		return managedVMResourceValidationError("kernel reimport has no retained root digest or generation")
	}
	digest, err := hashProtectedKernel(ctx, candidate, lease.record.Backing)
	if err != nil {
		return err
	}
	if digest != lease.record.KernelDigest {
		return managedVMResourceValidationError("kernel reimport bytes differ from retired protected kernel")
	}
	return nil
}
