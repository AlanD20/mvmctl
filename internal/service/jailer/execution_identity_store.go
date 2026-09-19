package jailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const executionIdentityRecordLimit = 2048

type executionIdentityStore struct {
	directory int
	lock      int
	retained  []int
}

func openExecutionIdentityStore(ctx context.Context, create bool) (_ *executionIdentityStore, returnErr error) {
	store := &executionIdentityStore{directory: -1, lock: -1}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, store.close(context.WithoutCancel(ctx)))
		}
	}()
	deps, policy := realInstanceAuthorityDeps(), productionInstanceAuthorityPolicy()
	root, err := unix.Open("/", instanceDirectoryFlags, 0)
	if err != nil {
		return nil, instanceAtomicError("open execution identity authority root", err)
	}
	store.retained = append(store.retained, root)
	if err := verifyInstanceDirectory(ctx, deps, root, "root", policy, instanceAncestorDirectory); err != nil {
		return nil, err
	}
	parent := root
	for index, name := range []string{"var", "lib", "mvmctl", "execution-identities"} {
		class := instanceAncestorDirectory
		if index == 2 {
			class = instanceStateBaseDirectory
		} else if index == 3 {
			class = instancePrivateDirectory
		}
		fd, err := openInstanceDirectory(ctx, deps, parent, name, create && index >= 2, class, policy)
		if err != nil {
			return nil, err
		}
		store.retained = append(store.retained, fd)
		parent = fd
	}
	store.directory = parent
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	lock, err := -1, error(nil)
	created := false
	if create {
		lock, err = unix.Openat(parent, "allocator.lock", flags|unix.O_CREAT|unix.O_EXCL, 0600)
		created = err == nil
	}
	if !create || errors.Is(err, unix.EEXIST) {
		lock, err = unix.Openat(parent, "allocator.lock", flags, 0)
	}
	if err != nil {
		return nil, instanceAtomicError("open execution identity allocator lock", err)
	}
	store.retained = append(store.retained, lock)
	if created {
		if err := errors.Join(
			unix.Fchown(lock, 0, 0),
			unix.Fchmod(lock, 0600),
			unix.Fsync(lock),
			unix.Fsync(parent),
		); err != nil {
			return nil, instanceAtomicError("persist execution identity allocator lock", err)
		}
	}
	if err := inspectExecutionIdentityRecordFD(ctx, lock); err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		err := unix.Flock(lock, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			store.lock = lock
			return store, nil
		}
		if !errors.Is(err, unix.EINTR) && !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EWOULDBLOCK) {
			return nil, instanceAtomicError("acquire execution identity allocator lock", err)
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (store *executionIdentityStore) close(ctx context.Context) error {
	var result error
	if store.lock >= 0 {
		result = unix.Flock(store.lock, unix.LOCK_UN)
		store.lock = -1
	}
	for index := len(store.retained) - 1; index >= 0; index-- {
		result = errors.Join(result, unix.Close(store.retained[index]))
	}
	store.retained = nil
	store.directory = -1
	return result
}

func inspectExecutionIdentityRecordFD(ctx context.Context, fd int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return instanceAtomicError("inspect execution identity reservation", err)
	}
	if stat.Mode != unix.S_IFREG|0600 || stat.Uid != 0 || stat.Gid != 0 || stat.Nlink != 1 ||
		stat.Size < 0 || stat.Size > executionIdentityRecordLimit {
		return instanceAtomicError("execution identity reservation metadata is unsafe", nil)
	}
	return nil
}

func (store *executionIdentityStore) read(ctx context.Context, vmID string) (
	record executionIdentityRecord, found bool, returnErr error,
) {
	fd, err := unix.Openat(store.directory, vmID+".json", instanceReadFlags|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return record, false, nil
	}
	if err != nil {
		return record, false, instanceAtomicError("open execution identity reservation", err)
	}
	file := os.NewFile(uintptr(fd), "execution-identity-reservation")
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	if err := inspectExecutionIdentityRecordFD(ctx, fd); err != nil {
		return record, false, err
	}
	raw, err := io.ReadAll(io.LimitReader(file, executionIdentityRecordLimit+1))
	if err != nil {
		return record, false, instanceAtomicError("read execution identity reservation", err)
	}
	record, err = decodeExecutionIdentityRecord(raw)
	if err != nil || record.VMID != vmID {
		return record, false, instanceAtomicError("execution identity reservation binding is invalid", err)
	}
	return record, true, nil
}

func decodeExecutionIdentityRecord(raw []byte) (executionIdentityRecord, error) {
	var record executionIdentityRecord
	if len(raw) == 0 || len(raw) > executionIdentityRecordLimit {
		return record, instanceAtomicError("execution identity reservation size is invalid", nil)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return record, instanceAtomicError("execution identity reservation is not an object", err)
	}
	seen := make(map[string]bool)
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return record, instanceAtomicError("execution identity reservation repeats a field", err)
		}
		seen[name] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return record, instanceAtomicError("execution identity reservation field is invalid", err)
		}
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, instanceAtomicError("decode execution identity reservation", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return record, instanceAtomicError("execution identity reservation has trailing data", err)
	}
	return record, validateExecutionIdentityRecord(record)
}

func (store *executionIdentityStore) write(
	ctx context.Context,
	record executionIdentityRecord,
	replace bool,
) (returnErr error) {
	if err := validateExecutionIdentityRecord(record); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return instanceAtomicError("encode execution identity reservation", err)
	}
	deps := realInstanceAuthorityDeps()
	directory := &instanceUIDDirectories{
		deps:    deps,
		policy:  productionInstanceAuthorityPolicy(),
		stateFD: store.directory,
	}
	name, fd, err := createInstanceRecordTemp(ctx, directory)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, unix.Close(fd))
		if name != "" {
			returnErr = errors.Join(returnErr, unix.Unlinkat(store.directory, name, 0))
		}
	}()
	if err := errors.Join(unix.Fchown(fd, 0, 0), unix.Fchmod(fd, 0600)); err != nil {
		return instanceAtomicError("set execution reservation metadata", err)
	}
	if err := writeAllInstanceRecord(ctx, deps, fd, raw); err != nil {
		return instanceAtomicError("write execution identity reservation", err)
	}
	if err := unix.Fsync(fd); err != nil {
		return instanceAtomicError("sync execution identity reservation", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	flags := uint(unix.RENAME_NOREPLACE)
	if replace {
		flags = 0
	}
	if err := unix.Renameat2(store.directory, name, store.directory, record.VMID+".json", flags); err != nil {
		return instanceAtomicError("publish execution identity reservation", err)
	}
	name = ""
	if err := unix.Fsync(store.directory); err != nil {
		return annotateInstanceRecordReplacement(instanceAtomicError("sync execution reservation directory", err), true)
	}
	return nil
}

func (store *executionIdentityStore) requireDistinct(ctx context.Context, selected executionIdentityRecord) error {
	names, err := readInstanceDirectoryNames(ctx, store.directory)
	if err != nil {
		return err
	}
	for _, name := range names {
		if name == "allocator.lock" {
			continue
		}
		if !strings.HasSuffix(name, ".json") || !vmIDPattern.MatchString(strings.TrimSuffix(name, ".json")) {
			return instanceAtomicError("execution identity store has unresolved or unexpected entries", nil)
		}
		record, found, err := store.read(ctx, strings.TrimSuffix(name, ".json"))
		if err != nil || !found {
			return instanceAtomicError("execution identity reservation disappeared during allocation", err)
		}
		if record.VMID == selected.VMID {
			continue
		}
		if record.Name == selected.Name || (record.Phase == executionIdentityReady &&
			(*record.UID == *selected.UID || *record.GID == *selected.GID)) {
			return instanceAtomicError("execution credentials are reserved by another VM", nil)
		}
	}
	return nil
}

func (store *executionIdentityStore) pin(ctx context.Context, record executionIdentityRecord) (int, error) {
	fd, err := unix.Openat(store.directory, record.VMID+".json", instanceReadFlags|unix.O_NONBLOCK, 0)
	if err != nil {
		return -1, instanceAtomicError("pin execution identity reservation", err)
	}
	if err := inspectExecutionIdentityRecordFD(ctx, fd); err != nil {
		return -1, errors.Join(err, unix.Close(fd))
	}
	return fd, nil
}
