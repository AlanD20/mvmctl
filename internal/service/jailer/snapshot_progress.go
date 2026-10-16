package jailer

import (
	"context"
	"encoding/json"
	"errors"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const snapshotProgressLeaf = "capture-progress.json"
const snapshotProgressTemporaryLeaf = ".capture-progress.tmp"

type snapshotCaptureProgress struct {
	SnapshotID       string `json:"snapshot_id"`
	SourceVMID       string `json:"source_vm_id"`
	OwnerUID         uint32 `json:"owner_uid"`
	LaunchGeneration uint64 `json:"launch_generation"`
	Stage            string `json:"stage"`
	VMState          string `json:"vm_state"`
	Published        bool   `json:"published"`
}

func (entry *snapshotStoreEntry) writeProgress(
	ctx context.Context,
	progress snapshotCaptureProgress,
) (returnErr error) {
	data, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	fd, err := unix.Openat2(entry.directoryFD, snapshotProgressTemporaryLeaf, &unix.OpenHow{
		Flags:   unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Mode:    0600,
		Resolve: managedVMResourceResolveFlags,
	})
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	if err := writeSnapshotBytes(ctx, fd, data); err != nil {
		return err
	}
	if err := unix.Fsync(fd); err != nil {
		return err
	}
	if err := unix.Renameat(
		entry.directoryFD,
		snapshotProgressTemporaryLeaf,
		entry.directoryFD,
		snapshotProgressLeaf,
	); err != nil {
		return err
	}
	return unix.Fsync(entry.directoryFD)
}

func (entry *snapshotStoreEntry) readProgress(ctx context.Context) (_ snapshotCaptureProgress, returnErr error) {
	fd, err := unix.Openat2(entry.directoryFD, snapshotProgressLeaf, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: managedVMResourceResolveFlags,
	})
	if err != nil {
		return snapshotCaptureProgress{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return snapshotCaptureProgress{}, err
	}
	if stat.Mode != unix.S_IFREG|0600 || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 || stat.Size < 1 ||
		stat.Size > 4096 {
		return snapshotCaptureProgress{}, instanceValidationError("snapshot progress record is not admitted")
	}
	data, err := readLaunchDeviceBytes(ctx, fd, int(stat.Size))
	if err != nil {
		return snapshotCaptureProgress{}, err
	}
	var progress snapshotCaptureProgress
	if err := json.Unmarshal(data, &progress); err != nil {
		return progress, err
	}
	if progress.SnapshotID != entry.snapshotID || !vmIDPattern.MatchString(progress.SourceVMID) ||
		progress.OwnerUID == 0 ||
		progress.LaunchGeneration == 0 {
		return progress, instanceValidationError("snapshot progress identity differs")
	}
	return progress, nil
}

func snapshotCaptureOutcomeError(progress snapshotCaptureProgress, cause error) error {
	publication := "unpublished"
	if progress.Stage == "publishing" {
		publication = "unknown"
	}
	if progress.Published {
		publication = "published"
	}
	return errs.WrapMsg(
		errs.CodeSnapshotCreateFailed,
		"snapshot "+progress.SnapshotID+" capture did not complete; source VM state is "+progress.VMState+"; no automatic resume was attempted",
		cause,
		errs.WithDetails(map[string]any{
			"publication_state":  publication,
			"snapshot_id":        progress.SnapshotID,
			"source_vm_id":       progress.SourceVMID,
			"snapshot_stage":     progress.Stage,
			"vm_state":           progress.VMState,
			"snapshot_published": progress.Published,
		}),
	)
}
