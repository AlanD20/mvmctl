package jailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// Worker records are root authority, independent from resource ownership and PID display mirrors.
// All combined acquisitions retain VM authority BEFORE this record's lock. No worker waits for VM
// admission while holding this lock; bootstrap must start only after the parent's Release completes.
type guestControlWorkerRecord struct {
	Schema           uint32                           `json:"schema"`
	ResourceOwnerUID uint32                           `json:"resource_owner_uid"`
	ResourceOwnerGID uint32                           `json:"resource_owner_gid"`
	VMID             string                           `json:"vm_id"`
	BootID           string                           `json:"boot_id"`
	LaunchGeneration uint64                           `json:"launch_generation"`
	ExecutionUID     uint32                           `json:"execution_uid"`
	Generation       uint64                           `json:"generation"`
	Revision         uint64                           `json:"revision"`
	Phase            string                           `json:"phase"`
	Process          *guestControlWorkerProcessRecord `json:"process"`
}

func (record guestControlWorkerRecord) principal() GuestControlPrincipal {
	return GuestControlPrincipal{OwnerUID: record.ResourceOwnerUID, OwnerGID: record.ResourceOwnerGID,
		VMID: record.VMID, BootID: record.BootID,
		LaunchGeneration: record.LaunchGeneration, ExecutionUID: record.ExecutionUID}
}

func validateGuestControlWorkerRecord(record guestControlWorkerRecord) error {
	p := record.principal()
	if record.Schema != 1 || p.OwnerUID == 0 || p.OwnerGID == 0 || p.ExecutionUID == 0 ||
		p.ExecutionUID == p.OwnerUID || !vmIDPattern.MatchString(p.VMID) || !hostBootIDPattern.MatchString(p.BootID) ||
		p.LaunchGeneration == 0 || record.Generation == 0 || record.Revision < record.Generation {
		return instanceValidationError("Guest Control worker record identity is invalid")
	}
	switch record.Phase {
	case "prepared":
		if record.Process != nil {
			return instanceValidationError("prepared worker carries process authority")
		}
	case "spawned", "ready":
		if record.Process == nil {
			return instanceValidationError("spawned worker has no process proof")
		}
	case "stopping", "stopped":
	default:
		return instanceValidationError("Guest Control worker record phase is invalid")
	}
	if record.Process != nil {
		return validateGuestControlWorkerProcessRecord(*record.Process)
	}
	return nil
}

func encodeGuestControlWorkerRecord(record guestControlWorkerRecord) ([]byte, error) {
	if err := validateGuestControlWorkerRecord(record); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > maxInstanceRecordBytes {
		return nil, instanceAtomicError("encode bounded worker record", err)
	}
	return raw, nil
}

func decodeGuestControlWorkerRecord(raw []byte) (guestControlWorkerRecord, error) {
	var record guestControlWorkerRecord
	if len(raw) == 0 || len(raw) > maxInstanceRecordBytes {
		return record, instanceValidationError("worker record size is invalid")
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return record, instanceAtomicError("decode worker record", err)
	}
	canonical, err := encodeGuestControlWorkerRecord(record)
	if err != nil || !bytes.Equal(canonical, raw) {
		return guestControlWorkerRecord{}, instanceAtomicError("worker record is not canonical", err)
	}
	return record, nil
}

type guestControlWorkerStore struct {
	directories *instanceUIDDirectories
	retained    []int
	lock        int
	vmID        string
	raw         []byte
	record      guestControlWorkerRecord
	found       bool
	uncertain   bool
}

// Fixed hierarchy only; deps/policy reuse existing instance filesystem admission for L1 fixtures.
func openGuestControlWorkerStore(
	ctx context.Context, deps instanceAuthorityDeps, policy instanceAuthorityPolicy, owner uint32, vmID string,
) (_ *guestControlWorkerStore, returnErr error) {
	if ctx == nil || owner == 0 || !vmIDPattern.MatchString(vmID) {
		return nil, instanceValidationError("worker store binding is invalid")
	}
	store := &guestControlWorkerStore{lock: -1, vmID: vmID}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, store.close(context.WithoutCancel(ctx)))
		}
	}()
	root, err := deps.open(ctx, policy.rootPath, instanceDirectoryFlags, 0)
	if err != nil {
		return nil, err
	}
	store.retained = append(store.retained, root)
	if err := verifyInstanceDirectory(ctx, deps, root, "worker filesystem root",
		policy, instanceAncestorDirectory); err != nil {
		return nil, err
	}
	parent := root
	for i, name := range []string{"var", "lib", "mvmctl", "guest-control", strconv.FormatUint(uint64(owner), 10)} {
		class := instancePrivateDirectory
		if i < 2 {
			class = instanceAncestorDirectory
		}
		if i == 2 {
			class = instanceStateBaseDirectory
		}
		fd, err := openInstanceDirectory(ctx, deps, parent, name, i >= 2, class, policy)
		if err != nil {
			return nil, err
		}
		store.retained = append(store.retained, fd)
		parent = fd
	}
	store.directories = &instanceUIDDirectories{deps: deps, policy: policy, uid: owner, stateFD: parent}
	store.lock, err = deps.openAt(ctx, parent, vmID+".lock",
		unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err := verifyGuestControlWorkerFile(ctx, deps, store.lock, "worker record lock", policy); err != nil {
		return nil, err
	}
	for {
		err := unix.Flock(store.lock, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return nil, err
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	store.record, store.raw, store.found, err = store.read(ctx)
	if err != nil {
		return nil, err
	}
	return store, nil
}

func (store *guestControlWorkerStore) read(
	ctx context.Context,
) (_ guestControlWorkerRecord, _ []byte, found bool, returnErr error) {
	d := store.directories
	fd, err := d.deps.openAt(ctx, d.stateFD, store.vmID+".json", instanceReadFlags, 0)
	if errors.Is(err, unix.ENOENT) {
		return guestControlWorkerRecord{}, nil, false, nil
	}
	if err != nil {
		return guestControlWorkerRecord{}, nil, false, err
	}
	defer func() { returnErr = errors.Join(returnErr, d.deps.close(context.WithoutCancel(ctx), fd)) }()
	if err := verifyGuestControlWorkerFile(ctx, d.deps, fd, "worker record", d.policy); err != nil {
		return guestControlWorkerRecord{}, nil, false, err
	}
	var before, after unix.Stat_t
	if err := d.deps.fstat(ctx, fd, &before); err != nil {
		return guestControlWorkerRecord{}, nil, false, err
	}
	raw, err := readAllInstanceRecord(ctx, d.deps, fd)
	if err != nil {
		return guestControlWorkerRecord{}, nil, false, err
	}
	if err := d.deps.fstat(ctx, fd, &after); err != nil || !guestControlWorkerRecordStatEqual(before, after) {
		return guestControlWorkerRecord{}, nil, false, instanceAtomicError("worker record changed during read", err)
	}
	record, err := decodeGuestControlWorkerRecord(raw)
	if err != nil {
		return guestControlWorkerRecord{}, nil, false, err
	}
	if record.ResourceOwnerUID != d.uid || record.VMID != store.vmID {
		return guestControlWorkerRecord{}, nil, false, instanceValidationError("worker record scope differs")
	}
	return record, raw, true, nil
}

func (store *guestControlWorkerStore) persist(ctx context.Context, target guestControlWorkerRecord) (returnErr error) {
	if store.uncertain || target.ResourceOwnerUID != store.directories.uid || target.VMID != store.vmID {
		return instanceValidationError("worker publication authority is unavailable")
	}
	if err := validateGuestControlWorkerTransition(store.record, store.found, target); err != nil {
		return err
	}
	_, current, found, err := store.read(ctx)
	if err != nil || found != store.found || !bytes.Equal(current, store.raw) {
		return instanceAtomicError("worker record changed under retained lock", err)
	}
	raw, err := encodeGuestControlWorkerRecord(target)
	if err != nil {
		return err
	}
	d := store.directories
	name, fd, err := createInstanceRecordTemp(ctx, d)
	if err != nil {
		return err
	}
	open := true
	defer func() {
		if open {
			returnErr = errors.Join(returnErr, d.deps.close(context.WithoutCancel(ctx), fd))
		}
		if returnErr != nil {
			if err := d.deps.unlinkAt(context.WithoutCancel(ctx), d.stateFD, name, 0); !errors.Is(err, unix.ENOENT) {
				returnErr = errors.Join(returnErr, err)
			}
		}
	}()
	if err := writeAllInstanceRecord(ctx, d.deps, fd, raw); err != nil {
		return err
	}
	if err := d.deps.fchown(ctx, fd, int(d.policy.expectedUID), int(d.policy.expectedGID)); err != nil {
		return err
	}
	if err := d.deps.fchmod(ctx, fd, 0600); err != nil {
		return err
	}
	if err := verifyGuestControlWorkerFile(ctx, d.deps, fd, "worker temporary record", d.policy); err != nil {
		return err
	}
	if err := d.deps.fsync(ctx, fd); err != nil {
		return err
	}
	closeErr := d.deps.close(context.WithoutCancel(ctx), fd)
	open = false
	if closeErr != nil {
		return closeErr
	}
	_, latest, latestFound, err := store.read(ctx)
	if err != nil || latestFound != store.found || !bytes.Equal(latest, store.raw) {
		return instanceAtomicError("worker record changed before publication", err)
	}
	if err := d.inspectRecordTarget(ctx, store.vmID); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Publication or sync may have occurred even when the call reports an error. Never retry this lease blindly.
	store.uncertain = true
	if err := d.deps.renameAt(ctx, d.stateFD, name, d.stateFD, store.vmID+".json"); err != nil {
		return err
	}
	if err := d.deps.fsync(ctx, d.stateFD); err != nil {
		return err
	}
	observed, readback, found, err := store.read(ctx)
	if err != nil || !found || !bytes.Equal(raw, readback) {
		return instanceAtomicError("worker publication readback differs", err)
	}
	store.record, store.raw, store.found, store.uncertain = observed, readback, true, false
	return nil
}

func validateGuestControlWorkerTransition(
	before guestControlWorkerRecord, found bool, target guestControlWorkerRecord,
) error {
	if err := validateGuestControlWorkerRecord(target); err != nil {
		return err
	}
	if !found {
		if target.Phase != "prepared" || target.Generation != 1 || target.Revision != 1 {
			return instanceValidationError("initial worker must be prepared")
		}
		return nil
	}
	if before.Revision == ^uint64(0) || target.Revision != before.Revision+1 ||
		before.ResourceOwnerUID != target.ResourceOwnerUID || before.VMID != target.VMID {
		return instanceValidationError("worker revision or identity differs")
	}
	if before.Phase == "stopped" && target.Phase == "prepared" {
		if before.Generation == ^uint64(0) || target.Generation != before.Generation+1 {
			return instanceValidationError("worker generation must advance")
		}
		return nil
	}
	if before.principal() != target.principal() || target.Generation != before.Generation {
		return instanceValidationError("worker incarnation changed without retirement")
	}
	allowed := before.Phase == "spawned" && target.Phase == "ready" ||
		before.Phase == "prepared" && target.Phase == "spawned" ||
		(before.Phase == "prepared" || before.Phase == "spawned" || before.Phase == "ready") &&
			target.Phase == "stopping" ||
		before.Phase == "stopping" && target.Phase == "stopped"
	if !allowed {
		return instanceValidationError("worker phase transition is invalid")
	}
	if before.Phase != "prepared" || target.Phase != "spawned" {
		if !guestControlWorkerProcessesEqual(before.Process, target.Process) {
			return instanceValidationError("worker process changed during retirement")
		}
	}
	return nil
}

func (store *guestControlWorkerStore) close(ctx context.Context) error {
	var result error
	if store.lock >= 0 {
		result = errors.Join(result, unix.Close(store.lock))
		store.lock = -1
	}
	if store.directories != nil {
		result = errors.Join(result, closeInstanceFDs(ctx, store.directories.deps, store.retained))
	} else {
		for _, fd := range store.retained {
			result = errors.Join(result, unix.Close(fd))
		}
	}
	store.retained = nil
	return result
}

func guestControlWorkerRecordStatEqual(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Uid == b.Uid && a.Gid == b.Gid &&
		a.Nlink == b.Nlink && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}

func verifyGuestControlWorkerFile(
	ctx context.Context, deps instanceAuthorityDeps, fd int, description string, policy instanceAuthorityPolicy,
) error {
	if err := verifyInstanceRegularFile(ctx, deps, fd, description, policy); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return err
	}
	if stat.Nlink != 1 || stat.Size < 0 || stat.Size > maxInstanceRecordBytes {
		return instanceValidationError("worker authority file link count or size is invalid")
	}
	return nil
}
