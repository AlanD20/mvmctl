package network

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

const floatingIPDirectory = "floating-ips"

// floatingIPStore owns a pinned root-only directory. Mutation callers retain the network index,
// selected network mutation and host-global firewall locks, in that order, until publication completes.
type floatingIPStore struct {
	owner     *networkStoreUID
	ambiguous bool
}

func openFloatingIPStore(ctx context.Context, owner *networkStoreUID, create bool) (*floatingIPStore, error) {
	if owner == nil || owner.stateFD < 0 {
		return nil, networkAuthorityCorruptError("floating-IP owner authority is released", nil)
	}
	fd, err := openNetworkStoreDirectory(ctx, owner.deps, owner.stateFD, floatingIPDirectory, create, owner.policy)
	if !create && errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := verifyNetworkStoreDirectory(
		ctx,
		owner.deps,
		fd,
		floatingIPDirectory,
		owner.policy,
		networkStoreDirectoryMode,
		true,
	); err != nil {
		return nil, closeRejectedNetworkStoreFD(ctx, owner.deps, fd, err)
	}
	return &floatingIPStore{owner: &networkStoreUID{
		deps: owner.deps, policy: owner.policy, uid: owner.uid, stateFD: fd,
	}}, nil
}

func (store *floatingIPStore) release(ctx context.Context) error {
	if store == nil {
		return nil
	}
	return store.owner.Release(ctx)
}

func (store *floatingIPStore) read(
	ctx context.Context, id string,
) (_ floatingIPRecord, _ []byte, _ unix.Stat_t, found bool, returnErr error) {
	if err := ctx.Err(); err != nil {
		return floatingIPRecord{}, nil, unix.Stat_t{}, false, networkAuthorityEffectError(
			"read floating-IP record",
			err,
		)
	}
	if store == nil || store.owner == nil || store.owner.stateFD < 0 || store.ambiguous ||
		!vmIDPattern.MatchString(id) {
		return floatingIPRecord{}, nil, unix.Stat_t{}, false, networkAuthorityCorruptError(
			"floating-IP store or identity is invalid",
			nil,
		)
	}
	owner := store.owner
	fd, err := owner.deps.openAt2(ctx, owner.stateFD, id+".json", &unix.OpenHow{
		Flags: networkStoreReadFlags, Resolve: networkStoreResolve,
	})
	if errors.Is(err, unix.ENOENT) {
		if err := ctx.Err(); err != nil {
			return floatingIPRecord{}, nil, unix.Stat_t{}, false, networkAuthorityEffectError(
				"read floating-IP record",
				err,
			)
		}
		return floatingIPRecord{}, nil, unix.Stat_t{}, false, nil
	}
	if err != nil {
		return floatingIPRecord{}, nil, unix.Stat_t{}, false, networkAuthorityEffectError(
			"open floating-IP record",
			err,
		)
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(
			returnErr,
			"close floating-IP record",
			owner.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	before, err := inspectNetworkAuthorityRecordFile(ctx, owner.deps, fd, owner.policy)
	if err != nil {
		return floatingIPRecord{}, nil, unix.Stat_t{}, false, err
	}
	raw, err := readAllNetworkAuthorityRecord(ctx, owner.deps, fd)
	if err != nil {
		return floatingIPRecord{}, nil, unix.Stat_t{}, false, err
	}
	after, err := inspectNetworkAuthorityRecordFile(ctx, owner.deps, fd, owner.policy)
	if err != nil || !sameNetworkAuthorityRecordStat(before, after) || after.Size != int64(len(raw)) {
		return floatingIPRecord{}, nil, unix.Stat_t{}, false, networkAuthorityCorruptError(
			"floating-IP record changed while reading",
			err,
		)
	}
	record, err := decodeFloatingIPRecord(raw)
	if err != nil || record.OwnerUID != owner.uid || record.ID != id {
		return floatingIPRecord{}, nil, unix.Stat_t{}, false, networkAuthorityCorruptError(
			"floating-IP record owner or identity differs",
			err,
		)
	}
	return record, raw, after, true, nil
}

func (store *floatingIPStore) list(ctx context.Context) ([]floatingIPRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, networkAuthorityEffectError("enumerate floating-IP records", err)
	}
	if store == nil {
		return nil, nil
	}
	names, err := store.owner.deps.readDirNames(ctx, store.owner.stateFD)
	if err != nil {
		return nil, networkAuthorityEffectError("enumerate floating-IP records", err)
	}
	slices.Sort(names)
	items := make([]floatingIPRecord, 0, len(names))
	namesSeen := make(map[string]struct{})
	addressesSeen := make(map[string]struct{})
	for _, name := range names {
		id := strings.TrimSuffix(name, ".json")
		if name != id+".json" || !vmIDPattern.MatchString(id) {
			return nil, networkAuthorityCorruptError("floating-IP inventory contains pending or unknown state", nil)
		}
		record, _, _, found, err := store.read(ctx, id)
		if err != nil || !found {
			return nil, networkAuthorityCorruptError("floating-IP inventory record is unavailable", err)
		}
		if !record.Retired {
			nameKey := record.NetworkID + ":" + record.Name
			if _, duplicate := namesSeen[nameKey]; duplicate {
				return nil, networkAuthorityCorruptError("floating-IP name is reserved more than once", nil)
			}
			addressKey := record.NetworkID + ":" + record.IPv4
			if _, duplicate := addressesSeen[addressKey]; duplicate {
				return nil, networkAuthorityCorruptError("floating-IP address is reserved more than once", nil)
			}
			namesSeen[nameKey] = struct{}{}
			addressesSeen[addressKey] = struct{}{}
		}
		items = append(items, record)
	}
	return items, nil
}

func (store *floatingIPStore) persist(ctx context.Context, record floatingIPRecord, previous *floatingIPRecord) error {
	if store == nil || store.owner == nil || store.owner.stateFD < 0 || store.ambiguous ||
		record.OwnerUID != store.owner.uid {
		return networkAuthorityCorruptError("floating-IP store cannot admit another mutation", nil)
	}
	raw, err := encodeFloatingIPRecord(record)
	if err != nil {
		return err
	}
	before, beforeRaw, evidence, found, err := store.read(ctx, record.ID)
	if err != nil {
		return err
	}
	if (previous == nil && found) || (previous != nil && (!found || before != *previous)) {
		return networkAuthorityCorruptError("floating-IP previous record changed", nil)
	}
	if previous == nil {
		if record.Generation != 1 || record.Retired {
			return networkAuthorityCorruptError("new floating-IP reservation must start at generation one", nil)
		}
	} else if previous.Retired || previous.Generation == ^uint64(0) || record.Generation != previous.Generation+1 ||
		record.NetworkID != previous.NetworkID || record.Name != previous.Name || record.IPv4 != previous.IPv4 {
		return networkAuthorityCorruptError(
			"floating-IP replacement changes immutable authority or has a stale generation",
			nil,
		)
	}
	owner := store.owner
	name, fd, err := createNetworkAuthorityTemp(ctx, owner)
	if err != nil {
		return err
	}
	open := true
	abort := func(cause error) error { return abortNetworkAuthorityWrite(ctx, owner, fd, name, open, cause) }
	if err := writeAllNetworkAuthorityRecord(ctx, owner.deps, fd, raw); err != nil {
		return abort(err)
	}
	if err := owner.deps.fchown(ctx, fd, int(owner.policy.expectedUID), int(owner.policy.expectedGID)); err != nil {
		return abort(err)
	}
	if err := owner.deps.fchmod(ctx, fd, networkStoreRecordMode); err != nil {
		return abort(err)
	}
	if _, err := inspectNetworkAuthorityRecordFile(ctx, owner.deps, fd, owner.policy); err != nil {
		return abort(err)
	}
	if err := owner.deps.fsync(ctx, fd); err != nil {
		return abort(err)
	}
	open = false
	if err := owner.deps.close(context.WithoutCancel(ctx), fd); err != nil {
		return abort(err)
	}
	_, observedRaw, observedStat, observedFound, err := store.read(ctx, record.ID)
	if err != nil || found != observedFound || (found && (!bytes.Equal(beforeRaw, observedRaw) ||
		!sameNetworkAuthorityRecordStat(evidence, observedStat))) {
		return abort(networkAuthorityCorruptError("floating-IP replacement evidence changed", err))
	}
	if err := ctx.Err(); err != nil {
		return abort(err)
	}
	if !found {
		err = owner.deps.renameNoReplace(ctx, owner.stateFD, name, owner.stateFD, record.ID+".json")
	} else {
		err = unix.Renameat(owner.stateFD, name, owner.stateFD, record.ID+".json")
	}
	if err != nil {
		return abort(networkAuthorityEffectError("publish floating-IP record", err))
	}
	cleanup := context.WithoutCancel(ctx)
	syncErr := owner.deps.fsync(cleanup, owner.stateFD)
	_, published, _, publishedFound, observeErr := store.read(cleanup, record.ID)
	if observeErr != nil || !publishedFound || !bytes.Equal(published, raw) {
		store.ambiguous = true
		return networkAuthorityCorruptError(
			"floating-IP publication outcome is unknown",
			errors.Join(syncErr, observeErr),
		)
	}
	if syncErr != nil {
		store.ambiguous = true
		failure := networkAuthorityEffectError("sync floating-IP directory", syncErr)
		failure.Details = map[string]any{"record_replaced": true, "durability_uncertain": true}
		return failure
	}
	return nil
}
