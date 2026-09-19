package jailer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

const snapshotRecordLeaf = "capture.json"

type snapshotArtifact struct {
	Name   string `json:"name"`
	Bytes  uint64 `json:"bytes"`
	SHA256 string `json:"sha256"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type snapshotCaptureRecord struct {
	Schema     uint32                  `json:"schema"`
	SnapshotID string                  `json:"snapshot_id"`
	OwnerUID   uint32                  `json:"owner_uid"`
	Mode       string                  `json:"mode"`
	Source     vmLaunchDeviceAuthority `json:"source"`
	Artifacts  []snapshotArtifact      `json:"artifacts"`
}

type snapshotStoreEntry struct {
	store       *protectedSnapshotStoreLease
	lockFD      int
	directoryFD int
	snapshotID  string
}

// CRITICAL: The independent root-owned lock survives entry removal. A replacement directory
// can never create a second concurrent lock for the same snapshot identity.
func lockSnapshotStoreEntry(
	ctx context.Context,
	store *protectedSnapshotStoreLease,
	snapshotID string,
	create bool,
) (_ *snapshotStoreEntry, returnErr error) {
	if !isCanonicalManagedResourceID(snapshotID, 64) || store == nil || store.fd < 0 {
		return nil, instanceValidationError("snapshot store selection is incomplete")
	}
	entry := &snapshotStoreEntry{store: store, snapshotID: snapshotID, lockFD: -1, directoryFD: -1}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, entry.Release(context.WithoutCancel(ctx)))
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.Openat2(store.fd, snapshotID+".lock", &unix.OpenHow{
		Flags: unix.O_RDWR | unix.O_CREAT | unix.O_CLOEXEC | unix.O_NOFOLLOW, Mode: 0600,
		Resolve: managedVMResourceResolveFlags,
	})
	if err != nil {
		return nil, err
	}
	entry.lockFD = fd
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if stat.Mode != unix.S_IFREG|0600 || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 || stat.Size != 0 {
		return nil, instanceValidationError("snapshot lock metadata is unsafe")
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return nil, managedVMResourceAtomicError("snapshot already has an active operation", err)
	}
	if create {
		if err := unix.Mkdirat(store.fd, snapshotID, 0700); err != nil {
			return nil, err
		}
		if err := unix.Fsync(store.fd); err != nil {
			return nil, err
		}
	}
	entry.directoryFD, err = unix.Openat2(store.fd, snapshotID, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: managedVMResourceResolveFlags,
	})
	if err != nil {
		return nil, err
	}
	if err := unix.Fstat(entry.directoryFD, &stat); err != nil {
		return nil, err
	}
	if stat.Mode != unix.S_IFDIR|0700 || stat.Uid != 0 || stat.Gid != 0 {
		return nil, instanceValidationError("snapshot directory is not root protected")
	}
	if create {
		if err := unix.Mkdirat(entry.directoryFD, "refs", 0700); err != nil {
			return nil, err
		}
		if err := unix.Fsync(entry.directoryFD); err != nil {
			return nil, err
		}
	}
	return entry, nil
}

func (entry *snapshotStoreEntry) Release(ctx context.Context) error {
	var result error
	for _, fd := range []*int{&entry.directoryFD, &entry.lockFD} {
		if *fd >= 0 {
			result = errors.Join(result, unix.Close(*fd))
			*fd = -1
		}
	}
	return result
}

func (entry *snapshotStoreEntry) publish(ctx context.Context, record snapshotCaptureRecord) error {
	if record.SnapshotID != entry.snapshotID {
		return instanceValidationError("snapshot publication identity differs")
	}
	if err := record.validate(); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if uint64(len(data)) > 2*managedVMConfigMaxBytes {
		return instanceValidationError("snapshot record exceeds protocol bound")
	}
	fd, err := unix.MemfdCreate("mvm-snapshot-record", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return err
	}
	writeErr := writeSnapshotBytes(ctx, fd, data)
	if writeErr == nil {
		writeErr = unix.Fchmod(fd, 0400)
	}
	if writeErr == nil {
		_, writeErr = unix.FcntlInt(uintptr(fd), unix.F_ADD_SEALS, managedVMConfigRequiredSeals)
	}
	if writeErr != nil {
		return errors.Join(writeErr, unix.Close(fd))
	}
	_, copyErr := entry.copyArtifact(ctx, snapshotRecordLeaf, fd, uint64(len(data)))
	if err := errors.Join(copyErr, unix.Close(fd)); err != nil {
		return err
	}
	return unix.Fsync(entry.directoryFD)
}

func (entry *snapshotStoreEntry) readRecord(ctx context.Context, ownerUID uint32) (snapshotCaptureRecord, error) {
	fd, err := unix.Openat2(entry.directoryFD, snapshotRecordLeaf, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: managedVMResourceResolveFlags,
	})
	if err != nil {
		return snapshotCaptureRecord{}, err
	}
	var stat unix.Stat_t
	err = unix.Fstat(fd, &stat)
	if err == nil && (stat.Mode != unix.S_IFREG|0400 || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 ||
		stat.Size < 1 || uint64(stat.Size) > 2*managedVMConfigMaxBytes) {
		err = instanceValidationError("snapshot record is not an immutable root record")
	}
	var data []byte
	if err == nil {
		data, err = readLaunchDeviceBytes(ctx, fd, int(stat.Size))
	}
	if err := errors.Join(err, unix.Close(fd)); err != nil {
		return snapshotCaptureRecord{}, err
	}
	var record snapshotCaptureRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return snapshotCaptureRecord{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return snapshotCaptureRecord{}, instanceValidationError("snapshot record has trailing data")
	}
	if err := record.validate(); err != nil {
		return snapshotCaptureRecord{}, err
	}
	if record.SnapshotID != entry.snapshotID || record.OwnerUID != ownerUID {
		return snapshotCaptureRecord{}, instanceValidationError("snapshot record owner or identity differs")
	}
	return record, nil
}

func (record snapshotCaptureRecord) validate() error {
	if record.Schema != 1 || !isCanonicalManagedResourceID(record.SnapshotID, 64) || record.OwnerUID == 0 ||
		record.Source.OwnerUID != record.OwnerUID || (record.Mode != "full" && record.Mode != "disks") {
		return instanceValidationError("snapshot capture record identity is invalid")
	}
	if err := record.Source.validate(); err != nil {
		return err
	}
	if record.Source.Release.Version != "1.16.1" {
		return instanceValidationError("snapshot release is unsupported")
	}
	want := 2 + len(record.Source.Volumes)
	if record.Mode == "full" {
		want += 2
	}
	if record.Source.CloudInit {
		want++
	}
	if len(record.Artifacts) != want {
		return instanceValidationError("snapshot artifact inventory is incomplete")
	}
	seen := make(map[string]bool, len(record.Artifacts))
	for _, artifact := range record.Artifacts {
		if !validSnapshotArtifactName(artifact.Name) || seen[artifact.Name] || artifact.Bytes == 0 ||
			artifact.Bytes > math.MaxInt64 || artifact.Inode == 0 || !isCanonicalManagedResourceID(artifact.SHA256, 64) {
			return instanceValidationError("snapshot artifact identity is invalid")
		}
		seen[artifact.Name] = true
	}
	if !seen["rootfs.img"] || !seen["kernel"] || (record.Mode == "full") != seen["memory"] ||
		(record.Mode == "full") != seen["vmstate"] || record.Source.CloudInit != seen["cloud-init.iso"] {
		return instanceValidationError("snapshot fixed artifact inventory differs")
	}
	memoryBytes, err := managedSnapshotMemoryBytes(record.Source.Config.MachineConfig.MemSizeMiB)
	if err != nil {
		return err
	}
	expected := map[string]uint64{"rootfs.img": record.Source.Rootfs.bytes, "kernel": record.Source.KernelBacking.bytes}
	if record.Source.CloudInit {
		if record.Source.CloudInitBacking == nil {
			return instanceValidationError("snapshot cloud-init authority is incomplete")
		}
		expected["cloud-init.iso"] = record.Source.CloudInitBacking.bytes
	}
	for _, volume := range record.Source.Volumes {
		expected["volume-"+volume.GuestDriveID+".raw"] = volume.Selection.ExpectedBytes
	}
	if record.Mode == "full" {
		expected["memory"] = memoryBytes
	}
	for _, artifact := range record.Artifacts {
		if artifact.Name == "vmstate" {
			if artifact.Bytes > managedSnapshotVMStateMaxBytes {
				return instanceValidationError("snapshot vmstate exceeds complete loader ceiling")
			}
		} else if expected[artifact.Name] != artifact.Bytes {
			return instanceValidationError("snapshot artifact extent differs from captured machine/device authority")
		}
	}
	return nil
}

func validSnapshotArtifactName(name string) bool {
	switch name {
	case "rootfs.img", "memory", "vmstate", "cloud-init.iso", "kernel":
		return true
	}
	return strings.HasPrefix(name, "volume-") && strings.HasSuffix(name, ".raw") &&
		isCanonicalManagedResourceID(strings.TrimSuffix(strings.TrimPrefix(name, "volume-"), ".raw"), 64)
}

func (entry *snapshotStoreEntry) copyArtifact(
	ctx context.Context,
	name string,
	sourceFD int,
	length uint64,
) (_ snapshotArtifact, returnErr error) {
	if (!validSnapshotArtifactName(name) && name != snapshotRecordLeaf) || length == 0 || length > math.MaxInt64 {
		return snapshotArtifact{}, instanceValidationError("snapshot copy extent or leaf is invalid")
	}
	fd, err := unix.Openat2(entry.directoryFD, name, &unix.OpenHow{
		Flags: unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW, Mode: 0600,
		Resolve: managedVMResourceResolveFlags,
	})
	if err != nil {
		return snapshotArtifact{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	buffer := make([]byte, 1<<20)
	hash := sha256.New()
	for offset := uint64(0); offset < length; {
		if err := ctx.Err(); err != nil {
			return snapshotArtifact{}, err
		}
		part := buffer[:min(uint64(len(buffer)), length-offset)]
		n, err := unix.Pread(sourceFD, part, int64(offset))
		if err != nil {
			return snapshotArtifact{}, err
		}
		if n <= 0 || n > len(part) {
			return snapshotArtifact{}, io.ErrUnexpectedEOF
		}
		if _, err := hash.Write(part[:n]); err != nil {
			return snapshotArtifact{}, err
		}
		if err := writeSnapshotBytes(ctx, fd, part[:n]); err != nil {
			return snapshotArtifact{}, err
		}
		offset += uint64(n)
	}
	var probe [1]byte
	n, err := unix.Pread(sourceFD, probe[:], int64(length))
	if err != nil {
		return snapshotArtifact{}, err
	}
	if n != 0 {
		return snapshotArtifact{}, instanceValidationError("snapshot source extent changed while copying")
	}
	if err := unix.Fchmod(fd, snapshotArtifactMode(name)); err != nil {
		return snapshotArtifact{}, err
	}
	if err := unix.Fsync(fd); err != nil {
		return snapshotArtifact{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return snapshotArtifact{}, err
	}
	if stat.Uid != 0 || stat.Gid != 0 || stat.Mode != unix.S_IFREG|snapshotArtifactMode(name) || stat.Nlink != 1 ||
		uint64(stat.Size) != length {
		return snapshotArtifact{}, instanceValidationError("snapshot copy metadata is not immutable")
	}
	return snapshotArtifact{
		Name:   name,
		Bytes:  length,
		SHA256: hex.EncodeToString(hash.Sum(nil)),
		Device: uint64(stat.Dev),
		Inode:  stat.Ino,
	}, nil
}

func writeSnapshotBytes(ctx context.Context, fd int, data []byte) error {
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := unix.Write(fd, data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrNoProgress
		}
		data = data[n:]
	}
	return nil
}

func (entry *snapshotStoreEntry) remove(ctx context.Context) (returnErr error) {
	refs, err := unix.Openat2(entry.directoryFD, "refs", &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: managedVMResourceResolveFlags,
	})
	if errors.Is(err, unix.ENOENT) {
		// Reconcile only the empty-parent suffix after refs removal. The kernel
		// refuses AT_REMOVEDIR if an unexpected artifact or reference remains.
		if err := unix.Unlinkat(entry.store.fd, entry.snapshotID, unix.AT_REMOVEDIR); err != nil {
			return err
		}
		return unix.Fsync(entry.store.fd)
	}
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(refs), "snapshot-references")
	names, readErr := file.Readdirnames(1)
	closeErr := file.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if len(names) != 0 {
		return instanceValidationError("snapshot is retained by a restored VM")
	}
	fd, err := unix.Openat2(entry.directoryFD, ".", &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW,
		Resolve: managedVMResourceResolveFlags,
	})
	if err != nil {
		return err
	}
	file = os.NewFile(uintptr(fd), "snapshot-artifacts")
	names, readErr = file.Readdirnames(managedMMIOMaxInterruptSlots + 10)
	closeErr = file.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if len(names) >= managedMMIOMaxInterruptSlots+10 {
		return instanceValidationError("snapshot directory has unexpected entries")
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "refs" {
			continue
		}
		if !validSnapshotArtifactName(name) && name != snapshotRecordLeaf && name != snapshotProgressLeaf &&
			name != snapshotProgressTemporaryLeaf {
			return instanceValidationError("snapshot directory has an unexpected leaf")
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(entry.directoryFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 ||
			(stat.Mode != unix.S_IFREG|snapshotArtifactMode(name) && stat.Mode != unix.S_IFREG|0600) {
			return instanceValidationError("snapshot removal leaf is not root protected")
		}
		if err := unix.Unlinkat(entry.directoryFD, name, 0); err != nil {
			return err
		}
	}
	if err := unix.Unlinkat(entry.directoryFD, "refs", unix.AT_REMOVEDIR); err != nil {
		return err
	}
	if err := unix.Unlinkat(entry.store.fd, entry.snapshotID, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return unix.Fsync(entry.store.fd)
}
