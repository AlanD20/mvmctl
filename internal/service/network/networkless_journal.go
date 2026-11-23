package network

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"golang.org/x/sys/unix"
)

const networklessJournalSchema = uint32(2)

type networklessLifecycle string

const (
	networklessPreparing networklessLifecycle = "preparing"
	networklessActive    networklessLifecycle = "active"
	networklessCleaning  networklessLifecycle = "cleaning"
	networklessCleaned   networklessLifecycle = "cleaned"
)

// This record carries no managed network identity or topology. The target identity is retained even after cleanup
// so a reused namespace path cannot be mistaken for this VM's old mount.
type networklessJournalRecord struct {
	Schema           uint32                  `json:"schema"`
	Lifecycle        networklessLifecycle    `json:"lifecycle"`
	OwnerUID         uint32                  `json:"owner_uid"`
	VMID             string                  `json:"vm_id"`
	BootID           string                  `json:"boot_id"`
	LaunchGeneration uint64                  `json:"launch_generation"`
	Token            string                  `json:"token"`
	Pending          string                  `json:"pending"`
	LoopbackUp       bool                    `json:"loopback_up"`
	Namespace        NamespaceHandle         `json:"namespace"`
	NamespaceMountID uint64                  `json:"namespace_mount_id"`
	Target           topologyNamespaceTarget `json:"target"`
}

type networklessJournal struct {
	store     *networkStoreUID
	vmID      VMID
	record    networklessJournalRecord
	raw       []byte
	evidence  unix.Stat_t
	ambiguous bool
}

func openNetworklessJournal(
	ctx context.Context,
	owner *networkStoreUID,
	vmID VMID,
	create bool,
) (*networklessJournal, error) {
	if owner == nil || owner.stateFD < 0 || !vmIDPattern.MatchString(string(vmID)) {
		return nil, networkAuthorityValidationError("networkless journal owner or VM identity is invalid")
	}
	fd, err := openNetworkStoreDirectory(ctx, owner.deps, owner.stateFD, "networkless", create, owner.policy)
	if err != nil {
		return nil, err
	}
	store := &networkStoreUID{deps: owner.deps, policy: owner.policy, uid: owner.uid, stateFD: fd}
	return &networklessJournal{store: store, vmID: vmID}, nil
}

func (journal *networklessJournal) read(ctx context.Context) (networklessJournalRecord, bool, error) {
	if journal == nil || journal.store == nil || journal.ambiguous {
		return networklessJournalRecord{}, false, networkAuthorityCorruptError(
			"networkless journal is unavailable",
			nil,
		)
	}
	raw, evidence, found, err := journal.readRaw(ctx)
	if err != nil || !found {
		return networklessJournalRecord{}, found, err
	}
	record, err := decodeNetworklessJournal(raw)
	if err != nil {
		return networklessJournalRecord{}, false, err
	}
	if record.OwnerUID != journal.store.uid || record.VMID != string(journal.vmID) {
		return networklessJournalRecord{}, false, networkAuthorityCorruptError(
			"networkless journal owner binding changed",
			nil,
		)
	}
	journal.record, journal.raw, journal.evidence = record, raw, evidence
	return record, true, nil
}

func (journal *networklessJournal) readRaw(ctx context.Context) (_ []byte, _ unix.Stat_t, found bool, returnErr error) {
	store := journal.store
	fd, err := store.deps.openAt2(ctx, store.stateFD, string(journal.vmID)+".json", &unix.OpenHow{
		Flags: networkStoreReadFlags, Resolve: networkStoreResolve,
	})
	if errors.Is(err, unix.ENOENT) {
		return nil, unix.Stat_t{}, false, nil
	}
	if err != nil {
		return nil, unix.Stat_t{}, false, networkAuthorityEffectError("open networkless journal", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, store.deps.close(context.WithoutCancel(ctx), fd))
	}()
	before, err := inspectNetworkAuthorityRecordFile(ctx, store.deps, fd, store.policy)
	if err != nil {
		return nil, unix.Stat_t{}, false, err
	}
	raw, err := readAllNetworkAuthorityRecord(ctx, store.deps, fd)
	if err != nil {
		return nil, unix.Stat_t{}, false, err
	}
	after, err := inspectNetworkAuthorityRecordFile(ctx, store.deps, fd, store.policy)
	if err != nil || !sameNetworkAuthorityRecordStat(before, after) || after.Size != int64(len(raw)) {
		return nil, unix.Stat_t{}, false, networkAuthorityCorruptError("networkless journal changed while reading", err)
	}
	return raw, after, true, nil
}

func (journal *networklessJournal) persist(ctx context.Context, record networklessJournalRecord) error {
	if journal == nil || journal.store == nil || journal.ambiguous {
		return networkAuthorityCorruptError("networkless journal cannot admit mutation", nil)
	}
	if record.OwnerUID != journal.store.uid || record.VMID != string(journal.vmID) {
		return networkAuthorityCorruptError("networkless journal replacement changes owner", nil)
	}
	if err := validateNetworklessJournal(record); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > maxNetworkAuthorityRecordBytes {
		return networkAuthorityCorruptError("encode bounded networkless journal", err)
	}
	store := journal.store
	temp, fd, err := createNetworkAuthorityTemp(ctx, store)
	if err != nil {
		return err
	}
	open := true
	abort := func(cause error) error { return abortNetworkAuthorityWrite(ctx, store, fd, temp, open, cause) }
	if err := writeAllNetworkAuthorityRecord(ctx, store.deps, fd, raw); err != nil {
		return abort(err)
	}
	if err := store.deps.fchown(ctx, fd, int(store.policy.expectedUID), int(store.policy.expectedGID)); err != nil {
		return abort(err)
	}
	if err := store.deps.fchmod(ctx, fd, networkStoreRecordMode); err != nil {
		return abort(err)
	}
	if _, err := inspectNetworkAuthorityRecordFile(ctx, store.deps, fd, store.policy); err != nil {
		return abort(err)
	}
	if err := store.deps.fsync(ctx, fd); err != nil {
		return abort(err)
	}
	open = false
	if err := store.deps.close(context.WithoutCancel(ctx), fd); err != nil {
		return abort(err)
	}
	before, evidence, found, err := journal.readRaw(ctx)
	if err != nil {
		return abort(err)
	}
	if (journal.raw == nil && found) || (journal.raw != nil && (!found ||
		!bytes.Equal(before, journal.raw) || !sameNetworkAuthorityRecordStat(evidence, journal.evidence))) {
		return abort(networkAuthorityCorruptError("networkless journal replacement evidence changed", nil))
	}
	if err := ctx.Err(); err != nil {
		return abort(err)
	}
	name := string(journal.vmID) + ".json"
	if !found {
		err = store.deps.renameNoReplace(ctx, store.stateFD, temp, store.stateFD, name)
	} else {
		err = unix.Renameat(store.stateFD, temp, store.stateFD, name)
	}
	if err != nil {
		return abort(networkAuthorityEffectError("publish networkless journal", err))
	}
	cleanup := context.WithoutCancel(ctx)
	syncErr := store.deps.fsync(cleanup, store.stateFD)
	observed, observedEvidence, observedFound, observedErr := journal.readRaw(cleanup)
	if observedErr != nil || !observedFound || !bytes.Equal(observed, raw) {
		journal.ambiguous = true
		return networkAuthorityCorruptError(
			"networkless journal publication outcome is unknown",
			errors.Join(syncErr, observedErr),
		)
	}
	journal.raw, journal.evidence, journal.record = observed, observedEvidence, record
	if syncErr != nil {
		failure := networkAuthorityEffectError("sync networkless journal directory", syncErr)
		failure.Details = map[string]any{"record_replaced": true, "durability_uncertain": true}
		return failure
	}
	return nil
}

func decodeNetworklessJournal(raw []byte) (networklessJournalRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var record networklessJournalRecord
	if err := decoder.Decode(&record); err != nil {
		return networklessJournalRecord{}, networkAuthorityCorruptError("decode networkless journal", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return networklessJournalRecord{}, networkAuthorityCorruptError("networkless journal has trailing input", err)
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(canonical, raw) {
		return networklessJournalRecord{}, networkAuthorityCorruptError(
			"networkless journal encoding is not canonical",
			err,
		)
	}
	return record, validateNetworklessJournal(record)
}

func validateNetworklessJournal(record networklessJournalRecord) error {
	key, err := newNamespaceKey(record.OwnerUID, record.VMID)
	if err != nil || record.Schema != networklessJournalSchema || !bootIDPattern.MatchString(record.BootID) ||
		record.LaunchGeneration == 0 || len(record.Pending) > 64 {
		return networkAuthorityCorruptError("networkless journal identity is invalid", err)
	}
	token, err := hex.DecodeString(record.Token)
	if err != nil || len(token) != 32 || record.Token != hex.EncodeToString(token) {
		return networkAuthorityCorruptError("networkless journal token is invalid", err)
	}
	switch record.Lifecycle {
	case networklessPreparing:
		switch record.Pending {
		case "prepare_target", "link_target", "create_namespace", "publish_mount", "raise_loopback":
		default:
			return networkAuthorityCorruptError("networkless preparation stage is invalid", nil)
		}
	case networklessActive:
		if record.Pending != "" || !record.LoopbackUp {
			return networkAuthorityCorruptError("active networkless journal has incomplete loopback evidence", nil)
		}
	case networklessCleaning:
		if record.Pending != "delete_namespace" {
			return networkAuthorityCorruptError("networkless cleanup stage is invalid", nil)
		}
	case networklessCleaned:
		if record.Pending != "" {
			return networkAuthorityCorruptError("cleaned networkless journal retains mutation stage", nil)
		}
	default:
		return networkAuthorityCorruptError("networkless journal lifecycle is invalid", nil)
	}
	targetPresent := record.Target != (topologyNamespaceTarget{})
	namespacePresent := record.Namespace != (NamespaceHandle{})
	if namespacePresent != (record.NamespaceMountID != 0) || namespacePresent && !targetPresent ||
		record.LoopbackUp && !namespacePresent {
		return networkAuthorityCorruptError("networkless journal stage identities are incomplete", nil)
	}
	if record.Lifecycle == networklessPreparing {
		switch record.Pending {
		case "prepare_target":
			if targetPresent || namespacePresent {
				return networkAuthorityCorruptError("unpublished target has early identity", nil)
			}
		case "link_target", "create_namespace":
			if !targetPresent || namespacePresent {
				return networkAuthorityCorruptError("target stage identity is invalid", nil)
			}
		case "publish_mount", "raise_loopback":
			if !namespacePresent {
				return networkAuthorityCorruptError("mount stage lacks exact identity", nil)
			}
		}
	}
	if record.Lifecycle == networklessActive && !namespacePresent {
		return networkAuthorityCorruptError("active networkless journal lacks exact mount", nil)
	}
	if namespacePresent {
		handle := namespaceHandle{
			path: namespaceHandlePath(record.Namespace.Path),
			identity: namespaceIdentity{
				device: record.Namespace.Identity.Device,
				inode:  record.Namespace.Identity.Inode,
			},
			ownerUID: record.Namespace.OwnerUID,
			ownerGID: record.Namespace.OwnerGID,
			mode:     record.Namespace.Mode,
		}
		if err := validateNamespaceHandle(handle, deriveNamespaceHandle(key)); err != nil {
			return err
		}
	}
	if !targetPresent {
		return nil
	}
	target := namespaceTargetFromJournal(record.Target)
	if target.directoryDevice == 0 || target.directoryInode == 0 || target.placeholderDevice == 0 ||
		target.placeholderInode == 0 || target.directoryMount.kind != namespaceTargetMountIdentityUnique ||
		target.placeholderMount.kind != namespaceTargetMountIdentityUnique ||
		!validNamespaceTargetMountIdentity(
			target.directoryMount,
		) || !validNamespaceTargetMountIdentity(target.placeholderMount) {
		return networkAuthorityCorruptError("networkless journal target identity is invalid", nil)
	}
	return nil
}
