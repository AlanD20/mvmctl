package jailer

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/guestcontrolauth"
)

const guestControlCredentialPrefix = "guest-control-"
const guestControlBindingLeaf = "guest-control-binding.json"

// guestControlBackingReceipt binds expected root-generated material to ordinary protected import.
// It proves neither guest file contents/permissions nor endpoint readiness or delegation. A pending
// cold preparation reuses the SAME immutable material; bound means the exact protected import retained it.
type guestControlBackingReceipt struct {
	SourceVMID string `json:"source_vm_id"`
	Version    string `json:"version"`
	Phase      string `json:"phase"`
}

type guestControlCredentialRecord struct {
	Schema        uint32 `json:"schema"`
	OwnerUID      uint32 `json:"owner_uid"`
	SourceVMID    string `json:"source_vm_id"`
	RootMaterial  []byte `json:"root_material"`
	AgentMaterial []byte `json:"agent_material"`
}

func (record guestControlCredentialRecord) version() (string, error) {
	if record.Schema != 1 || record.OwnerUID == 0 || !vmIDPattern.MatchString(record.SourceVMID) {
		return "", instanceValidationError("Guest Control credential binding is invalid")
	}
	if _, err := guestcontrolauth.DecodeRootHostMaterial(record.RootMaterial); err != nil {
		return "", instanceValidationError("Guest Control root credential is invalid")
	}
	if _, err := guestcontrolauth.DecodeAgentMaterial(record.AgentMaterial); err != nil {
		return "", instanceValidationError("Guest Control agent credential is invalid")
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return "", instanceValidationError("Guest Control credential encoding failed")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func validateGuestControlReceipt(receipt guestControlBackingReceipt) error {
	if !vmIDPattern.MatchString(receipt.SourceVMID) || !isCanonicalManagedResourceID(receipt.Version, 64) ||
		(receipt.Phase != "pending" && receipt.Phase != "bound") {
		return instanceValidationError("Guest Control provisioning receipt is invalid")
	}
	return nil
}

func guestControlCredentialLeaf(version string) string {
	return guestControlCredentialPrefix + version + ".json"
}

func readGuestControlCredential(
	ctx context.Context, directory int, ownerUID uint32, receipt guestControlBackingReceipt,
) (guestControlCredentialRecord, error) {
	if err := validateGuestControlReceipt(receipt); err != nil {
		return guestControlCredentialRecord{}, err
	}
	var record guestControlCredentialRecord
	if err := readProtectedRecord(ctx, directory, guestControlCredentialLeaf(receipt.Version), &record); err != nil {
		return guestControlCredentialRecord{}, instanceAtomicError("read Guest Control credential authority", err)
	}
	version, err := record.version()
	if err != nil || version != receipt.Version || record.OwnerUID != ownerUID || record.SourceVMID != receipt.SourceVMID {
		return guestControlCredentialRecord{}, instanceValidationError("Guest Control credential version or owner differs")
	}
	return record, nil
}

type GuestControlCredentialServer struct{}

func NewGuestControlCredentialServer() *GuestControlCredentialServer {
	return &GuestControlCredentialServer{}
}

// Prepare returns only the root-generated agent half for the exact pending cold VM. The operator already
// controls guest root; tampering with these staging bytes cannot change the expected root pin or acquire grants.
func (server *GuestControlCredentialServer) Prepare(
	ctx context.Context, uid, gid uint32, request GuestControlPrepareRequest,
) (_ GuestControlPrepareResult, returnErr error) {
	if err := request.Validate(); err != nil {
		return GuestControlPrepareResult{}, err
	}
	authority, err := AcquireVMProvisionRootfs(ctx, uid, gid, request.VMID, request.CacheRoot)
	if err != nil {
		return GuestControlPrepareResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, authority.Release(context.WithoutCancel(ctx))) }()
	authority.mu.Lock()
	defer authority.mu.Unlock()
	backing := authority.backing
	backing.mu.Lock()
	defer backing.mu.Unlock()
	if authority.protected {
		if backing.record.GuestControl == nil || backing.record.GuestControl.Phase != "bound" {
			return GuestControlPrepareResult{}, instanceValidationError("Guest Control requires an absent cold backing")
		}
		receipt := *backing.record.GuestControl
		if _, err := readCredentialForBacking(ctx, authority.cache, backing, receipt); err != nil {
			return GuestControlPrepareResult{}, err
		}
		return GuestControlPrepareResult{
			VMID: request.VMID, Reference: GuestControlReference{receipt.SourceVMID, receipt.Version},
		}, nil
	}
	// Establish the ordinary typed store layout so cancellation/cache retirement can inventory
	// pending credentials even when no disk has yet been imported.
	if err := ensureGuestControlColdStore(ctx, authority.cache); err != nil {
		return GuestControlPrepareResult{}, err
	}
	receipt, record, err := prepareColdGuestControl(ctx, backing)
	if err != nil {
		return GuestControlPrepareResult{}, err
	}
	return GuestControlPrepareResult{
		VMID: request.VMID, Reference: GuestControlReference{receipt.SourceVMID, receipt.Version},
		NeedsProvision: true, AgentMaterial: append([]byte(nil), record.AgentMaterial...)}, nil
}

func ensureGuestControlColdStore(ctx context.Context, cache *managedCacheLease) (returnErr error) {
	anchor, retained, err := openProtectedAnchor(ctx, cache.ownerUID, cache.identity, false)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, closeProtectedFDs(retained)) }()
	lock, err := acquireProtectedLock(ctx, anchor)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(lock)) }()
	store, err := pinProtectedStore(ctx, cache, anchor, true)
	if err != nil {
		return err
	}
	return unix.Close(store)
}

func prepareColdGuestControl(ctx context.Context, backing *protectedBackingLease,
) (guestControlBackingReceipt, guestControlCredentialRecord, error) {
	var receipt guestControlBackingReceipt
	err := readProtectedRecord(ctx, backing.metadataFD, guestControlBindingLeaf, &receipt)
	if err == nil {
		if receipt.Phase != "pending" || receipt.SourceVMID != backing.record.ID {
			return receipt, guestControlCredentialRecord{}, instanceValidationError("Guest Control cold binding differs")
		}
		record, err := readGuestControlCredential(ctx, backing.metadataFD, backing.ownerUID, receipt)
		return receipt, record, err
	}
	if !errors.Is(err, unix.ENOENT) {
		return receipt, guestControlCredentialRecord{}, err
	}
	host, agent, err := guestcontrolauth.Generate(ctx)
	if err != nil {
		return receipt, guestControlCredentialRecord{}, err
	}
	rootRaw, err := host.Encode()
	if err != nil {
		return receipt, guestControlCredentialRecord{}, err
	}
	agentRaw, err := agent.Encode()
	if err != nil {
		return receipt, guestControlCredentialRecord{}, err
	}
	record := guestControlCredentialRecord{1, backing.ownerUID, backing.record.ID, rootRaw, agentRaw}
	version, err := record.version()
	if err != nil {
		return receipt, record, err
	}
	if err := writeProtectedRecord(ctx, backing.metadataFD, guestControlCredentialLeaf(version), record); err != nil {
		return receipt, record, err
	}
	receipt = guestControlBackingReceipt{backing.record.ID, version, "pending"}
	if err := writeProtectedRecord(ctx, backing.metadataFD, guestControlBindingLeaf, receipt); err != nil {
		return receipt, record, err
	}
	return receipt, record, nil
}

// bindColdGuestControl imports EXPECTED lineage only. The operator may change guest staging contents;
// this receipt makes no assertion about successful injection, guest permissions, or authenticated readiness.
func bindColdGuestControl(ctx context.Context, backing *protectedBackingLease) error {
	if backing.record.Kind != protectedBackingRootfs || backing.record.SnapshotSource {
		return nil
	}
	var prepared guestControlBackingReceipt
	err := readProtectedRecord(ctx, backing.metadataFD, guestControlBindingLeaf, &prepared)
	if errors.Is(err, unix.ENOENT) {
		return nil
	} else if err != nil {
		return err
	}
	if prepared.SourceVMID != backing.record.ID || prepared.Phase != "pending" {
		return instanceValidationError("Guest Control expected cold import binding differs")
	}
	if _, err := readGuestControlCredential(ctx, backing.metadataFD, backing.ownerUID, prepared); err != nil {
		return err
	}
	prepared.Phase = "bound"
	if current := backing.record.GuestControl; current != nil {
		if *current != prepared {
			return instanceValidationError("Guest Control protected import credential was replaced")
		}
		return nil
	}
	next := backing.record
	next.GuestControl = &prepared
	if err := writeProtectedRecord(ctx, backing.metadataFD, "record.json", next); err != nil {
		return err
	}
	backing.record = next
	return nil
}

// CheckGuestControlAgentMaterial authorizes the ordinary typed LoopMount write against root's
// pending immutable selection. It never imports a caller certificate or pin into root authority.
func (lease *VMProvisionRootfsLease) CheckGuestControlAgentMaterial(ctx context.Context, raw []byte) error {
	if lease == nil || lease.vmProvisionRootfsLeaseState == nil || ctx == nil {
		return instanceValidationError("Guest Control provisioning authority is required")
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released || lease.backing == nil {
		return instanceValidationError("Guest Control provisioning authority is inactive")
	}
	backing := lease.backing
	backing.mu.Lock()
	defer backing.mu.Unlock()
	var receipt guestControlBackingReceipt
	if err := readProtectedRecord(ctx, backing.metadataFD, guestControlBindingLeaf, &receipt); err != nil {
		return err
	}
	if receipt.SourceVMID != backing.record.ID || receipt.Phase != "pending" {
		return instanceValidationError("Guest Control provisioning selection differs")
	}
	record, err := readGuestControlCredential(ctx, backing.metadataFD, backing.ownerUID, receipt)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(raw, record.AgentMaterial) != 1 {
		return instanceValidationError("Guest Control agent material differs from root selection")
	}
	return nil
}

func readCredentialForBacking(ctx context.Context, cache *managedCacheLease, backing *protectedBackingLease,
	receipt guestControlBackingReceipt,
) (guestControlCredentialRecord, error) {
	if receipt.SourceVMID == backing.record.ID {
		return readGuestControlCredential(ctx, backing.metadataFD, backing.ownerUID, receipt)
	}
	return readBoundGuestControlCredential(ctx, cache, receipt)
}

// readBoundGuestControlCredential follows only a root receipt, including a root-captured lineage after
// restore into a different VM. The caller retains shared protected-store-use authority, preventing whole-cache
// retirement. Immutable credential files need no live source VM or source VM lock.
func readBoundGuestControlCredential(
	ctx context.Context, cache *managedCacheLease, receipt guestControlBackingReceipt,
) (_ guestControlCredentialRecord, returnErr error) {
	if err := validateGuestControlReceipt(receipt); err != nil {
		return guestControlCredentialRecord{}, err
	}
	anchor, retained, err := openProtectedAnchor(ctx, cache.ownerUID, cache.identity, false)
	if err != nil {
		return guestControlCredentialRecord{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, closeProtectedFDs(retained)) }()
	kind, err := openProtectedDirectory(ctx, anchor, protectedBackingRootfs, false, true)
	if err != nil {
		return guestControlCredentialRecord{}, err
	}
	retained = append(retained, kind)
	source, err := openProtectedDirectory(ctx, kind, receipt.SourceVMID, false, true)
	if err != nil {
		return guestControlCredentialRecord{}, err
	}
	retained = append(retained, source)
	return readGuestControlCredential(ctx, source, cache.ownerUID, receipt)
}

// readGuestControlHostMaterial is a read-only projection for a later worker admission. Caller must retain
// the exact VM/backing authority; neither a path nor caller-selected material can enter this seam.
func (backing *protectedBackingLease) readGuestControlHostMaterial(
	ctx context.Context, cache *managedCacheLease,
) (guestcontrolauth.RootHostMaterial, error) {
	backing.mu.Lock()
	defer backing.mu.Unlock()
	if err := backing.active(ctx); err != nil {
		return guestcontrolauth.RootHostMaterial{}, err
	}
	if cache.ownerUID != backing.ownerUID || cache.identity != backing.cacheIdentity ||
		backing.record.GuestControl == nil || backing.record.GuestControl.Phase != "bound" {
		return guestcontrolauth.RootHostMaterial{}, instanceValidationError("Guest Control import credential is unavailable")
	}
	record, err := readBoundGuestControlCredential(ctx, cache, *backing.record.GuestControl)
	if err != nil {
		return guestcontrolauth.RootHostMaterial{}, err
	}
	return guestcontrolauth.DecodeRootHostMaterial(record.RootMaterial)
}

func isGuestControlCredentialLeaf(name string) bool {
	version := strings.TrimSuffix(strings.TrimPrefix(name, guestControlCredentialPrefix), ".json")
	return name == guestControlCredentialLeaf(version) && isCanonicalManagedResourceID(version, 64)
}

type guestControlRetirementLeaf struct {
	directory int
	fd        int
	name      string
	stat      unix.Stat_t
}

func (plan *protectedCacheCleanupPlan) admitGuestControlMetadata(
	ctx context.Context, metadata int, sourceVMID string, leaves []string,
) ([]string, error) {
	var ordinary []string
	for _, leaf := range leaves {
		base := strings.TrimSuffix(leaf, ".next")
		if base != guestControlBindingLeaf && !isGuestControlCredentialLeaf(base) {
			ordinary = append(ordinary, leaf)
			continue
		}
		var admitted guestControlRetirementLeaf
		var err error
		if base == guestControlBindingLeaf {
			var receipt guestControlBackingReceipt
			if err := readProtectedRecord(ctx, metadata, leaf, &receipt); err != nil {
				return nil, err
			}
			if receipt.SourceVMID != sourceVMID || receipt.Phase != "pending" || validateGuestControlReceipt(receipt) != nil {
				return nil, instanceValidationError("Guest Control retired pending binding differs")
			}
			if _, err := readGuestControlCredential(ctx, metadata, plan.cache.ownerUID, receipt); err != nil {
				return nil, err
			}
			fd, openErr := openProtectedFile(ctx, metadata, leaf, unix.O_RDONLY)
			if openErr != nil {
				return nil, openErr
			}
			var stat unix.Stat_t
			if err := unix.Fstat(fd, &stat); err != nil {
				return nil, errors.Join(err, unix.Close(fd))
			}
			admitted = guestControlRetirementLeaf{metadata, fd, leaf, stat}
		} else {
			admitted, err = admitGuestControlRetirementLeaf(ctx, metadata, leaf, plan.cache.ownerUID, sourceVMID)
		}
		if err != nil {
			return nil, err
		}
		plan.retained = append(plan.retained, admitted.fd)
		plan.guestControlCredentials = append(plan.guestControlCredentials, admitted)
	}
	return ordinary, nil
}

// admitGuestControlRetirementLeaf admits only canonical immutable source material. Called after
// whole-cache retirement has exclusive store-use authority and proved snapshot branches empty.
func admitGuestControlRetirementLeaf(
	ctx context.Context, directory int, name string, ownerUID uint32, sourceVMID string,
) (_ guestControlRetirementLeaf, returnErr error) {
	leaf := strings.TrimSuffix(name, ".next")
	if !isGuestControlCredentialLeaf(leaf) {
		return guestControlRetirementLeaf{}, instanceValidationError("unknown Guest Control credential retirement leaf")
	}
	version := strings.TrimSuffix(strings.TrimPrefix(leaf, guestControlCredentialPrefix), ".json")
	fd, err := openProtectedFile(ctx, directory, name, unix.O_RDONLY)
	if err != nil {
		return guestControlRetirementLeaf{}, err
	}
	accepted := false
	defer func() {
		if !accepted {
			returnErr = errors.Join(returnErr, unix.Close(fd))
		}
	}()
	var before, after unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return guestControlRetirementLeaf{}, err
	}
	var record guestControlCredentialRecord
	if err := readProtectedRecord(ctx, directory, name, &record); err != nil {
		return guestControlRetirementLeaf{}, err
	}
	actual, err := record.version()
	if err != nil || actual != version || record.OwnerUID != ownerUID || record.SourceVMID != sourceVMID {
		return guestControlRetirementLeaf{}, instanceValidationError("Guest Control retirement credential binding differs")
	}
	if err := unix.Fstatat(directory, name, &after, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
		!sameProtectedReadStat(before, after) {
		return guestControlRetirementLeaf{}, instanceValidationError("Guest Control retirement credential changed")
	}
	accepted = true
	return guestControlRetirementLeaf{directory, fd, name, before}, nil
}

func (plan *protectedCacheCleanupPlan) retireGuestControlCredentials(ctx context.Context) error {
	// References must disappear durably before any material they name. Inventory sorting is
	// insufficient: a version hash can sort before "binding", making an interrupted deletion
	// leave a reference whose material is gone and permanently preventing safe admission.
	for _, leaf := range plan.guestControlCredentials {
		if strings.TrimSuffix(leaf.name, ".next") == guestControlBindingLeaf {
			if err := retireGuestControlLeaf(ctx, leaf); err != nil {
				return err
			}
		}
	}
	for _, leaf := range plan.guestControlCredentials {
		if strings.TrimSuffix(leaf.name, ".next") == guestControlBindingLeaf {
			continue
		}
		// A previous reference unlink may have succeeded but its fsync failed. On retry
		// inventory sees no reference, so seal that observed absence AGAIN before deletion.
		if err := unix.Fsync(leaf.directory); err != nil {
			return err
		}
		if err := retireGuestControlLeaf(ctx, leaf); err != nil {
			return err
		}
	}
	return nil
}

func retireGuestControlLeaf(ctx context.Context, leaf guestControlRetirementLeaf) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var pinned, current unix.Stat_t
	if err := unix.Fstat(leaf.fd, &pinned); err != nil {
		return err
	}
	if err := unix.Fstatat(leaf.directory, leaf.name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !sameProtectedReadStat(leaf.stat, pinned) || !sameProtectedReadStat(pinned, current) {
		return instanceValidationError("Guest Control retired credential identity changed")
	}
	if err := unix.Unlinkat(leaf.directory, leaf.name, 0); err != nil {
		return err
	}
	return unix.Fsync(leaf.directory)
}
