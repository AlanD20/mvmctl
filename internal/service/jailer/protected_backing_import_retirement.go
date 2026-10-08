package jailer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math"
	"os"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/authorityfs"
)

// protectedImportRetirement is held only by final VM removal. Ordinary backing lookup cannot
// convert an incomplete import into either a ready lease or initial-source authority.
type protectedImportRetirement struct {
	locked   *lockedInstance
	ownerUID uint32
	vmID     string
	identity vmExecutionIdentity
}

func newProtectedImportRetirement(
	ctx context.Context, locked *lockedInstance, caller instanceCaller, identity vmExecutionIdentity,
) (*protectedImportRetirement, error) {
	if locked == nil {
		return nil, instanceStateError("", "protected import retirement requires retained VM authority")
	}
	retirement := &protectedImportRetirement{
		locked: locked, ownerUID: caller.uid, vmID: locked.record.vmID, identity: identity,
	}
	if err := retirement.requireAuthority(ctx); err != nil {
		return nil, err
	}
	if err := retirement.requireAbsence(ctx); err != nil {
		return nil, err
	}
	return retirement, nil
}

func (retirement *protectedImportRetirement) requireAuthority(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if retirement == nil || retirement.locked == nil || retirement.locked.vmLock == nil ||
		retirement.locked.vmLock.state == nil || os.Geteuid() != 0 || os.Getegid() != 0 {
		return instanceStateError("", "protected import retirement requires root VM authority")
	}
	state := retirement.locked.vmLock.state
	state.mu.Lock()
	defer state.mu.Unlock()
	return retirement.requireLockedAuthority(state)
}

// CALLER MUST HOLD: state.mu. Retaining this mutex across removal excludes authority release.
func (retirement *protectedImportRetirement) requireLockedAuthority(state *vmAuthorityLeaseState) error {
	key, err := authorityfs.NewVMKey(retirement.vmID)
	if err != nil || state.released || !state.active || state.value == nil ||
		state.ownerUID != retirement.ownerUID || state.key != key ||
		retirement.locked.record.lifecycle != instanceLifecycleCleaned ||
		retirement.locked.record.ownerUID != retirement.ownerUID ||
		retirement.locked.record.vmID != retirement.vmID ||
		retirement.identity.uid == 0 || retirement.identity.uid == math.MaxUint32 ||
		retirement.identity.gid == 0 || retirement.identity.gid == math.MaxUint32 ||
		retirement.identity.uid == retirement.ownerUID {
		return instanceStateError(retirement.vmID, "protected import retirement owner or VM authority changed")
	}
	return nil
}

func (retirement *protectedImportRetirement) requireAbsence(ctx context.Context) error {
	state := retirement.locked.vmLock.state
	if err := state.accounts.requireProcessAbsence(ctx, retirement.identity); err != nil {
		return err
	}
	if err := requireExecutionRetirementTreesAbsent(ctx, state.policy, retirement.ownerUID, retirement.vmID); err != nil {
		return err
	}
	return requireProtectedVMGrantsAbsent(
		ctx,
		state.policy,
		retirement.ownerUID,
		retirement.vmID,
		retirement.identity.uid,
	)
}

type protectedImportCancellation struct {
	cache       *managedCacheLease
	anchorFD    int
	storeFD     int
	dataKindFD  int
	logicalFD   int
	metadataFD  int
	directoryFD int
	retained    []int
	record      protectedBackingRecord
}

// The dependencies cover the three durable cancellation boundaries. Admission always stays in
// this module; these functions neither select candidates nor supply authority.
type protectedImportCancellationDeps struct {
	writeRecord     func(context.Context, int, protectedBackingRecord) error
	unlinkCandidate func(context.Context, int, protectedImportLeaf) error
	syncDirectory   func(context.Context, int) error
}

type protectedImportLeaf string

const (
	protectedImportPending protectedImportLeaf = "pending"
	protectedImportDisk    protectedImportLeaf = "disk"
)

func productionProtectedImportCancellationDeps() protectedImportCancellationDeps {
	return protectedImportCancellationDeps{
		writeRecord: func(ctx context.Context, metadata int, record protectedBackingRecord) error {
			return writeProtectedRecord(ctx, metadata, "record.json", record)
		},
		unlinkCandidate: func(ctx context.Context, directory int, leaf protectedImportLeaf) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return unix.Unlinkat(directory, string(leaf), 0)
		},
		syncDirectory: func(ctx context.Context, directory int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return unix.Fsync(directory)
		},
	}
}

// openProtectedImportCancellation opens existing authority only, including never-unlinked locks.
// CRITICAL: It never opens the source, creates a store/object, or repairs a mode or ACL.
func openProtectedImportCancellation(
	ctx context.Context, cache *managedCacheLease, kind, vmID string,
) (_ *protectedImportCancellation, returnErr error) {
	if err := validateActiveManagedCacheLease(cache); err != nil {
		return nil, err
	}
	if (kind != protectedBackingRootfs && kind != protectedBackingCloudInit) ||
		!isCanonicalManagedResourceID(vmID, 32) {
		return nil, managedVMResourceValidationError("protected import retirement selection is invalid")
	}
	anchor, retained, err := openProtectedAnchor(ctx, cache.ownerUID, cache.identity, false)
	if err != nil {
		return nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, closeProtectedFDs(retained))
		}
	}()
	parent := retained[len(retained)-2]
	for _, component := range []string{"logical", kind, vmID} {
		parent, err = openProtectedDirectory(ctx, parent, component, false, true)
		if err != nil {
			return nil, err
		}
		retained = append(retained, parent)
	}
	logicalFD := parent
	lock, err := openProtectedImportLock(ctx, logicalFD)
	if err != nil {
		return nil, err
	}
	retained = append(retained, lock)
	storeLock, err := openProtectedImportLock(ctx, anchor)
	if err != nil {
		return nil, err
	}
	store, storeErr := pinProtectedStore(ctx, cache, anchor, false)
	if store >= 0 {
		retained = append(retained, store)
	}
	if err := errors.Join(storeErr, unix.Close(storeLock)); err != nil {
		return nil, err
	}
	parent = anchor
	for _, component := range []string{kind, vmID} {
		parent, err = openProtectedDirectory(ctx, parent, component, false, true)
		if err != nil {
			return nil, err
		}
		retained = append(retained, parent)
	}
	metadataFD := parent
	lock, err = openProtectedImportLock(ctx, metadataFD)
	if err != nil {
		return nil, err
	}
	retained = append(retained, lock)
	parent = store
	dataKindFD := -1
	for _, component := range []string{kind, vmID} {
		parent, err = openProtectedDirectory(ctx, parent, component, false, true)
		if err != nil {
			return nil, err
		}
		retained = append(retained, parent)
		if component == kind {
			dataKindFD = parent
		}
	}
	cancellation := &protectedImportCancellation{
		cache: cache, anchorFD: anchor, storeFD: store, dataKindFD: dataKindFD,
		logicalFD: logicalFD, metadataFD: metadataFD, directoryFD: parent, retained: retained,
	}
	if err := cancellation.readAuthority(ctx, cache.ownerUID, cache.identity, kind, vmID); err != nil {
		return nil, err
	}
	return cancellation, nil
}

func openProtectedImportLock(ctx context.Context, directory int) (int, error) {
	fd, err := openProtectedFile(ctx, directory, "lock", unix.O_RDWR)
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Size != 0 || stat.Mode&07777 != 0600 {
		return -1, errors.Join(managedVMResourceAtomicError("inspect existing import lock", err), unix.Close(fd))
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return -1, errors.Join(instanceLockContextError("protected import retirement", err), unix.Close(fd))
	}
	return fd, nil
}

func (cancellation *protectedImportCancellation) readAuthority(
	ctx context.Context, ownerUID uint32, cache managedCacheIdentity, kind, vmID string,
) error {
	var logical protectedLogicalRecord
	if err := readProtectedRecord(ctx, cancellation.logicalFD, "logical.json", &logical); err != nil {
		return err
	}
	if logical.Version != 1 || logical.Owner != ownerUID || logical.Kind != kind || logical.ID != vmID ||
		logical.Cache != protectedCacheIdentity(cache) || !logical.Initialized {
		return managedVMResourceValidationError("protected import retirement logical authority conflicts")
	}
	if err := readProtectedRecord(ctx, cancellation.metadataFD, "record.json", &cancellation.record); err != nil {
		return err
	}
	record := cancellation.record
	if record.Version != 1 || record.Owner != ownerUID || record.Kind != kind || record.ID != vmID ||
		record.Cache != logical.Cache || len(record.References) != 0 || len(record.NextReferences) != 0 {
		return managedVMResourceValidationError("protected import retirement record conflicts or retains grants")
	}
	return cancellation.requireDirectory(ctx)
}

func (cancellation *protectedImportCancellation) requireDirectory(ctx context.Context) error {
	if err := validateActiveManagedCacheLease(cancellation.cache); err != nil {
		return err
	}
	cacheIdentity, err := protectedIdentity(cancellation.cache.cacheFD)
	if err != nil || cacheIdentity.Device != unix.Mkdev(cancellation.cache.identity.deviceMajor,
		cancellation.cache.identity.deviceMinor) || cacheIdentity.Inode != cancellation.cache.identity.inode {
		return managedVMResourceAtomicError("protected import retirement cache identity changed", err)
	}
	store, err := pinProtectedStore(ctx, cancellation.cache, cancellation.anchorFD, false)
	if err != nil {
		return err
	}
	storeIdentity, storeErr := protectedIdentity(store)
	retainedStore, retainedErr := protectedIdentity(cancellation.storeFD)
	if err := errors.Join(storeErr, retainedErr, unix.Close(store)); err != nil {
		return err
	}
	if !sameProtectedNode(storeIdentity, retainedStore) {
		return managedVMResourceValidationError("protected import retirement store binding changed")
	}
	current, err := openProtectedDirectory(ctx, cancellation.dataKindFD, cancellation.record.ID, false, true)
	if err != nil {
		return err
	}
	currentIdentity, currentErr := protectedIdentity(current)
	if err := errors.Join(currentErr, unix.Close(current)); err != nil {
		return err
	}
	if !sameProtectedNode(currentIdentity, cancellation.record.Directory) {
		return managedVMResourceValidationError("protected import retirement object binding changed")
	}
	fd, err := openProtectedDirectory(ctx, cancellation.directoryFD, ".", false, true)
	if err != nil {
		return err
	}
	identity, statErr := protectedIdentity(fd)
	if err := errors.Join(statErr, unix.Close(fd)); err != nil {
		return err
	}
	if !sameProtectedNode(identity, cancellation.record.Directory) {
		return managedVMResourceValidationError("protected import retirement directory identity changed")
	}
	return nil
}

func (cancellation *protectedImportCancellation) cancel(
	ctx context.Context, deps protectedImportCancellationDeps,
) error {
	if deps.writeRecord == nil || deps.unlinkCandidate == nil || deps.syncDirectory == nil {
		return managedVMResourceValidationError("protected import cancellation dependencies are incomplete")
	}
	if err := cancellation.requireDirectory(ctx); err != nil {
		return err
	}
	phase := cancellation.record.Phase
	if len(cancellation.record.References) != 0 || len(cancellation.record.NextReferences) != 0 ||
		(phase != "copying" && phase != "copied" && phase != "cancelling-copy" && phase != "cancelling-copied") {
		return managedVMResourceValidationError("protected import cancellation lacks an inactive import record")
	}
	leaf, identity, err := cancellation.admitCandidate(ctx)
	if err != nil {
		return err
	}
	intent := cancellation.record
	if phase == "copying" || phase == "copied" {
		if intent.Backing.Inode == 0 && leaf != "" {
			// Only an empty exact root-only pending inode can fill the crash gap before import
			// recorded it. Nonempty unrecorded bytes never become deletion authority.
			intent.Backing = identity
		}
		intent.Phase = "cancelling-copy"
		if phase == "copied" {
			intent.Phase = "cancelling-copied"
		}
	}
	// A prior rename may have succeeded while its metadata-directory fsync failed. Replay
	// re-establishes the intent's durability before any candidate unlink, even if bytes are gone.
	if err := deps.writeRecord(ctx, cancellation.metadataFD, intent); err != nil {
		return err
	}
	cancellation.record = intent
	if err := cancellation.requireDirectory(ctx); err != nil {
		return err
	}
	// Repeat complete leaf admission after durable intent. The pinned root-owned directory
	// cannot authorize a replaced inode, foreign entry or new access grant on replay.
	leaf, _, err = cancellation.admitCandidate(ctx)
	if err != nil {
		return err
	}
	if leaf != "" {
		if err := deps.unlinkCandidate(ctx, cancellation.directoryFD, leaf); err != nil {
			return managedVMResourceAtomicError("remove cancelled protected import candidate", err)
		}
	}
	if err := cancellation.requireEmpty(ctx); err != nil {
		return err
	}
	if err := deps.syncDirectory(ctx, cancellation.directoryFD); err != nil {
		return managedVMResourceAtomicError("sync cancelled protected import directory", err)
	}
	if err := cancellation.requireDirectory(ctx); err != nil {
		return err
	}
	if err := cancellation.requireEmpty(ctx); err != nil {
		return err
	}
	terminal := cancellation.record
	terminal.Phase = "removed"
	if err := deps.writeRecord(ctx, cancellation.metadataFD, terminal); err != nil {
		return err
	}
	cancellation.record = terminal
	return nil
}

func (cancellation *protectedImportCancellation) requireEmpty(ctx context.Context) error {
	leaf, _, err := cancellation.admitCandidate(ctx)
	if err != nil {
		return err
	}
	if leaf != "" {
		return managedVMResourceValidationError("protected import candidate remains after cancellation")
	}
	return nil
}

func (cancellation *protectedImportCancellation) admitCandidate(
	ctx context.Context,
) (_ protectedImportLeaf, _ protectedBackingIdentity, returnErr error) {
	fd, err := openProtectedDirectory(ctx, cancellation.directoryFD, ".", false, true)
	if err != nil {
		return "", protectedBackingIdentity{}, err
	}
	file := os.NewFile(uintptr(fd), "protected-import-candidate-inventory")
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	names, err := file.Readdirnames(3)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", protectedBackingIdentity{}, managedVMResourceAtomicError("enumerate protected import leaves", err)
	}
	if len(names) > 1 {
		return "", protectedBackingIdentity{}, managedVMResourceValidationError(
			"protected import has inconsistent leaves",
		)
	}
	record := cancellation.record
	copying := record.Phase == "copying" || record.Phase == "cancelling-copy"
	maximumSource := int64(managedVMCloudInitMaxBytes)
	minimumSource := int64(managedVMCloudInitMinBytes)
	if record.Kind == protectedBackingRootfs {
		minimumSource, maximumSource = int64(managedVMRootfsMinBytes), int64(managedVMRootfsMaxBytes)
	}
	if record.Backing.Size < 0 || (record.Backing.Inode == 0 && record.Backing != (protectedBackingIdentity{})) ||
		(record.Source.Inode == 0 && record.Source != (protectedBackingIdentity{})) ||
		(record.Source.Inode != 0 && (record.Source.Size < minimumSource || record.Source.Size > maximumSource)) ||
		(copying && record.Backing.Size > record.Source.Size) ||
		(!copying && (record.Backing.Inode == 0 || record.Backing.Size <= 0 || record.Backing.Size != record.Source.Size)) {
		return "", protectedBackingIdentity{}, managedVMResourceValidationError(
			"protected import byte authority is inconsistent",
		)
	}
	if len(names) == 0 {
		if (record.Phase == "copying" && record.Backing.Inode == 0) ||
			record.Phase == "cancelling-copy" || record.Phase == "cancelling-copied" {
			return "", protectedBackingIdentity{}, nil
		}
		return "", protectedBackingIdentity{}, managedVMResourceValidationError(
			"recorded protected import candidate disappeared",
		)
	}
	leaf := protectedImportLeaf(names[0])
	if leaf != protectedImportPending && (!copying && leaf != protectedImportDisk) ||
		(copying && leaf != protectedImportPending) {
		return "", protectedBackingIdentity{}, managedVMResourceValidationError(
			"protected import contains an unexpected leaf",
		)
	}
	candidate, err := openProtectedFile(ctx, cancellation.directoryFD, string(leaf), unix.O_RDONLY)
	if err != nil {
		return "", protectedBackingIdentity{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(candidate)) }()
	var stat unix.Stat_t
	if err := unix.Fstat(candidate, &stat); err != nil {
		return "", protectedBackingIdentity{}, err
	}
	identity := protectedBackingIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Size: stat.Size}
	if stat.Mode&07777 != 0600 || stat.Size < 0 ||
		(record.Backing.Inode == 0 && (leaf != protectedImportPending || stat.Size != 0)) ||
		(record.Backing.Inode != 0 && !sameProtectedNode(identity, record.Backing)) ||
		(copying && record.Backing.Inode != 0 && stat.Size > record.Source.Size) ||
		(!copying && identity != record.Backing) {
		return "", protectedBackingIdentity{}, managedVMResourceValidationError(
			"protected import candidate metadata differs from authority",
		)
	}
	expected, err := protectedACLBytes(nil)
	if err != nil {
		return "", protectedBackingIdentity{}, err
	}
	buffer := make([]byte, len(expected)+1)
	n, aclErr := unix.Fgetxattr(candidate, protectedBackingACL, buffer)
	if !errors.Is(aclErr, unix.ENODATA) &&
		(aclErr != nil || n != len(expected) || !bytes.Equal(buffer[:n], expected)) {
		return "", protectedBackingIdentity{}, managedVMResourceAtomicError(
			"protected import candidate has foreign access grants",
			aclErr,
		)
	}
	var current unix.Stat_t
	if err := unix.Fstatat(cancellation.directoryFD, string(leaf), &current, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
		!sameProtectedReadStat(stat, current) {
		return "", protectedBackingIdentity{}, managedVMResourceAtomicError(
			"protected import candidate binding changed",
			err,
		)
	}
	return leaf, identity, nil
}
