package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// CALLER MUST HOLD: destination shared store-use, logical and object locks. Old-store acquisition
// is nonblocking, so retirement's exclusive store-use -> logical ordering cannot deadlock this path.
func prepareProtectedKernelImport(ctx context.Context, cache *managedCacheLease, ownerFD, logicalFD, storeFD int,
	id string, backing *protectedBackingRecord, snapshotSource *protectedBackingIdentity,
) (_ protectedLogicalRecord, _ []int, returnErr error) {
	record, err := readProtectedKernelLogical(ctx, logicalFD, cache.ownerUID, id)
	if err != nil {
		return record, nil, err
	}
	store, err := protectedIdentity(storeFD)
	if err != nil {
		return record, nil, err
	}
	selected := protectedCacheIdentity(cache.identity)
	if pending := record.Kernel.Pending; pending != nil {
		if (pending.SnapshotSource == nil) != (snapshotSource == nil) ||
			pending.SnapshotSource != nil && *pending.SnapshotSource != *snapshotSource {
			return record, nil, managedVMResourceValidationError("kernel pending import belongs to a different producer")
		}
		if pending.Cache != selected || !sameProtectedNode(pending.Store, store) {
			return record, nil, managedVMResourceValidationError("kernel pending import selects a different destination")
		}
		if err := syncProtectedCacheDirectory(ctx, logicalFD); err != nil {
			return record, nil, err
		}
		if current := record.Kernel.Current; current != nil && current.Cache != selected {
			previous, err := admitProtectedKernelPredecessor(ctx, cache.ownerUID, ownerFD, logicalFD, id, *current, true)
			return record, previous, err
		}
		return record, nil, nil
	}
	current := record.Kernel.Current
	if current != nil && current.Phase == "ready" {
		if current.Cache != selected || !sameProtectedNode(current.Store, store) {
			return record, nil, managedVMResourceValidationError("live kernel cannot select a different cache")
		}
		return record, nil, syncProtectedCacheDirectory(ctx, logicalFD)
	}
	if current == nil && record.Cache != selected {
		return record, nil, managedVMResourceValidationError("unprotected kernel cannot move to another cache")
	}
	if current == nil && snapshotSource == nil && backing != nil &&
		backing.Phase == "kernel-aborted" && backing.SnapshotSource {
		return record, nil, managedVMResourceValidationError("initial snapshot abort requires its original trusted producer")
	}
	var retained []int
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, closeProtectedFDs(retained))
		}
	}()
	predecessor := uint64(0)
	if current != nil {
		predecessor = current.Generation
		retained, err = admitProtectedKernelPredecessor(ctx, cache.ownerUID, ownerFD, logicalFD, id, *current,
			current.Cache != selected)
		if err != nil {
			return record, nil, err
		}
	}
	generation, err := nextProtectedKernelGeneration(*record.Kernel)
	if err != nil {
		return record, nil, err
	}
	directory := protectedBackingIdentity{}
	if backing == nil {
		if err := requireNewProtectedKernelDirectoryAbsent(ctx, storeFD, id); err != nil {
			return record, nil, err
		}
	} else {
		if backing.Kind != protectedBackingKernel || backing.ID != id || backing.Owner != cache.ownerUID ||
			backing.Cache != selected || (backing.Phase != "removed" && backing.Phase != "kernel-aborted" &&
			backing.Phase != "kernel-unprotected-removed") ||
			len(backing.References) != 0 || len(backing.NextReferences) != 0 {
			return record, nil, managedVMResourceValidationError("kernel successor destination retains nonterminal authority")
		}
		directory = backing.Directory
	}
	record.Kernel.LastGeneration = generation
	record.Kernel.Pending = &protectedKernelPending{Generation: generation, Predecessor: predecessor,
		Cache: selected, Store: store, Directory: directory, Phase: "preparing", SnapshotSource: snapshotSource}
	if err := writeProtectedRecord(ctx, logicalFD, "logical.json", record); err != nil {
		return record, nil, err
	}
	return record, retained, nil
}

// CALLER MUST HOLD: the complete restored backing admission set. A validated immutable root
// snapshot is an initial producer, never permission to revive a removed kernel or another import.
func prepareProtectedKernelSnapshot(ctx context.Context, cache *managedCacheLease, ownerFD, logicalFD, storeFD int,
	id string, backing *protectedBackingRecord, sourceFD int, size uint64,
) (protectedLogicalRecord, bool, error) {
	if size < managedVMKernelMinBytes || size > managedVMKernelMaxBytes {
		return protectedLogicalRecord{}, false, managedVMResourceValidationError("snapshot kernel source is outside policy")
	}
	if err := validateRestoredBackingSource(ctx, sourceFD, size); err != nil {
		return protectedLogicalRecord{}, false, err
	}
	logical, err := readProtectedKernelLogical(ctx, logicalFD, cache.ownerUID, id)
	if err != nil {
		return logical, false, err
	}
	selected := protectedCacheIdentity(cache.identity)
	if logical.Kernel.Current != nil {
		// Already committed kernels remain consumers; finishRestoredBacking compares captured bytes.
		logical, err = requireProtectedKernelSelection(ctx, logicalFD, cache.ownerUID, id, selected, true)
		return logical, false, err
	}
	if backing != nil && (backing.Version != 1 || backing.Owner != cache.ownerUID ||
		backing.Kind != protectedBackingKernel || backing.ID != id || backing.Cache != selected ||
		len(backing.References) != 0 || len(backing.NextReferences) != 0) {
		return logical, false, managedVMResourceValidationError(
			"initial snapshot kernel retains conflicting backing authority",
		)
	}
	if logical.Cache != selected {
		return logical, false, managedVMResourceValidationError("initial snapshot kernel cannot select another cache")
	}
	source, err := protectedIdentity(sourceFD)
	if err != nil {
		return logical, false, err
	}
	if pending := logical.Kernel.Pending; pending != nil {
		if pending.SnapshotSource == nil || *pending.SnapshotSource != source || pending.Predecessor != 0 {
			return logical, false, managedVMResourceValidationError("snapshot kernel cannot complete another producer's import")
		}
		if backing != nil && (!backing.SnapshotSource || backing.Source.Inode != 0 && backing.Source != source) {
			return logical, false, managedVMResourceValidationError("snapshot kernel backing differs from its sealed source")
		}
	} else if logical.Kernel.LastGeneration == 0 {
		if logical.Initialized || backing != nil {
			return logical, false, managedVMResourceValidationError("initial snapshot kernel retains unrelated authority")
		}
	} else {
		// A cancelled initial snapshot may retry only its exact source and terminal receipt.
		if backing == nil || !backing.SnapshotSource || backing.Source != source ||
			backing.Phase != "kernel-aborted" || backing.Generation != logical.Kernel.LastGeneration {
			return logical, false, managedVMResourceValidationError("initial snapshot retry has no matching aborted source")
		}
		store, err := protectedIdentity(storeFD)
		if err != nil {
			return logical, false, err
		}
		if err := requireProtectedKernelTerminal(ctx, logicalFD, logical, *backing, store); err != nil {
			return logical, false, err
		}
	}
	logical, previous, err := prepareProtectedKernelImport(ctx, cache, ownerFD, logicalFD, storeFD, id, backing, &source)
	return logical, true, errors.Join(err, closeProtectedFDs(previous)) // An initial producer has no predecessor leases.
}

func requireNewProtectedKernelDirectoryAbsent(ctx context.Context, storeFD int, id string) (returnErr error) {
	kind, err := openProtectedDirectory(ctx, storeFD, protectedBackingKernel, false, true)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(kind)) }()
	return protectedCacheNameAbsent(ctx, kind, id)
}

func admitProtectedKernelPredecessor(ctx context.Context, owner uint32, ownerFD, logicalFD int, id string,
	current protectedKernelIncarnation, requireRetiredStore bool) (_ []int, returnErr error) {
	var receipt protectedKernelReceipt
	if err := readProtectedRecord(ctx, logicalFD, protectedKernelReceiptLeaf(current.Generation), &receipt); err != nil {
		return nil, err
	}
	want := protectedKernelReceipt{Version: protectedKernelLogicalVersion, Owner: owner, ID: id, Binding: current}
	if receipt != want {
		return nil, managedVMResourceValidationError("kernel predecessor receipt differs from committed authority")
	}
	identity := managedCacheIdentity{deviceMajor: current.Cache.Major, deviceMinor: current.Cache.Minor,
		inode: current.Cache.Inode}
	// Reuse the pinned root-owned operator namespace. The recorded cache key cannot escape it.
	anchor, err := openProtectedDirectory(ctx, ownerFD, protectedCacheKey(identity), false, true)
	if err != nil {
		return nil, err
	}
	retained := []int{anchor}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, closeProtectedFDs(retained))
		}
	}()
	use, err := acquireProtectedStoreUse(ctx, anchor, false)
	if err != nil {
		return nil, err
	}
	retained = append(retained, use)
	var store protectedBackingAnchor
	if err := readProtectedRecord(ctx, anchor, "store.json", &store); err != nil {
		return nil, err
	}
	if err := validateProtectedStoreLayout(ctx, anchor, store); err != nil {
		return nil, err
	}
	if store.Owner != owner || store.Cache != current.Cache || !sameProtectedNode(store.Store, current.Store) {
		return nil, managedVMResourceValidationError("kernel predecessor store differs from terminal authority")
	}
	if requireRetiredStore && (store.Retirement == nil || store.Retirement.Phase != "removed" ||
		store.Retirement.Quarantine == nil || store.Retirement.Quarantine.Inode == 0) {
		return nil, errs.New(errs.CodeKernelImportFailed, "kernel successor requires completed predecessor cache retirement",
			errs.WithClass(errs.ClassConflict), errs.WithEntity(id),
			errs.WithDetails(map[string]any{"predecessor_store_retirement_required": true}))
	}
	metadata := anchor
	for _, component := range []string{protectedBackingKernel, id} {
		metadata, err = openProtectedDirectory(ctx, metadata, component, false, true)
		if err != nil {
			return nil, err
		}
		retained = append(retained, metadata)
	}
	// The selected object is already locked for a same-cache reimport.
	if requireRetiredStore {
		lock, err := acquireProtectedLockMode(ctx, metadata, false)
		if err != nil {
			return nil, err
		}
		retained = append(retained, lock)
	}
	var backing protectedBackingRecord
	if err := readProtectedRecord(ctx, metadata, "record.json", &backing); err != nil {
		return nil, err
	}
	if backing.Version != 1 || backing.Owner != owner || backing.Kind != protectedBackingKernel || backing.ID != id ||
		backing.Cache != current.Cache || backing.KernelDigest != current.Digest ||
		len(backing.References) != 0 || len(backing.NextReferences) != 0 {
		return nil, managedVMResourceValidationError("kernel predecessor backing is not exactly retired")
	}
	if backing.Phase == "kernel-aborted" {
		logical, err := readProtectedKernelLogical(ctx, logicalFD, owner, id)
		if err != nil {
			return nil, err
		}
		if logical.Kernel.Current == nil || *logical.Kernel.Current != current {
			return nil, managedVMResourceValidationError("kernel predecessor current authority changed")
		}
		if err := requireProtectedKernelAbortedProjection(ctx, logicalFD, logical, backing, store.Store); err != nil {
			return nil, err
		}
	} else if backing.Phase != "removed" || backing.Generation != current.Generation ||
		backing.Backing != current.Backing {
		return nil, managedVMResourceValidationError("kernel predecessor backing is not exactly retired")
	}
	// Terminal object and store publication may be visible after a parent-sync failure.
	if err := errors.Join(syncProtectedCacheDirectory(ctx, logicalFD), syncProtectedCacheDirectory(ctx, metadata),
		syncProtectedCacheDirectory(ctx, anchor)); err != nil {
		return nil, err
	}
	return retained, nil
}

// These closed effects mirror protected cache retirement's publication dependencies. Both
// filesystems must be sealed: visible rename results alone do not establish durable authority.
type protectedKernelPublicationDeps struct {
	syncBackingMetadata  func(context.Context, int) error
	syncLogicalAuthority func(context.Context, int) error
}

func realProtectedKernelPublicationDeps() protectedKernelPublicationDeps {
	return protectedKernelPublicationDeps{
		syncBackingMetadata:  syncProtectedCacheDirectory,
		syncLogicalAuthority: syncProtectedCacheDirectory,
	}
}

func (lease *protectedBackingLease) commitKernelGeneration(ctx context.Context) error {
	return lease.commitKernelGenerationWithPublication(ctx, realProtectedKernelPublicationDeps())
}

func (lease *protectedBackingLease) commitKernelGenerationWithPublication(
	ctx context.Context, deps protectedKernelPublicationDeps,
) error {
	if lease.kernelGeneration == 0 {
		return nil // Internal non-lineage backing consumers retain their existing contract.
	}
	record, err := readProtectedKernelLogical(ctx, lease.logicalFD, lease.ownerUID, lease.record.ID)
	if err != nil {
		return err
	}
	pending := record.Kernel.Pending
	if pending == nil {
		if record.Kernel.Current == nil || record.Kernel.Current.Generation != lease.record.Generation ||
			record.Kernel.Current.Cache != lease.record.Cache || record.Kernel.Current.Backing != lease.record.Backing ||
			record.Kernel.Current.Phase != "ready" {
			return managedVMResourceValidationError("kernel backing differs from committed generation")
		}
		return deps.syncLogicalAuthority(ctx, lease.logicalFD)
	}
	if pending.Generation != lease.kernelGeneration || pending.Cache != lease.record.Cache ||
		lease.record.Generation != pending.Generation || lease.record.Phase != "ready" ||
		!sameProtectedNode(pending.Directory, lease.record.Directory) {
		return managedVMResourceValidationError("kernel publication differs from pending generation")
	}
	digest, err := hashProtectedKernel(ctx, lease.fd, lease.record.Backing)
	if err != nil {
		return err
	}
	if current := record.Kernel.Current; current != nil && digest != current.Digest {
		return managedVMResourceValidationError("kernel successor bytes differ from retired immutable bytes")
	}
	// The per-cache ready record may be visible following a failed parent fsync. A retry
	// skips publishImport, so it must seal that exact record before making the pointer current.
	if err := deps.syncBackingMetadata(ctx, lease.metadataFD); err != nil {
		return err
	}
	record.Kernel.Current = &protectedKernelIncarnation{Generation: pending.Generation, Cache: pending.Cache,
		Store: pending.Store, Backing: lease.record.Backing, Digest: digest, Phase: "ready"}
	record.Kernel.Pending = nil
	record.Initialized = true
	return writeProtectedRecord(ctx, lease.logicalFD, "logical.json", record)
}

func (lease *protectedBackingLease) commitKernelRetirement(ctx context.Context) error {
	if lease.record.Kind != protectedBackingKernel || lease.kernelGeneration == 0 {
		return nil
	}
	record, err := readProtectedKernelLogical(ctx, lease.logicalFD, lease.ownerUID, lease.record.ID)
	if err != nil {
		return err
	}
	current := record.Kernel.Current
	if record.Kernel.Pending != nil || current == nil || current.Generation != lease.record.Generation ||
		current.Cache != lease.record.Cache || current.Backing != lease.record.Backing ||
		current.Digest != lease.record.KernelDigest || lease.record.Phase != "removed" ||
		len(lease.record.References) != 0 || len(lease.record.NextReferences) != 0 {
		return managedVMResourceValidationError("kernel retirement differs from committed generation")
	}
	terminal := *current
	terminal.Phase = "removed"
	if err := retainProtectedKernelReceipt(ctx, lease.logicalFD, protectedKernelReceipt{
		Version: protectedKernelLogicalVersion, Owner: lease.ownerUID, ID: lease.record.ID, Binding: terminal,
	}); err != nil {
		return err
	}
	record.Kernel.Current = &terminal
	return writeProtectedRecord(ctx, lease.logicalFD, "logical.json", record)
}
