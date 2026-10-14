package jailer

import (
	"context"
	"errors"
	"fmt"
	"math"

	"golang.org/x/sys/unix"
)

const protectedKernelLogicalVersion = 2

// The permanent logical lock owns the current pointer and every attempted generation.
// Terminal receipts never move with a cache and are never overwritten or removed.
type protectedKernelLineage struct {
	LastGeneration uint64                      `json:"last_generation"`
	Current        *protectedKernelIncarnation `json:"current,omitempty"`
	Pending        *protectedKernelPending     `json:"pending,omitempty"`
}

type protectedKernelIncarnation struct {
	Generation uint64                   `json:"generation"`
	Cache      protectedBackingCache    `json:"cache"`
	Store      protectedBackingIdentity `json:"store"`
	Backing    protectedBackingIdentity `json:"backing"`
	Digest     string                   `json:"digest"`
	Phase      string                   `json:"phase"`
}

type protectedKernelPending struct {
	Generation  uint64                   `json:"generation"`
	Predecessor uint64                   `json:"predecessor"`
	Cache       protectedBackingCache    `json:"cache"`
	Store       protectedBackingIdentity `json:"store"`
	Directory   protectedBackingIdentity `json:"directory"`
	Candidate   protectedBackingIdentity `json:"candidate"`
	Phase       string                   `json:"phase"`
	// Only the trusted snapshot producer may resume this exact initial source descriptor.
	SnapshotSource *protectedBackingIdentity `json:"snapshot_source,omitempty"`
}

type protectedKernelReceipt struct {
	Version uint32                     `json:"version"`
	Owner   uint32                     `json:"owner"`
	ID      string                     `json:"id"`
	Binding protectedKernelIncarnation `json:"binding"`
}

func validateProtectedLogical(record protectedLogicalRecord, owner uint32, kind, id string) error {
	if record.Owner != owner || record.Kind != kind || record.ID != id || record.Cache.Inode == 0 {
		return managedVMResourceValidationError("protected logical identity conflicts")
	}
	if kind != protectedBackingKernel {
		if record.Version != 1 || record.Kernel != nil {
			return managedVMResourceValidationError("mutable logical authority schema differs")
		}
		return nil
	}
	if record.Version != protectedKernelLogicalVersion || record.Kernel == nil {
		return managedVMResourceValidationError("kernel logical authority requires fresh schema 2")
	}
	lineage := record.Kernel
	if lineage.Current != nil {
		current := lineage.Current
		if !record.Initialized || current.Generation == 0 || current.Generation > lineage.LastGeneration ||
			current.Cache.Inode == 0 || current.Store.Inode == 0 || current.Backing.Inode == 0 ||
			current.Backing.Size <= 0 || !isCanonicalManagedResourceID(current.Digest, 64) ||
			(current.Phase != "ready" && current.Phase != "removed") {
			return managedVMResourceValidationError("kernel committed generation authority is incomplete")
		}
	}
	if lineage.Pending != nil {
		pending := lineage.Pending
		if pending.Generation == 0 || pending.Generation != lineage.LastGeneration || pending.Cache.Inode == 0 ||
			pending.Store.Inode == 0 || (pending.Phase != "preparing" && pending.Phase != "copying" &&
			pending.Phase != "aborting") {
			return managedVMResourceValidationError("kernel pending generation authority is incomplete")
		}
		if lineage.Current == nil {
			if pending.Predecessor != 0 || pending.Cache != record.Cache {
				return managedVMResourceValidationError("initial kernel generation cannot select another cache")
			}
		} else if pending.Predecessor != lineage.Current.Generation ||
			pending.Generation <= lineage.Current.Generation || lineage.Current.Phase != "removed" {
			return managedVMResourceValidationError("kernel successor requires its exact removed predecessor")
		}
		if source := pending.SnapshotSource; source != nil &&
			(lineage.Current != nil || source.Inode == 0 || source.Size <= 0) {
			return managedVMResourceValidationError("snapshot kernel producer requires exact initial source authority")
		}
	}
	return nil
}

func readProtectedKernelLogical(ctx context.Context, fd int, owner uint32, id string) (protectedLogicalRecord, error) {
	var record protectedLogicalRecord
	if err := readProtectedRecord(ctx, fd, "logical.json", &record); err != nil {
		return record, err
	}
	if err := validateProtectedLogical(record, owner, protectedBackingKernel, id); err != nil {
		return record, err
	}
	if record.Kernel.Current == nil && record.Kernel.Pending == nil && record.Kernel.LastGeneration != 0 {
		var receipt protectedKernelReceipt
		name := protectedKernelReceiptLeaf(record.Kernel.LastGeneration)
		if err := readProtectedRecord(ctx, fd, name, &receipt); err != nil {
			return record, err
		}
		if receipt.Version != protectedKernelLogicalVersion || receipt.Owner != owner || receipt.ID != id ||
			receipt.Binding.Generation != record.Kernel.LastGeneration || receipt.Binding.Phase != "aborted" {
			return record, managedVMResourceValidationError("kernel has no committed or aborted authority")
		}
	}
	return record, nil
}

func protectedKernelReceiptLeaf(generation uint64) string {
	return fmt.Sprintf("kernel-generation-%020d.json", generation)
}

// CALLER MUST HOLD: the permanent logical kernel lock. Creation cannot replace a receipt.
func retainProtectedKernelReceipt(ctx context.Context, fd int, receipt protectedKernelReceipt) error {
	if receipt.Version != protectedKernelLogicalVersion || receipt.Owner == 0 ||
		!isCanonicalManagedResourceID(receipt.ID, 64) || receipt.Binding.Generation == 0 ||
		(receipt.Binding.Phase != "removed" && receipt.Binding.Phase != "aborted") {
		return managedVMResourceValidationError("kernel terminal generation receipt is invalid")
	}
	name := protectedKernelReceiptLeaf(receipt.Binding.Generation)
	var retained protectedKernelReceipt
	err := readProtectedRecord(ctx, fd, name, &retained)
	if err == nil {
		if retained != receipt {
			return managedVMResourceValidationError("kernel terminal generation receipt changed")
		}
		return syncProtectedCacheDirectory(ctx, fd)
	}
	if !errors.Is(err, unix.ENOENT) {
		return err
	}
	// Root-private namespace plus the retained logical lock excludes competing publication.
	return writeProtectedRecord(ctx, fd, name, receipt)
}

func nextProtectedKernelGeneration(lineage protectedKernelLineage) (uint64, error) {
	if lineage.Pending != nil || lineage.LastGeneration == math.MaxUint64 {
		return 0, managedVMResourceValidationError("kernel generation is pending or exhausted")
	}
	return lineage.LastGeneration + 1, nil
}

func requireProtectedKernelSelection(ctx context.Context, logicalFD int, owner uint32, id string,
	cache protectedBackingCache, active bool) (protectedLogicalRecord, error) {
	record, err := readProtectedKernelLogical(ctx, logicalFD, owner, id)
	if err != nil {
		return record, err
	}
	if record.Kernel.Pending != nil {
		return record, managedVMResourceValidationError("kernel has an unfinished explicit import")
	}
	if current := record.Kernel.Current; current != nil {
		if current.Cache != cache || (active && current.Phase != "ready") {
			return record, managedVMResourceValidationError("kernel cache or generation is not current")
		}
	} else if active || record.Cache != cache {
		return record, managedVMResourceValidationError("kernel has no current protected generation")
	}
	return record, nil
}

func requireProtectedKernelTerminal(ctx context.Context, logicalFD int, index protectedLogicalRecord,
	backing protectedBackingRecord, store protectedBackingIdentity) error {
	if err := validateProtectedLogical(index, backing.Owner, protectedBackingKernel, backing.ID); err != nil {
		return err
	}
	if !index.Initialized || index.Kernel.Pending != nil && index.Kernel.Pending.Cache == backing.Cache {
		return managedVMResourceValidationError("kernel cleanup retains unfinished import authority")
	}
	if backing.Phase == "kernel-unprotected-removed" {
		if index.Cache != backing.Cache || index.Kernel.Current != nil || index.Kernel.LastGeneration != 0 {
			return managedVMResourceValidationError("unprotected kernel terminal authority conflicts")
		}
		return nil
	}
	var receipt protectedKernelReceipt
	if err := readProtectedRecord(ctx, logicalFD, protectedKernelReceiptLeaf(backing.Generation), &receipt); err != nil {
		return err
	}
	phase := "removed"
	if backing.Phase == "kernel-aborted" {
		phase = "aborted"
	}
	if receipt.Version != protectedKernelLogicalVersion || receipt.Owner != backing.Owner || receipt.ID != backing.ID ||
		receipt.Binding.Generation != backing.Generation || receipt.Binding.Cache != backing.Cache ||
		!sameProtectedNode(receipt.Binding.Store, store) || receipt.Binding.Backing != backing.Backing ||
		receipt.Binding.Phase != phase || phase == "removed" && receipt.Binding.Digest != backing.KernelDigest ||
		phase == "aborted" && receipt.Binding.Digest != "" {
		return managedVMResourceValidationError("kernel cleanup terminal receipt differs from backing authority")
	}
	return syncProtectedCacheDirectory(ctx, logicalFD)
}

// A failed same-cache successor replaces the per-cache projection, not the removed current
// authority. Only both exact terminal receipts permit treating that newer projection as inactive.
func requireProtectedKernelAbortedProjection(ctx context.Context, logicalFD int, logical protectedLogicalRecord,
	backing protectedBackingRecord, store protectedBackingIdentity) error {
	current := logical.Kernel.Current
	if current == nil || current.Phase != "removed" || backing.Phase != "kernel-aborted" ||
		backing.Cache != current.Cache || backing.Generation <= current.Generation ||
		backing.Generation > logical.Kernel.LastGeneration || backing.KernelDigest != current.Digest ||
		!sameProtectedNode(current.Store, store) || len(backing.References) != 0 || len(backing.NextReferences) != 0 {
		return managedVMResourceValidationError("aborted kernel projection differs from removed current authority")
	}
	var removed protectedKernelReceipt
	if err := readProtectedRecord(ctx, logicalFD, protectedKernelReceiptLeaf(current.Generation), &removed); err != nil {
		return err
	}
	want := protectedKernelReceipt{Version: protectedKernelLogicalVersion, Owner: backing.Owner,
		ID: backing.ID, Binding: *current}
	if removed != want {
		return managedVMResourceValidationError("aborted kernel projection has no exact removed predecessor receipt")
	}
	return requireProtectedKernelTerminal(ctx, logicalFD, logical, backing, store)
}
