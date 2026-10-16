package jailer

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"strconv"

	"golang.org/x/sys/unix"
)

// Root-owned state ancestors may legitimately be separate filesystems, as in
// instance authority. Each component is pinned and admitted before use; record
// device/inode anchors reject replacement rather than forbidding /var mounts.
const vmRootfsExportDirectoryResolveFlags = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS

type vmRootfsExportNode struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type vmRootfsExportCopyRecord struct {
	Schema       uint32                 `json:"schema"`
	Source       vmRootfsExportProgress `json:"source"`
	Phase        string                 `json:"phase"`
	DataRoot     vmRootfsExportNode     `json:"data_root"`
	UIDDirectory vmRootfsExportNode     `json:"uid_directory"`
	Directory    vmRootfsExportNode     `json:"directory"`
	File         vmRootfsExportNode     `json:"file"`
	Marker       vmRootfsExportNode     `json:"marker"`
}

// The root-only logical lock/record outlive removal of their independent data.
// Export orders VM lifecycle then ImageID; release never acquires a VM lock.
type vmRootfsExportStoreEntry struct {
	dirs         *instanceUIDDirectories
	imageID      string
	metadata     int
	lock         int
	dataRoot     int
	uidDirectory int
	directory    int
	record       *vmRootfsExportCopyRecord
}

func lockVMRootfsExportStoreEntry(
	ctx context.Context,
	dirs *instanceUIDDirectories,
	imageID string,
) (_ *vmRootfsExportStoreEntry, returnErr error) {
	if !isCanonicalManagedResourceID(imageID, 64) {
		return nil, instanceValidationError("VM export copy ID is invalid")
	}
	entry := &vmRootfsExportStoreEntry{
		dirs:         dirs,
		imageID:      imageID,
		metadata:     -1,
		lock:         -1,
		dataRoot:     -1,
		uidDirectory: -1,
		directory:    -1,
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, entry.close(context.WithoutCancel(ctx)))
		}
	}()
	uid, err := openVMRootfsExportUIDDirectory(ctx, dirs, true)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(uid)) }()
	copies, err := openInstanceDirectory(ctx, dirs.deps, uid, "copies", true, instancePrivateDirectory, dirs.policy)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(copies)) }()
	entry.metadata, err = openInstanceDirectory(
		ctx,
		dirs.deps,
		copies,
		imageID,
		true,
		instancePrivateDirectory,
		dirs.policy,
	)
	if err != nil {
		return nil, err
	}
	entry.lock, err = acquireProtectedLock(ctx, entry.metadata)
	if err != nil {
		return nil, err
	}
	entry.record, err = entry.readRecord(ctx)
	if err != nil {
		return nil, err
	}
	return entry, nil
}

func (entry *vmRootfsExportStoreEntry) readRecord(ctx context.Context) (_ *vmRootfsExportCopyRecord, returnErr error) {
	fd, err := unix.Openat2(
		entry.metadata,
		"copy.json",
		&unix.OpenHow{Flags: instanceReadFlags | unix.O_NONBLOCK, Resolve: managedVMResourceResolveFlags},
	)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return nil, err
	}
	if before.Uid != 0 || before.Gid != 0 || before.Mode != unix.S_IFREG|0600 || before.Nlink != 1 ||
		before.Size <= 0 ||
		before.Size > vmRootfsExportProgressLimit {
		return nil, instanceValidationError("VM export copy record is not bounded root authority")
	}
	raw, err := readLaunchDeviceBytes(ctx, fd, int(before.Size))
	if err != nil {
		return nil, err
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil || !sameProtectedReadStat(before, after) {
		return nil, instanceAtomicError("VM export copy record changed", err)
	}
	var record vmRootfsExportCopyRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(canonical, raw) {
		return nil, instanceAtomicError("VM export copy record is not canonical", err)
	}
	if err := entry.validateRecord(record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (entry *vmRootfsExportStoreEntry) validateRecord(record vmRootfsExportCopyRecord) error {
	if record.Schema != 1 || record.Source.OwnerUID != entry.dirs.uid || record.Source.ImageID != entry.imageID ||
		record.DataRoot.Inode == 0 ||
		record.UIDDirectory.Inode == 0 {
		return instanceValidationError("VM export copy record ownership/anchor differs")
	}
	if err := record.Source.validate(record.Source.VMID); err != nil {
		return err
	}
	switch record.Phase {
	case "allocating", "allocated", "publishing", "ready", "releasing", "released":
	default:
		return instanceValidationError("VM export copy record phase is invalid")
	}
	if (record.Phase == "allocated" || record.Phase == "publishing" || record.Phase == "ready") &&
		(record.Directory.Inode == 0 || record.File.Inode == 0 || record.Marker.Inode == 0) {
		return instanceValidationError("VM export copy record is missing exact data identity")
	}
	return nil
}

func (entry *vmRootfsExportStoreEntry) writeRecord(ctx context.Context) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := entry.validateRecord(*entry.record); err != nil {
		return err
	}
	raw, err := json.Marshal(entry.record)
	if err != nil || len(raw) > vmRootfsExportProgressLimit {
		return instanceAtomicError("encode bounded VM export copy", err)
	}
	fd, err := unix.Openat(entry.metadata, ".", unix.O_TMPFILE|unix.O_WRONLY|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	if err := writeSnapshotBytes(ctx, fd, raw); err != nil {
		return err
	}
	if err := errors.Join(unix.Fchmod(fd, 0600), unix.Fsync(fd)); err != nil {
		return err
	}
	if err := unix.Unlinkat(entry.metadata, "copy.next", 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err := unix.Linkat(fd, "", entry.metadata, "copy.next", unix.AT_EMPTY_PATH); err != nil {
		return err
	}
	if err := unix.Renameat(entry.metadata, "copy.next", entry.metadata, "copy.json"); err != nil {
		return err
	}
	return unix.Fsync(entry.metadata)
}

func exportNode(fd int) (vmRootfsExportNode, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return vmRootfsExportNode{}, err
	}
	return vmRootfsExportNode{Device: uint64(stat.Dev), Inode: stat.Ino}, nil
}

func openVMRootfsExportDataDirectory(ctx context.Context, parent int, name string, create bool) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, err
	}
	fd, err := unix.Openat2(
		parent,
		name,
		&unix.OpenHow{Flags: instanceDirectoryFlags, Resolve: vmRootfsExportDirectoryResolveFlags},
	)
	if errors.Is(err, unix.ENOENT) && create {
		if err := unix.Mkdirat(parent, name, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, err
		}
		if err := unix.Fsync(parent); err != nil {
			return -1, err
		}
		fd, err = unix.Openat2(
			parent,
			name,
			&unix.OpenHow{Flags: instanceDirectoryFlags, Resolve: vmRootfsExportDirectoryResolveFlags},
		)
	}
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(
		fd,
		&stat,
	); err != nil || stat.Uid != 0 || stat.Gid != 0 || stat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		stat.Mode&07022 != 0 {
		return -1, errors.Join(instanceAtomicError("VM export directory is not root-controlled", err), unix.Close(fd))
	}
	return fd, nil
}

func (entry *vmRootfsExportStoreEntry) openDataParents(ctx context.Context, create bool) (returnErr error) {
	fd, err := entry.dirs.deps.open(ctx, entry.dirs.policy.rootPath, instanceDirectoryFlags, 0)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	if err := verifyInstanceDirectory(
		ctx,
		entry.dirs.deps,
		fd,
		"root",
		entry.dirs.policy,
		instanceAncestorDirectory,
	); err != nil {
		return err
	}
	for _, component := range []string{"var", "lib", "mvmctl"} {
		next, err := openVMRootfsExportDataDirectory(ctx, fd, component, create && component == "mvmctl")
		if err != nil {
			return err
		}
		closeErr := unix.Close(fd)
		fd = next
		if closeErr != nil {
			return closeErr
		}
	}
	if create {
		// The pinned root-owned inode is the shared state base, never a private child.
		// Deliberately grant traversal only; all private authority nodes stay 0700.
		if err := errors.Join(unix.Fchmod(fd, 0711), unix.Fsync(fd)); err != nil {
			return err
		}
	}
	entry.dataRoot, err = openVMRootfsExportDataDirectory(ctx, fd, "vm-rootfs-export-data", create)
	if err != nil {
		return err
	}
	if create {
		if err := errors.Join(unix.Fchmod(entry.dataRoot, 0711), unix.Fsync(entry.dataRoot)); err != nil {
			return err
		}
	}
	entry.uidDirectory, err = openVMRootfsExportDataDirectory(
		ctx,
		entry.dataRoot,
		strconv.FormatUint(uint64(entry.dirs.uid), 10),
		create,
	)
	if err != nil {
		return err
	}
	if create {
		if err := setVMRootfsExportReadACL(ctx, entry.uidDirectory, entry.dirs.uid, true); err != nil {
			return err
		}
	}
	if entry.record != nil {
		root, rootErr := exportNode(entry.dataRoot)
		uid, uidErr := exportNode(entry.uidDirectory)
		if rootErr != nil || uidErr != nil || root != entry.record.DataRoot || uid != entry.record.UIDDirectory {
			return instanceAtomicError("VM export data parent anchor changed", errors.Join(rootErr, uidErr))
		}
	}
	return nil
}

func vmRootfsExportReadACL(uid uint32, directory bool) []byte {
	owner, caller := uint16(4), uint16(4)
	if directory {
		owner, caller = 7, 5
	}
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, 2)
	for _, item := range []struct {
		tag, permission uint16
		id              uint32
	}{
		{1, owner, math.MaxUint32}, {2, caller, uid}, {4, 0, math.MaxUint32}, {16, caller, math.MaxUint32}, {32, 0, math.MaxUint32},
	} {
		data = binary.LittleEndian.AppendUint16(data, item.tag)
		data = binary.LittleEndian.AppendUint16(data, item.permission)
		data = binary.LittleEndian.AppendUint32(data, item.id)
	}
	return data
}

func verifyVMRootfsExportReadACL(ctx context.Context, fd int, uid uint32, directory bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	want := vmRootfsExportReadACL(uid, directory)
	got := make([]byte, len(want)+1)
	n, err := unix.Fgetxattr(fd, protectedBackingACL, got)
	if err != nil || n != len(want) || !bytes.Equal(got[:n], want) {
		return instanceAtomicError("VM export read ACL differs", err)
	}
	return nil
}

func setVMRootfsExportReadACL(ctx context.Context, fd int, uid uint32, directory bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if directory {
		if err := unix.Fremovexattr(fd, "system.posix_acl_default"); err != nil && !errors.Is(err, unix.ENODATA) {
			return err
		}
	}
	if err := unix.Fsetxattr(fd, protectedBackingACL, vmRootfsExportReadACL(uid, directory), 0); err != nil {
		return instanceAtomicError("VM export filesystem requires exact read ACL support", err)
	}
	if err := verifyVMRootfsExportReadACL(ctx, fd, uid, directory); err != nil {
		return err
	}
	return unix.Fsync(fd)
}

func (entry *vmRootfsExportStoreEntry) close(ctx context.Context) error {
	var result error
	for _, fd := range []*int{&entry.directory, &entry.uidDirectory, &entry.dataRoot, &entry.lock, &entry.metadata} {
		if *fd >= 0 {
			result = errors.Join(result, unix.Close(*fd))
			*fd = -1
		}
	}
	return result
}

// The read-only sentinel survives data removal, so the installed CLI can retry
// root-only parent-sync/tombstone reconciliation without guessing metadata.
func (entry *vmRootfsExportStoreEntry) createPresenceMarker(ctx context.Context) (returnErr error) {
	fd, err := unix.Openat2(
		entry.uidDirectory,
		entry.imageID+".copy",
		&unix.OpenHow{
			Flags:   unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW,
			Mode:    0600,
			Resolve: managedVMResourceResolveFlags,
		},
	)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	if err := writeSnapshotBytes(ctx, fd, []byte(entry.imageID)); err != nil {
		return err
	}
	if err := setVMRootfsExportReadACL(ctx, fd, entry.dirs.uid, false); err != nil {
		return err
	}
	entry.record.Marker, err = exportNode(fd)
	if err != nil {
		return err
	}
	if err := unix.Fsync(entry.uidDirectory); err != nil {
		return err
	}
	return entry.writeRecord(ctx)
}
