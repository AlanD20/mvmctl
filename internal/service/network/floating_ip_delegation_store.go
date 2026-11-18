package network

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

const floatingIPDelegationDirectory = "floating-ip-delegations"

// floatingIPDelegationStore owns a pinned root-only directory. Mutation callers retain the network index,
// selected network mutation and host-global firewall locks, in that order, until publication completes.
type floatingIPDelegationStore struct {
	owner     *networkStoreUID
	ambiguous bool
}

func openFloatingIPDelegationStore(
	ctx context.Context, owner *networkStoreUID, create bool,
) (*floatingIPDelegationStore, error) {
	if owner == nil || owner.stateFD < 0 {
		return nil, networkAuthorityCorruptError("floating-IP delegation owner authority is released", nil)
	}
	fd, err := openNetworkStoreDirectory(
		ctx, owner.deps, owner.stateFD, floatingIPDelegationDirectory, create, owner.policy,
	)
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
		floatingIPDelegationDirectory,
		owner.policy,
		networkStoreDirectoryMode,
		true,
	); err != nil {
		return nil, closeRejectedNetworkStoreFD(ctx, owner.deps, fd, err)
	}
	return &floatingIPDelegationStore{owner: &networkStoreUID{
		deps: owner.deps, policy: owner.policy, uid: owner.uid, stateFD: fd,
	}}, nil
}

func (store *floatingIPDelegationStore) release(ctx context.Context) error {
	if store == nil {
		return nil
	}
	return store.owner.Release(ctx)
}

func (store *floatingIPDelegationStore) read(
	ctx context.Context, id string,
) (_ floatingIPDelegationRecord, _ []byte, _ unix.Stat_t, found bool, returnErr error) {
	if err := ctx.Err(); err != nil {
		return floatingIPDelegationRecord{}, nil, unix.Stat_t{}, false, networkAuthorityEffectError(
			"read floating-IP delegation record",
			err,
		)
	}
	if store == nil || store.owner == nil || store.owner.stateFD < 0 || store.ambiguous ||
		!vmIDPattern.MatchString(id) {
		return floatingIPDelegationRecord{}, nil, unix.Stat_t{}, false, networkAuthorityCorruptError(
			"floating-IP delegation store or identity is invalid",
			nil,
		)
	}
	owner := store.owner
	fd, err := owner.deps.openAt2(ctx, owner.stateFD, id+".json", &unix.OpenHow{
		Flags: networkStoreReadFlags, Resolve: networkStoreResolve,
	})
	if errors.Is(err, unix.ENOENT) {
		if err := ctx.Err(); err != nil {
			return floatingIPDelegationRecord{}, nil, unix.Stat_t{}, false, networkAuthorityEffectError(
				"read floating-IP delegation record",
				err,
			)
		}
		return floatingIPDelegationRecord{}, nil, unix.Stat_t{}, false, nil
	}
	if err != nil {
		return floatingIPDelegationRecord{}, nil, unix.Stat_t{}, false, networkAuthorityEffectError(
			"open floating-IP delegation record",
			err,
		)
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(
			returnErr,
			"close floating-IP delegation record",
			owner.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	before, err := inspectNetworkAuthorityRecordFile(ctx, owner.deps, fd, owner.policy)
	if err != nil {
		return floatingIPDelegationRecord{}, nil, unix.Stat_t{}, false, err
	}
	raw, err := readAllNetworkAuthorityRecord(ctx, owner.deps, fd)
	if err != nil {
		return floatingIPDelegationRecord{}, nil, unix.Stat_t{}, false, err
	}
	after, err := inspectNetworkAuthorityRecordFile(ctx, owner.deps, fd, owner.policy)
	if err != nil || !sameNetworkAuthorityRecordStat(before, after) || after.Size != int64(len(raw)) {
		return floatingIPDelegationRecord{}, nil, unix.Stat_t{}, false, networkAuthorityCorruptError(
			"floating-IP delegation record changed while reading",
			err,
		)
	}
	record, err := decodeFloatingIPDelegationRecord(raw)
	if err != nil || record.OwnerUID != owner.uid || record.ID != id {
		return floatingIPDelegationRecord{}, nil, unix.Stat_t{}, false, networkAuthorityCorruptError(
			"floating-IP delegation record owner or identity differs",
			err,
		)
	}
	return record, raw, after, true, nil
}

func (store *floatingIPDelegationStore) list(ctx context.Context) ([]floatingIPDelegationRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, networkAuthorityEffectError("enumerate floating-IP delegation records", err)
	}
	if store == nil {
		return nil, nil
	}
	if store.owner == nil || store.owner.stateFD < 0 || store.ambiguous {
		return nil, networkAuthorityCorruptError("delegation inventory authority is unavailable", nil)
	}
	names, err := store.owner.deps.readDirNames(ctx, store.owner.stateFD)
	if err != nil {
		return nil, networkAuthorityEffectError("enumerate floating-IP delegation records", err)
	}
	slices.Sort(names)
	items := make([]floatingIPDelegationRecord, 0, len(names))
	active := make(map[floatingIPDelegationTuple]struct{})
	for _, name := range names {
		id := strings.TrimSuffix(name, ".json")
		if name != id+".json" || !vmIDPattern.MatchString(id) {
			return nil, networkAuthorityCorruptError("delegation inventory contains pending or unknown state", nil)
		}
		record, _, _, found, err := store.read(ctx, id)
		if err != nil || !found {
			return nil, networkAuthorityCorruptError("delegation inventory record is unavailable", err)
		}
		if record.State != floatingIPDelegationRevoked {
			key := record.tuple()
			if _, duplicate := active[key]; duplicate {
				return nil, networkAuthorityCorruptError("duplicate live delegation tuple", nil)
			}
			active[key] = struct{}{}
		}
		items = append(items, record)
	}
	return items, nil
}

func (store *floatingIPDelegationStore) persist(
	ctx context.Context, record floatingIPDelegationRecord, previous *floatingIPDelegationRecord,
) error {
	if store == nil || store.owner == nil || store.owner.stateFD < 0 || store.ambiguous ||
		record.OwnerUID != store.owner.uid {
		return networkAuthorityCorruptError("floating-IP delegation store cannot admit another mutation", nil)
	}
	raw, err := encodeFloatingIPDelegationRecord(record)
	if err != nil {
		return err
	}
	before, beforeRaw, evidence, found, err := store.read(ctx, record.ID)
	if err != nil {
		return err
	}
	if (previous == nil && found) || (previous != nil && (!found || before != *previous)) {
		return networkAuthorityCorruptError("floating-IP delegation previous record changed", nil)
	}
	if err := validateFloatingIPDelegationReplacement(record, previous); err != nil {
		return err
	}
	items, err := store.list(ctx)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ID != record.ID && item.State != floatingIPDelegationRevoked &&
			record.State != floatingIPDelegationRevoked && item.tuple() == record.tuple() {
			return networkAuthorityCorruptError("delegation tuple already has live authority", nil)
		}
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
		return abort(networkAuthorityCorruptError("floating-IP delegation replacement evidence changed", err))
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
		return abort(networkAuthorityEffectError("publish floating-IP delegation record", err))
	}
	cleanup := context.WithoutCancel(ctx)
	syncErr := owner.deps.fsync(cleanup, owner.stateFD)
	_, published, _, publishedFound, observeErr := store.read(cleanup, record.ID)
	if observeErr != nil || !publishedFound || !bytes.Equal(published, raw) {
		store.ambiguous = true
		return networkAuthorityCorruptError(
			"floating-IP delegation publication outcome is unknown",
			errors.Join(syncErr, observeErr),
		)
	}
	if syncErr != nil {
		store.ambiguous = true
		failure := networkAuthorityEffectError("sync floating-IP delegation directory", syncErr)
		failure.Details = map[string]any{"record_replaced": true, "durability_uncertain": true}
		return failure
	}
	return nil
}
