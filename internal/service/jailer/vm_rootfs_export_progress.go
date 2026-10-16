package jailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"golang.org/x/sys/unix"
)

const vmRootfsExportProgressLimit = 16384

// A single per-VM journal remains outside caller storage and survives receiver death.
// Mutations require the exact source VM lifecycle lock. Atomic readback may also
// conservatively report pending source recovery during independent copy release.
type vmRootfsExportJournal struct {
	directory int
	vmID      string
}

type vmRootfsExportProgress struct {
	Schema        uint32                  `json:"schema"`
	OwnerUID      uint32                  `json:"owner_uid"`
	OwnerGID      uint32                  `json:"owner_gid"`
	VMID          string                  `json:"vm_id"`
	ImageID       string                  `json:"image_id"`
	CacheRoot     string                  `json:"cache_root"`
	Generation    uint64                  `json:"generation"`
	BootID        string                  `json:"boot_id"`
	Rootfs        protectedBackingBinding `json:"rootfs"`
	OriginalState string                  `json:"original_state"`
	Stage         string                  `json:"stage"`
	OutputDevice  uint64                  `json:"output_device"`
	OutputInode   uint64                  `json:"output_inode"`
	ParentDevice  uint64                  `json:"parent_device"`
	ParentInode   uint64                  `json:"parent_inode"`
}

func openVMRootfsExportJournal(
	ctx context.Context, dirs *instanceUIDDirectories, vmID string, create bool,
) (_ *vmRootfsExportJournal, returnErr error) {
	if !vmIDPattern.MatchString(vmID) {
		return nil, instanceValidationError("VM export journal identity is invalid")
	}
	fd, err := openVMRootfsExportUIDDirectory(ctx, dirs, create)
	if err != nil {
		return nil, err
	}
	return &vmRootfsExportJournal{directory: fd, vmID: vmID}, nil
}

func openVMRootfsExportUIDDirectory(
	ctx context.Context,
	dirs *instanceUIDDirectories,
	create bool,
) (_ int, returnErr error) {
	fd, err := dirs.deps.open(ctx, dirs.policy.rootPath, instanceDirectoryFlags, 0)
	if err != nil {
		return -1, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, dirs.deps.close(context.WithoutCancel(ctx), fd))
		}
	}()
	if err := verifyInstanceDirectory(ctx, dirs.deps, fd, "root", dirs.policy, instanceAncestorDirectory); err != nil {
		return -1, err
	}
	for index, name := range []string{"var", "lib", "mvmctl", "vm-rootfs-exports", strconv.FormatUint(uint64(dirs.uid), 10)} {
		class := instanceAncestorDirectory
		if index == 2 {
			class = instanceStateBaseDirectory
		} else if index > 2 {
			class = instancePrivateDirectory
		}
		next, err := openInstanceDirectory(ctx, dirs.deps, fd, name, create && index >= 2, class, dirs.policy)
		if err != nil {
			return -1, err
		}
		old := fd
		fd = next
		if err := dirs.deps.close(context.WithoutCancel(ctx), old); err != nil {
			return -1, err
		}
	}
	return fd, nil
}

func (journal *vmRootfsExportJournal) read(ctx context.Context) (
	_ *vmRootfsExportProgress, returnErr error,
) {
	fd, err := unix.Openat2(journal.directory, journal.vmID+".json", &unix.OpenHow{
		Flags: instanceReadFlags | unix.O_NONBLOCK, Resolve: managedVMResourceResolveFlags,
	})
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	before, err := stableManagedVMConfigStat(ctx, realManagedFirecrackerConfigDeps(), fd)
	if err != nil {
		return nil, err
	}
	if before.uid != 0 || before.gid != 0 || before.mode != unix.S_IFREG|0600 || before.nlink != 1 ||
		before.size == 0 || before.size > vmRootfsExportProgressLimit {
		return nil, instanceStateError(journal.vmID, "VM export progress metadata is not admitted")
	}
	raw, err := readLaunchDeviceBytes(ctx, fd, int(before.size))
	if err != nil {
		return nil, err
	}
	after, err := stableManagedVMConfigStat(ctx, realManagedFirecrackerConfigDeps(), fd)
	if err != nil || before != after {
		return nil, instanceAtomicError("VM export progress changed during read", err)
	}
	var progress vmRootfsExportProgress
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&progress); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(progress)
	if err != nil || !bytes.Equal(raw, canonical) {
		return nil, instanceAtomicError("VM export progress is not canonical", err)
	}
	if err := progress.validate(journal.vmID); err != nil {
		return nil, err
	}
	return &progress, nil
}

func (progress vmRootfsExportProgress) validate(vmID string) error {
	if progress.Schema != 1 || progress.VMID != vmID || progress.OwnerUID == 0 || progress.Generation == 0 ||
		progress.BootID == "" || !validVMRootfsExportState(progress.OriginalState) ||
		progress.Rootfs.ownerUID != progress.OwnerUID || progress.Rootfs.kind != protectedBackingRootfs ||
		progress.Rootfs.logicalID != vmID {
		return instanceStateError(vmID, "VM export progress binding is invalid")
	}
	if err := (VMRootfsExportRequest{VMID: vmID, ImageID: progress.ImageID, CacheRoot: progress.CacheRoot}).Validate(); err != nil {
		return err
	}
	switch progress.Stage {
	case "prepared", "pause_inflight", "paused", "copying", "copy_quiesced", "resume_inflight", "restored", "complete":
		return nil
	default:
		return instanceStateError(vmID, "VM export progress stage is invalid")
	}
}

func (journal *vmRootfsExportJournal) write(ctx context.Context, progress vmRootfsExportProgress) (returnErr error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := progress.validate(journal.vmID); err != nil {
		return err
	}
	raw, err := json.Marshal(progress)
	if err != nil || len(raw) > vmRootfsExportProgressLimit {
		return instanceAtomicError("encode bounded VM export progress", err)
	}
	fd, err := unix.Openat(journal.directory, ".", unix.O_TMPFILE|unix.O_WRONLY|unix.O_CLOEXEC, 0600)
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
	temporary := journal.vmID + ".tmp"
	// The root-only per-VM directory name and held lifecycle lock make an abandoned
	// temporary link safe to remove; caller namespace state never selects this target.
	if err := unix.Unlinkat(journal.directory, temporary, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err := unix.Linkat(fd, "", journal.directory, temporary, unix.AT_EMPTY_PATH); err != nil {
		return err
	}
	if err := unix.Renameat(journal.directory, temporary, journal.directory, journal.vmID+".json"); err != nil {
		return err
	}
	return unix.Fsync(journal.directory)
}

func (journal *vmRootfsExportJournal) remove(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := unix.Unlinkat(journal.directory, journal.vmID+".json", 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return unix.Fsync(journal.directory)
}

func (journal *vmRootfsExportJournal) close(ctx context.Context) error {
	return unix.Close(journal.directory)
}

// requireVMRootfsExportIdle is private admission for control/capture/rebind only.
// Termination deliberately bypasses it so an unresolved API outcome cannot trap a VM.
func requireVMRootfsExportIdle(
	ctx context.Context,
	dirs *instanceUIDDirectories,
	record instanceRecord,
) (returnErr error) {
	journal, err := openVMRootfsExportJournal(ctx, dirs, record.vmID, false)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return vmRootfsExportError(record.vmID, "unknown", err)
	}
	defer func() { returnErr = errors.Join(returnErr, journal.close(context.WithoutCancel(ctx))) }()
	progress, err := journal.read(ctx)
	if err != nil {
		return vmRootfsExportError(record.vmID, "unknown", err)
	}
	if progress != nil && progress.Stage != "complete" {
		return vmRootfsExportError(record.vmID, "unknown", instanceStateError(record.vmID,
			"VM rootfs export requires recovery; repeat image import or stop the VM"))
	}
	return nil
}
