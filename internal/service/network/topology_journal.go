package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"

	"golang.org/x/sys/unix"
)

const topologyJournalSchema = uint32(1)

type topologyLifecycle string

const (
	topologyPreparing  topologyLifecycle = "preparing_down"
	topologyActivating topologyLifecycle = "activating"
	topologyActive     topologyLifecycle = "active"
	topologyCleaning   topologyLifecycle = "cleaning"
	topologyCleaned    topologyLifecycle = "cleaned"
)

type topologyProgress struct {
	Namespace   bool `json:"namespace"`
	LocalBridge bool `json:"local_bridge"`
	TAP         bool `json:"tap"`
	Veth        bool `json:"veth"`
	Attached    bool `json:"attached"`
	L3          bool `json:"l3"`
	LoopbackUp  bool `json:"loopback_up"`
	BridgeUp    bool `json:"bridge_up"`
	TAPUp       bool `json:"tap_up"`
	PeerUp      bool `json:"peer_up"`
	HostUp      bool `json:"host_up"`
}

type topologyJournalRecord struct {
	Schema           uint32                  `json:"schema"`
	Lifecycle        topologyLifecycle       `json:"lifecycle"`
	OwnerUID         uint32                  `json:"owner_uid"`
	NetworkID        string                  `json:"network_id"`
	VMID             string                  `json:"vm_id"`
	BootID           string                  `json:"boot_id"`
	LaunchGeneration uint64                  `json:"launch_generation"`
	NetworkDigest    string                  `json:"network_digest"`
	Prefix           string                  `json:"prefix"`
	Gateway          string                  `json:"gateway"`
	GuestIPv4        string                  `json:"guest_ipv4"`
	GuestMAC         string                  `json:"guest_mac"`
	EndpointID       string                  `json:"endpoint_id"`
	Pending          string                  `json:"pending"`
	Topology         TopologyRecord          `json:"topology"`
	Progress         topologyProgress        `json:"progress"`
	NamespaceTarget  topologyNamespaceTarget `json:"namespace_target"`
}

type topologyNamespaceTarget struct {
	DirectoryDevice      uint64 `json:"directory_device"`
	DirectoryInode       uint64 `json:"directory_inode"`
	DirectoryMountKind   uint8  `json:"directory_mount_kind"`
	DirectoryMount       uint64 `json:"directory_mount"`
	PlaceholderDevice    uint64 `json:"placeholder_device"`
	PlaceholderInode     uint64 `json:"placeholder_inode"`
	PlaceholderMountKind uint8  `json:"placeholder_mount_kind"`
	PlaceholderMount     uint64 `json:"placeholder_mount"`
}

// topologyJournal retains the last exact file evidence, including a visible replacement whose fsync failed.
// Its owner serializes all calls while the VM, network, and global endpoint mutation locks are retained.
type topologyJournal struct {
	store     *networkStoreUID
	vmID      VMID
	record    topologyJournalRecord
	evidence  unix.Stat_t
	raw       []byte
	ambiguous bool
}

func openTopologyJournal(ctx context.Context, owner *networkStoreUID, vmID VMID) (*topologyJournal, error) {
	if owner == nil || owner.stateFD < 0 || !vmIDPattern.MatchString(string(vmID)) {
		return nil, networkAuthorityValidationError("topology journal owner or VM identity is invalid")
	}
	fd, err := openNetworkStoreDirectory(ctx, owner.deps, owner.stateFD, "topologies", true, owner.policy)
	if err != nil {
		return nil, err
	}
	store := &networkStoreUID{deps: owner.deps, policy: owner.policy, uid: owner.uid, stateFD: fd}
	return &topologyJournal{store: store, vmID: vmID}, nil
}

func (journal *topologyJournal) read(ctx context.Context) (topologyJournalRecord, bool, error) {
	if journal == nil || journal.store == nil || journal.store.stateFD < 0 || journal.ambiguous {
		return topologyJournalRecord{}, false, networkAuthorityCorruptError("topology journal is unavailable", nil)
	}
	raw, evidence, found, err := readTopologyJournal(ctx, journal.store, journal.vmID)
	if err != nil || !found {
		return topologyJournalRecord{}, found, err
	}
	record, err := decodeTopologyJournal(raw)
	if err != nil {
		return topologyJournalRecord{}, false, err
	}
	if record.OwnerUID != journal.store.uid || record.VMID != string(journal.vmID) {
		return topologyJournalRecord{}, false, networkAuthorityCorruptError("topology journal owner binding changed", nil)
	}
	journal.raw, journal.evidence, journal.record = raw, evidence, record
	return record, true, nil
}

func readTopologyJournal(
	ctx context.Context,
	store *networkStoreUID,
	vmID VMID,
) (_ []byte, _ unix.Stat_t, found bool, returnErr error) {
	fd, err := store.deps.openAt2(ctx, store.stateFD, string(vmID)+".json", &unix.OpenHow{
		Flags: networkStoreReadFlags, Resolve: networkStoreResolve,
	})
	if errors.Is(err, unix.ENOENT) {
		return nil, unix.Stat_t{}, false, nil
	}
	if err != nil {
		return nil, unix.Stat_t{}, false, networkAuthorityEffectError("open topology journal", err)
	}
	defer func() {
		returnErr = appendNetworkAuthorityError(returnErr, "close topology journal", store.deps.close(context.WithoutCancel(ctx), fd))
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
		return nil, unix.Stat_t{}, false, networkAuthorityCorruptError("topology journal changed while reading", err)
	}
	return raw, after, true, nil
}

func (journal *topologyJournal) persist(ctx context.Context, record topologyJournalRecord) error {
	if journal == nil || journal.store == nil || journal.ambiguous {
		return networkAuthorityCorruptError("topology journal cannot admit another mutation", nil)
	}
	if record.OwnerUID != journal.store.uid || record.VMID != string(journal.vmID) {
		return networkAuthorityCorruptError("topology journal replacement changes its owner binding", nil)
	}
	if err := validateTopologyJournal(record); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil || len(raw) > maxNetworkAuthorityRecordBytes {
		return networkAuthorityCorruptError("encode bounded topology journal", err)
	}
	store := journal.store
	tempName, fd, err := createNetworkAuthorityTemp(ctx, store)
	if err != nil {
		return err
	}
	open := true
	abort := func(err error) error {
		return abortNetworkAuthorityWrite(ctx, store, fd, tempName, open, err)
	}
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
	before, evidence, found, err := readTopologyJournal(ctx, store, journal.vmID)
	if err != nil {
		return abort(err)
	}
	if (journal.raw == nil && found) || (journal.raw != nil && (!found ||
		!bytes.Equal(before, journal.raw) || !sameNetworkAuthorityRecordStat(evidence, journal.evidence))) {
		return abort(networkAuthorityCorruptError("topology journal replacement evidence changed", nil))
	}
	if err := ctx.Err(); err != nil {
		return abort(err)
	}
	name := string(journal.vmID) + ".json"
	if !found {
		err = store.deps.renameNoReplace(ctx, store.stateFD, tempName, store.stateFD, name)
	} else {
		// CRITICAL: Only the exact previously admitted file can be replaced under the retained mutation locks.
		err = unix.Renameat(store.stateFD, tempName, store.stateFD, name)
	}
	if err != nil {
		return abort(networkAuthorityEffectError("publish topology journal", err))
	}
	cleanup := context.WithoutCancel(ctx)
	syncErr := store.deps.fsync(cleanup, store.stateFD)
	observed, observedEvidence, observedFound, observeErr := readTopologyJournal(cleanup, store, journal.vmID)
	if observeErr != nil || !observedFound || !bytes.Equal(observed, raw) {
		journal.ambiguous = true
		return networkAuthorityCorruptError("topology journal publication outcome is unknown", errors.Join(syncErr, observeErr))
	}
	journal.raw, journal.evidence, journal.record = observed, observedEvidence, record
	if syncErr != nil {
		failure := networkAuthorityEffectError("sync topology journal directory", syncErr)
		failure.Details = map[string]any{"record_replaced": true, "durability_uncertain": true}
		return failure
	}
	return nil
}

func decodeTopologyJournal(raw []byte) (topologyJournalRecord, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var record topologyJournalRecord
	if err := decoder.Decode(&record); err != nil {
		return topologyJournalRecord{}, networkAuthorityCorruptError("decode topology journal", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return topologyJournalRecord{}, networkAuthorityCorruptError("topology journal has trailing input", err)
	}
	canonical, err := json.Marshal(record)
	// The private writer emits one canonical representation. Exact comparison also rejects duplicate, missing,
	// case-colliding, null-for-scalar, and reordered fields without accepting json.Unmarshal's last-value semantics.
	if err != nil || !bytes.Equal(canonical, raw) {
		return topologyJournalRecord{}, networkAuthorityCorruptError("topology journal encoding is not canonical", err)
	}
	return record, validateTopologyJournal(record)
}

func validateTopologyJournal(record topologyJournalRecord) error {
	key, err := newTopologyKey(record.OwnerUID, record.VMID, record.NetworkID)
	if err != nil || record.Schema != topologyJournalSchema || !bootIDPattern.MatchString(record.BootID) ||
		record.LaunchGeneration == 0 || !networkIDPattern.MatchString(record.NetworkDigest) ||
		record.EndpointID != string(deriveVMLinkName("mvh", key.ownerUID, key.vmID, "host-veth")) {
		return networkAuthorityCorruptError("topology journal identity is invalid", err)
	}
	switch record.Lifecycle {
	case topologyPreparing, topologyActivating, topologyActive, topologyCleaning, topologyCleaned:
	default:
		return networkAuthorityCorruptError("topology journal lifecycle is invalid", nil)
	}
	prefix, prefixErr := netip.ParsePrefix(record.Prefix)
	gateway, gatewayErr := netip.ParseAddr(record.Gateway)
	guest, guestErr := netip.ParseAddr(record.GuestIPv4)
	if err := errors.Join(prefixErr, gatewayErr, guestErr); err != nil {
		return networkAuthorityCorruptError("topology journal L3 identity is invalid", err)
	}
	network, err := admitManagedNetworkL3(managedNetworkL3Input{prefix: prefix, gateway: &gateway})
	if err != nil {
		return err
	}
	if _, err := deriveEndpointL3(key, network, endpointL3Input{guestAddress: guest, requestedMAC: &record.GuestMAC}); err != nil {
		return err
	}
	if len(record.Pending) > 64 {
		return fmt.Errorf("topology journal mutation marker exceeds limit")
	}
	if record.Lifecycle == topologyCleaned && record.Progress != (topologyProgress{}) {
		return networkAuthorityCorruptError("cleaned topology journal retains live progress", nil)
	}
	return nil
}
