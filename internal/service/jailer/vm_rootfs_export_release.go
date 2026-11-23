package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// Release removes only this caller's root-recorded independent output. It never
// needs a live source or changes a pending original-state recovery decision.
func (server *VMRootfsExportServer) Release(
	ctx context.Context,
	ownerUID, ownerGID uint32,
	request VMRootfsExportReleaseRequest,
) (_ VMRootfsExportReleaseResult, returnErr error) {
	defer func() {
		if returnErr != nil {
			returnErr = errs.WrapMsg(
				errs.CodeImageImportFailed,
				"VM export copy release did not complete",
				returnErr,
				errs.WithDetails(map[string]any{"image_id": request.ImageID, "rootfs_export_copy_pending": true}),
			)
		}
	}()
	if err := request.Validate(); err != nil {
		return VMRootfsExportReleaseResult{}, err
	}
	if ctx == nil {
		return VMRootfsExportReleaseResult{}, instanceValidationError("VM export release context is required")
	}
	if err := validateInstanceCaller(instanceCaller{uid: ownerUID, gid: ownerGID}); err != nil {
		return VMRootfsExportReleaseResult{}, err
	}
	dirs := &instanceUIDDirectories{deps: server.instances.deps, policy: server.instances.policy, uid: ownerUID}
	entry, err := lockVMRootfsExportStoreEntry(ctx, dirs, request.ImageID)
	if err != nil {
		return VMRootfsExportReleaseResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, entry.close(context.WithoutCancel(ctx))) }()
	result := VMRootfsExportReleaseResult{ImageID: request.ImageID, Released: true, Found: entry.record != nil}
	if entry.record != nil {
		result.SourceVMID = entry.record.Source.VMID
		// Copy removal is independent of source-state recovery. An unreadable
		// source journal cannot claim known restoration or block retiring bytes.
		result.SourceRecoveryPending = true
		journal, err := openVMRootfsExportJournal(ctx, dirs, result.SourceVMID, false)
		if err == nil {
			progress, readErr := journal.read(ctx)
			if errors.Join(readErr, journal.close(context.WithoutCancel(ctx))) == nil {
				result.SourceRecoveryPending = progress != nil && progress.ImageID == request.ImageID &&
					progress.Stage != "complete"
			}
		}
	}
	if err := entry.releaseData(ctx); err != nil {
		return VMRootfsExportReleaseResult{}, err
	}
	return result, nil
}

func (entry *vmRootfsExportStoreEntry) releaseData(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if entry.dataRoot < 0 {
		if err := entry.openDataParents(ctx, false); err != nil {
			if errors.Is(err, unix.ENOENT) && (entry.record == nil || entry.record.Phase == "released") {
				return nil
			}
			return err
		}
	}
	var named unix.Stat_t
	err := unix.Fstatat(entry.uidDirectory, entry.imageID, &named, unix.AT_SYMLINK_NOFOLLOW)
	if entry.record == nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return instanceAtomicError("unrecorded VM export data cannot be adopted for release", err)
	}
	if entry.record.Phase == "released" {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return instanceAtomicError("retired VM export data reappeared; retained for inspection", err)
	}
	entry.record.Phase = "releasing"
	if err := entry.writeRecord(ctx); err != nil {
		return err
	}
	if errors.Is(err, unix.ENOENT) {
		// The prior attempt may have removed the directory before parent sync.
		if err := unix.Fsync(entry.uidDirectory); err != nil {
			return err
		}
		entry.record.Phase = "released"
		return entry.writeRecord(ctx)
	}
	if err != nil {
		return err
	}
	if named.Uid != 0 || named.Gid != 0 || named.Mode&unix.S_IFMT != unix.S_IFDIR || named.Mode&07022 != 0 {
		return instanceValidationError("VM export release directory is not root controlled")
	}
	// CRITICAL: A name or empty root-owned directory cannot replace the
	// durable identity checkpoint, including the crash-before-identity window.
	if entry.record.Directory.Inode == 0 ||
		uint64(named.Dev) != entry.record.Directory.Device || named.Ino != entry.record.Directory.Inode {
		return instanceValidationError("VM export release directory identity changed")
	}
	if entry.directory < 0 {
		entry.directory, err = openVMRootfsExportDataDirectory(ctx, entry.uidDirectory, entry.imageID, false)
		if err != nil {
			return err
		}
	}
	identity, err := exportNode(entry.directory)
	if err != nil || identity.Device != uint64(named.Dev) || identity.Inode != named.Ino {
		return instanceAtomicError("VM export release directory was replaced", err)
	}
	err = unix.Fstatat(entry.directory, "disk.raw", &named, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		if uint64(named.Dev) != entry.record.File.Device || named.Ino != entry.record.File.Inode || named.Uid != 0 ||
			named.Gid != 0 ||
			named.Mode&unix.S_IFMT != unix.S_IFREG ||
			named.Nlink != 1 ||
			named.Mode&07007 != 0 ||
			named.Size != int64(entry.record.Source.Rootfs.bytes) {
			return instanceValidationError("VM export release leaf differs from exact root copy")
		}
		// No operator has directory-write authority. All root export operations
		// retain this same never-unlinked ImageID lock, closing the former race.
		if err := unix.Unlinkat(entry.directory, "disk.raw", 0); err != nil {
			return err
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err := unix.Fsync(entry.directory); err != nil {
		return err
	}
	if err := unix.Unlinkat(entry.uidDirectory, entry.imageID, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	if err := unix.Fsync(entry.uidDirectory); err != nil {
		return err
	}
	entry.record.Phase = "released"
	return entry.writeRecord(ctx)
}
