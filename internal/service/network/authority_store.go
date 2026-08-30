package network

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"golang.org/x/sys/unix"
)

var networkStoreTempNamePattern = regexp.MustCompile(`^\.mvm-network-[a-f0-9]{32}\.tmp$`)

func (uid *networkStoreUID) readRecord(
	ctx context.Context,
	networkID NetworkID,
) (record networkAuthorityRecord, found bool, returnErr error) {
	if uid == nil || uid.stateFD < 0 {
		return networkAuthorityRecord{}, false, networkAuthorityCorruptError(
			"network authority owner directory is released",
			nil,
		)
	}
	if !networkIDPattern.MatchString(string(networkID)) {
		return networkAuthorityRecord{}, false, networkAuthorityValidationError("network ID is invalid")
	}
	if err := ctx.Err(); err != nil {
		return networkAuthorityRecord{}, false, networkAuthorityEffectError("open network authority record", err)
	}
	name := string(networkID) + ".json"
	fd, err := uid.deps.openAt2(ctx, uid.stateFD, name, &unix.OpenHow{
		Flags:   networkStoreReadFlags,
		Resolve: networkStoreResolve,
	})
	if errors.Is(err, unix.ENOENT) {
		if contextErr := ctx.Err(); contextErr != nil {
			return networkAuthorityRecord{}, false, networkAuthorityEffectError(
				"open network authority record",
				contextErr,
			)
		}
		return networkAuthorityRecord{}, false, nil
	}
	if err != nil {
		return networkAuthorityRecord{}, false, networkAuthorityEffectError("open network authority record", err)
	}
	defer func() {
		if err := uid.deps.close(context.WithoutCancel(ctx), fd); err != nil {
			returnErr = appendNetworkAuthorityError(returnErr, "close network authority record", err)
		}
	}()
	before, err := inspectNetworkAuthorityRecordFile(ctx, uid.deps, fd, uid.policy)
	if err != nil {
		return networkAuthorityRecord{}, false, err
	}
	raw, err := readAllNetworkAuthorityRecord(ctx, uid.deps, fd)
	if err != nil {
		return networkAuthorityRecord{}, false, networkAuthorityEffectError("read network authority record", err)
	}
	after, err := inspectNetworkAuthorityRecordFile(ctx, uid.deps, fd, uid.policy)
	if err != nil {
		return networkAuthorityRecord{}, false, err
	}
	if !sameNetworkAuthorityRecordStat(before, after) || after.Size != int64(len(raw)) {
		return networkAuthorityRecord{}, false, networkAuthorityCorruptError(
			"network authority record changed while being read",
			nil,
		)
	}
	record, err = decodeNetworkAuthorityRecord(raw)
	if err != nil {
		return networkAuthorityRecord{}, false, err
	}
	if record.ownerUID != uid.uid || record.networkID != networkID {
		return networkAuthorityRecord{}, false, networkAuthorityCorruptError(
			"network authority record identity is inconsistent",
			nil,
		)
	}
	return record, true, nil
}

func inspectNetworkAuthorityRecordFile(
	ctx context.Context,
	deps networkStoreDeps,
	fd int,
	policy networkStorePolicy,
) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return unix.Stat_t{}, networkAuthorityEffectError("inspect network authority record", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return unix.Stat_t{}, networkAuthorityCorruptError(
			"network authority record is not a one-link regular file",
			nil,
		)
	}
	if stat.Uid != policy.expectedUID || stat.Gid != policy.expectedGID ||
		stat.Mode&07777 != networkStoreRecordMode {
		return unix.Stat_t{}, networkAuthorityCorruptError(
			"network authority record has unexpected owner or mode",
			nil,
		)
	}
	if stat.Size <= 0 || stat.Size > maxNetworkAuthorityRecordBytes {
		return unix.Stat_t{}, networkAuthorityCorruptError("network authority record size is invalid", nil)
	}
	return stat, nil
}

func sameNetworkAuthorityRecordStat(first, second unix.Stat_t) bool {
	return first.Dev == second.Dev &&
		first.Ino == second.Ino &&
		first.Mode == second.Mode &&
		first.Nlink == second.Nlink &&
		first.Uid == second.Uid &&
		first.Gid == second.Gid &&
		first.Size == second.Size &&
		first.Mtim == second.Mtim &&
		first.Ctim == second.Ctim
}

func (uid *networkStoreUID) writeRecord(ctx context.Context, record networkAuthorityRecord) error {
	if uid == nil || uid.stateFD < 0 {
		return networkAuthorityCorruptError("network authority owner directory is released", nil)
	}
	if record.ownerUID != uid.uid {
		return networkAuthorityCorruptError("network authority record owner does not match directory", nil)
	}
	raw, err := encodeNetworkAuthorityRecord(record)
	if err != nil {
		return err
	}
	tempName, tempFD, err := createNetworkAuthorityTemp(ctx, uid)
	if err != nil {
		return err
	}
	tempOpen := true
	abort := func(cause error) error {
		return abortNetworkAuthorityWrite(ctx, uid, tempFD, tempName, tempOpen, cause)
	}
	if err := writeAllNetworkAuthorityRecord(ctx, uid.deps, tempFD, raw); err != nil {
		return abort(networkAuthorityEffectError("write network authority temporary record", err))
	}
	if err := uid.deps.fchown(ctx, tempFD, int(uid.policy.expectedUID), int(uid.policy.expectedGID)); err != nil {
		return abort(networkAuthorityEffectError("set network authority record owner", err))
	}
	if err := uid.deps.fchmod(ctx, tempFD, networkStoreRecordMode); err != nil {
		return abort(networkAuthorityEffectError("set network authority record mode", err))
	}
	beforeSync, err := inspectNetworkAuthorityRecordFile(ctx, uid.deps, tempFD, uid.policy)
	if err != nil {
		return abort(err)
	}
	if beforeSync.Size != int64(len(raw)) {
		return abort(networkAuthorityCorruptError("network authority temporary record is incomplete", nil))
	}
	if err := uid.deps.fsync(ctx, tempFD); err != nil {
		return abort(networkAuthorityEffectError("sync network authority temporary record", err))
	}
	afterSync, err := inspectNetworkAuthorityRecordFile(ctx, uid.deps, tempFD, uid.policy)
	if err != nil {
		return abort(err)
	}
	if !sameNetworkAuthorityRecordStat(beforeSync, afterSync) {
		return abort(networkAuthorityCorruptError("network authority temporary record changed while syncing", nil))
	}
	tempOpen = false
	if err := uid.deps.close(context.WithoutCancel(ctx), tempFD); err != nil {
		return abort(networkAuthorityEffectError("close network authority temporary record", err))
	}
	if err := ctx.Err(); err != nil {
		return abort(networkAuthorityEffectError("publish network authority record", err))
	}
	name := string(record.networkID) + ".json"
	if err := uid.deps.renameNoReplace(ctx, uid.stateFD, tempName, uid.stateFD, name); err != nil {
		return abort(networkAuthorityEffectError("atomically create network authority record", err))
	}
	if err := uid.deps.fsync(ctx, uid.stateFD); err != nil {
		result := networkAuthorityEffectError("sync network authority record directory", err)
		result.Details = map[string]any{"record_created": true, "durability_uncertain": true}
		return result
	}
	return nil
}

func createNetworkAuthorityTemp(ctx context.Context, uid *networkStoreUID) (string, int, error) {
	for range networkStoreTempAttempts {
		if err := ctx.Err(); err != nil {
			return "", -1, networkAuthorityEffectError("create network authority temporary record", err)
		}
		name, err := uid.deps.randomName(ctx)
		if err != nil {
			return "", -1, networkAuthorityEffectError("generate network authority temporary name", err)
		}
		if !networkStoreTempNamePattern.MatchString(name) {
			return "", -1, networkAuthorityCorruptError("generated network authority temporary name is invalid", nil)
		}
		fd, err := uid.deps.openAt2(ctx, uid.stateFD, name, &unix.OpenHow{
			Flags:   networkStoreTempFlags,
			Mode:    uint64(networkStoreRecordMode),
			Resolve: networkStoreResolve,
		})
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", -1, networkAuthorityEffectError("create network authority temporary record", err)
		}
		return name, fd, nil
	}
	return "", -1, networkAuthorityCorruptError("exclusive network authority temporary names exhausted", nil)
}

func writeAllNetworkAuthorityRecord(ctx context.Context, deps networkStoreDeps, fd int, value []byte) error {
	for written := 0; written < len(value); {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := deps.write(ctx, fd, value[written:])
		if count < 0 || count > len(value)-written {
			return fmt.Errorf("invalid write count %d", count)
		}
		written += count
		if err != nil {
			return err
		}
		if count == 0 {
			return errors.New("zero-length network authority write")
		}
	}
	return nil
}

func abortNetworkAuthorityWrite(
	ctx context.Context,
	uid *networkStoreUID,
	tempFD int,
	tempName string,
	tempOpen bool,
	primary error,
) error {
	cleanupCtx := context.WithoutCancel(ctx)
	result := primary
	if tempOpen {
		if err := uid.deps.close(cleanupCtx, tempFD); err != nil {
			result = appendNetworkAuthorityError(result, "close incomplete network authority record", err)
		}
	}
	if err := uid.deps.unlinkAt(cleanupCtx, uid.stateFD, tempName, 0); err != nil {
		result = appendNetworkAuthorityError(result, "remove incomplete network authority record", err)
	}
	return result
}

func (root *networkStoreRoot) findClaim(
	ctx context.Context,
	networkID NetworkID,
) (*networkAuthorityRecord, error) {
	if root == nil || root.stateFD < 0 {
		return nil, networkAuthorityCorruptError("network authority root is released", nil)
	}
	if !networkIDPattern.MatchString(string(networkID)) {
		return nil, networkAuthorityValidationError("network ID is invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, networkAuthorityEffectError("enumerate network authority owners", err)
	}
	names, err := root.deps.readDirNames(ctx, root.stateFD)
	if err != nil {
		return nil, networkAuthorityEffectError("enumerate network authority owners", err)
	}
	var found *networkAuthorityRecord
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, networkAuthorityEffectError("enumerate network authority owners", err)
		}
		uidValue, parseErr := strconv.ParseUint(name, 10, 32)
		if parseErr != nil || uidValue == 0 || strconv.FormatUint(uidValue, 10) != name {
			return nil, networkAuthorityCorruptError("network authority owner index is inconsistent", nil)
		}
		uid, openErr := root.openUID(ctx, uint32(uidValue), false)
		if openErr != nil {
			return nil, openErr
		}
		record, recordFound, readErr := uid.readRecord(ctx, networkID)
		closeErr := uid.Release(context.WithoutCancel(ctx))
		if readErr != nil {
			return nil, appendNetworkAuthorityError(readErr, "close indexed network authority owner", closeErr)
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if !recordFound {
			continue
		}
		if found != nil {
			return nil, networkAuthorityCorruptError("duplicate network authority identity", nil)
		}
		copy := record
		found = &copy
	}
	return found, nil
}
