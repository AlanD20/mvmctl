package jailer

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

type protectedCacheDirectory struct {
	Parent   string                   `json:"parent"`
	Name     string                   `json:"name"`
	Identity protectedBackingIdentity `json:"identity"`
}

type protectedCacheRetirement struct {
	Phase       string                    `json:"phase"`
	Directories []protectedCacheDirectory `json:"directories"`
	Quarantine  *protectedBackingIdentity `json:"quarantine,omitempty"`
}

// The never-unlinked gate lives outside the selected cache. Shared holders may retain several
// backing and branch leases; retirement alone takes exclusive authority. See ADR-0022.
func acquireProtectedStoreUse(ctx context.Context, anchor int, exclusive bool) (int, error) {
	fd, err := openProtectedFile(ctx, anchor, "store-use.lock", unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Size != 0 || stat.Mode != unix.S_IFREG|0600 {
		return -1, errors.Join(managedVMResourceAtomicError("inspect protected store-use lock", err), unix.Close(fd))
	}
	mode := unix.LOCK_SH | unix.LOCK_NB
	if exclusive {
		mode = unix.LOCK_EX | unix.LOCK_NB
	}
	if err := unix.Flock(fd, mode); err != nil {
		return -1, errors.Join(instanceLockContextError("protected store use", err), unix.Close(fd))
	}
	return fd, nil
}

func cleanProtectedCache(ctx context.Context, cache *managedCacheLease) (returnErr error) {
	if err := validateActiveManagedCacheLease(cache); err != nil {
		return err
	}
	anchor, retained, err := openProtectedAnchor(ctx, cache.ownerUID, cache.identity, false)
	if errors.Is(err, unix.ENOENT) {
		return requireUnprotectedCache(ctx, cache)
	}
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, closeProtectedFDs(retained)) }()
	// The retained owner directory supplies the permanent global logical-index namespace.
	return retireProtectedCache(ctx, cache, anchor, retained[len(retained)-2])
}

func requireUnprotectedCache(ctx context.Context, cache *managedCacheLease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var stat unix.Stat_t
	err := unix.Fstatat(cache.cacheFD, protectedBackingDirectory, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return protectedCacheNameAbsent(ctx, cache.cacheFD, ProtectedCacheQuarantineDirectory)
	}
	return managedVMResourceAtomicError("unanchored protected cache cannot be cleaned", err)
}

type protectedCacheCleanupPlan struct {
	cache       *managedCacheLease
	anchor      int
	store       int
	retained    []int
	directories []protectedCacheDirectory
	opened      map[string]int
	record      protectedBackingAnchor
	quarantine  int
	completed   bool
	deps        protectedCacheCaptureDeps
}

// retireProtectedCache consumes admitted descriptors, not caller-selected deletion paths.
// All admission precedes durable intent and every unlink; no recursive root deletion is allowed.
func retireProtectedCache(ctx context.Context, cache *managedCacheLease, anchor, owner int) (returnErr error) {
	return retireProtectedCacheWithCapture(ctx, cache, anchor, owner, realProtectedCacheCaptureDeps())
}

func retireProtectedCacheWithCapture(ctx context.Context, cache *managedCacheLease, anchor, owner int,
	deps protectedCacheCaptureDeps) (returnErr error) {
	var record protectedBackingAnchor
	if err := readProtectedRecord(ctx, anchor, "store.json", &record); errors.Is(err, unix.ENOENT) {
		return requireUnprotectedCache(ctx, cache)
	} else if err != nil {
		return err
	}
	use, err := acquireProtectedStoreUse(ctx, anchor, true)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(use)) }()
	// Initialization remains independently serialized. All locks are nonblocking, including
	// pending users holding a logical lock before they can acquire shared store-use authority.
	lock, err := acquireProtectedLock(ctx, anchor)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(lock)) }()
	if err := readProtectedRecord(ctx, anchor, "store.json", &record); err != nil {
		return err
	}
	if err := requireProtectedCleanupCache(ctx, cache, record); err != nil {
		return err
	}
	if err := validateProtectedStoreLayout(ctx, anchor, record); err != nil {
		return err
	}
	plan := &protectedCacheCleanupPlan{
		cache:      cache,
		anchor:     anchor,
		store:      -1,
		quarantine: -1,
		record:     record,
		deps:       deps,
		opened:     make(map[string]int),
	}
	defer func() { returnErr = errors.Join(returnErr, closeProtectedFDs(plan.retained)) }()
	if err := plan.preflight(ctx, owner); err != nil {
		return err
	}
	if plan.completed {
		return nil
	}
	if record.Retirement != nil && record.Retirement.Phase != "removed" {
		// Record publication can rename successfully but fail its parent fsync. The file
		// was synced before rename; seal this visible intent before ANY resumed effect.
		if err := plan.deps.syncRetirementIntent(ctx, anchor); err != nil {
			return err
		}
	}
	if err := plan.capture(ctx); err != nil {
		return err
	}
	if err := plan.removeParents(ctx); err != nil {
		return err
	}
	plan.record.Retirement.Phase = "removed"
	return writeProtectedRecord(ctx, anchor, "store.json", plan.record)
}

func requireProtectedCleanupCache(ctx context.Context, cache *managedCacheLease, record protectedBackingAnchor) error {
	if err := validateActiveManagedCacheLease(cache); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(cache.cacheFD, &stat); err != nil {
		return err
	}
	if record.Version != protectedStoreLayoutVersion ||
		record.SnapshotLocks.Inode == 0 ||
		record.Owner != cache.ownerUID ||
		record.OwnerGID != cache.ownerGID ||
		record.Cache != protectedCacheIdentity(cache.identity) ||
		record.Locator != protectedLocator(cache) ||
		record.Store.Inode == 0 ||
		stat.Uid != cache.ownerUID ||
		stat.Gid != cache.ownerGID ||
		stat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		stat.Mode&07022 != 0 ||
		uint64(stat.Dev) != unix.Mkdev(record.Cache.Major, record.Cache.Minor) ||
		stat.Ino != record.Cache.Inode {
		return managedVMResourceValidationError("protected cleanup cache or anchor identity changed")
	}
	if record.Retirement != nil {
		switch record.Retirement.Phase {
		case "preparing", "promoting", "capturing", "removing", "removed":
		default:
			return managedVMResourceValidationError("protected cleanup retirement phase is invalid")
		}
		if record.Retirement.Quarantine == nil && record.Retirement.Phase != "preparing" ||
			record.Retirement.Quarantine != nil && record.Retirement.Quarantine.Inode == 0 {
			return managedVMResourceValidationError("protected cleanup quarantine authority is invalid")
		}
		seen := make(map[string]bool)
		for _, directory := range record.Retirement.Directories {
			key := protectedCleanupKey(directory.Parent, directory.Name)
			if !validProtectedCleanupDirectory(directory) || seen[key] {
				return managedVMResourceValidationError("protected cleanup retirement inventory is invalid")
			}
			seen[key] = true
		}
	}
	return nil
}

func protectedCleanupKey(parent, name string) string { return parent + "/" + name }

func validProtectedCleanupDirectory(directory protectedCacheDirectory) bool {
	if directory.Identity.Inode == 0 {
		return false
	}
	if directory.Parent == "" {
		return protectedKindIDLength(directory.Name) != 0 || directory.Name == "snapshots" ||
			directory.Name == "volume-removals"
	}
	if directory.Parent == "volume-removals" {
		return isCanonicalManagedResourceID(directory.Name, 64)
	}
	return isCanonicalManagedResourceID(directory.Name, protectedKindIDLength(directory.Parent)) &&
		protectedKindIDLength(directory.Parent) != 0
}

func protectedCleanupNames(ctx context.Context, fd int) (_ []string, returnErr error) {
	reader, err := unix.Openat2(fd, ".", &unix.OpenHow{
		Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: protectedBackingResolve,
	})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(reader), "protected-cleanup-inventory")
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	var all []string
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		names, err := file.Readdirnames(128)
		all = append(all, names...)
		if errors.Is(err, io.EOF) {
			slices.Sort(all)
			return all, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func (plan *protectedCacheCleanupPlan) preflight(ctx context.Context, owner int) error {
	if err := plan.preflightSnapshotLockAuthority(ctx); err != nil {
		return err
	}
	if err := plan.locate(ctx); err != nil {
		return err
	}
	if plan.completed {
		return nil
	}
	if plan.store >= 0 {
		names, err := protectedCleanupNames(ctx, plan.store)
		if err != nil {
			return err
		}
		for _, name := range names {
			if name == protectedCacheQuarantineChild && plan.record.Retirement != nil &&
				(plan.record.Retirement.Phase == "preparing" || plan.record.Retirement.Phase == "promoting") {
				if err := plan.admitPreparingQuarantine(ctx); err != nil {
					return err
				}
				continue
			}
			if protectedKindIDLength(name) == 0 && name != "snapshots" && name != "volume-removals" {
				return managedVMResourceValidationError("protected cleanup store contains an unknown category")
			}
			if err := plan.openDirectory(ctx, plan.store, "", name); err != nil {
				return err
			}
		}
	}
	for _, branch := range []string{"snapshots"} {
		if fd, exists := plan.opened[protectedCleanupKey("", branch)]; exists {
			names, err := protectedCleanupNames(ctx, fd)
			if err != nil {
				return err
			}
			if len(names) != 0 {
				return managedVMResourceValidationError("protected cleanup branch retains entries or permanent locks")
			}
		}
	}
	for _, kind := range []string{protectedBackingKernel, protectedBackingRootfs,
		protectedBackingVolume, protectedBackingCloudInit} {
		if err := plan.preflightKind(ctx, owner, kind); err != nil {
			return err
		}
	}
	return nil
}

func (plan *protectedCacheCleanupPlan) openDirectory(ctx context.Context, parent int, kind, name string) error {
	fd, err := openProtectedDirectory(ctx, parent, name, false, true)
	if err != nil {
		return err
	}
	plan.retained = append(plan.retained, fd)
	identity, err := protectedIdentity(fd)
	if err != nil {
		return err
	}
	directory := protectedCacheDirectory{Parent: kind, Name: name, Identity: identity}
	key := protectedCleanupKey(kind, name)
	if plan.record.Retirement != nil && !plan.recordedDirectory(directory) {
		return managedVMResourceValidationError("protected cleanup directory differs from retirement intent")
	}
	plan.opened[key] = fd
	plan.directories = append(plan.directories, directory)
	return nil
}

func (plan *protectedCacheCleanupPlan) recordedDirectory(directory protectedCacheDirectory) bool {
	if plan.record.Retirement == nil {
		return false
	}
	for _, recorded := range plan.record.Retirement.Directories {
		if recorded.Parent == directory.Parent && recorded.Name == directory.Name &&
			sameProtectedNode(recorded.Identity, directory.Identity) {
			return true
		}
	}
	return false
}

func (plan *protectedCacheCleanupPlan) preflightKind(ctx context.Context, owner int, kind string) error {
	metadataKind, err := openProtectedDirectory(ctx, plan.anchor, kind, false, true)
	if errors.Is(err, unix.ENOENT) {
		if _, exists := plan.opened[protectedCleanupKey("", kind)]; exists {
			return managedVMResourceValidationError("protected cleanup data category has no authority")
		}
		if kind == protectedBackingVolume {
			return plan.preflightVolumeQuarantines(ctx, map[string]protectedBackingIdentity{})
		}
		return nil
	}
	if err != nil {
		return err
	}
	plan.retained = append(plan.retained, metadataKind)
	ids, err := protectedCleanupNames(ctx, metadataKind)
	if err != nil {
		return err
	}
	expected := make(map[string]protectedBackingIdentity)
	expectedQuarantines := make(map[string]protectedBackingIdentity)
	for _, id := range ids {
		if !isCanonicalManagedResourceID(id, protectedKindIDLength(kind)) {
			return managedVMResourceValidationError("protected cleanup metadata contains an unknown object")
		}
		metadata, err := openProtectedDirectory(ctx, metadataKind, id, false, true)
		if err != nil {
			return err
		}
		plan.retained = append(plan.retained, metadata)
		logical := owner
		for _, name := range []string{"logical", kind, id} {
			logical, err = openProtectedDirectory(ctx, logical, name, false, true)
			if err != nil {
				return err
			}
			plan.retained = append(plan.retained, logical)
		}
		for _, directory := range []int{logical, metadata} {
			lock, err := acquireProtectedLockMode(ctx, directory, false)
			if err != nil {
				return err
			}
			plan.retained = append(plan.retained, lock)
		}
		var index protectedLogicalRecord
		if err := readProtectedRecord(ctx, logical, "logical.json", &index); err != nil {
			return err
		}
		if index.Version != 1 || index.Owner != plan.cache.ownerUID || index.Kind != kind || index.ID != id ||
			index.Cache != plan.record.Cache || !index.Initialized {
			return managedVMResourceValidationError("protected cleanup logical authority conflicts")
		}
		var record protectedBackingRecord
		if err := readProtectedRecord(ctx, metadata, "record.json", &record); err != nil {
			return err
		}
		if record.Version != 1 || record.Owner != plan.cache.ownerUID || record.Kind != kind || record.ID != id ||
			record.Cache != plan.record.Cache || len(record.References) != 0 || len(record.NextReferences) != 0 ||
			(record.Phase != "removed" && (kind != protectedBackingKernel || record.Phase != "kernel-unprotected-removed")) {
			return managedVMResourceValidationError("protected cleanup object is not authoritatively retired")
		}
		if err := validateInitialVolumeRemoval(record); err != nil {
			return err
		}
		if kind == protectedBackingVolume {
			if !record.SnapshotSource && record.InitialRemoval == nil {
				return managedVMResourceValidationError("protected cleanup volume source removal is incomplete")
			}
			if record.InitialRemoval != nil {
				expectedQuarantines[id] = record.InitialRemoval.Quarantine
				if err := unix.Fsync(metadata); err != nil {
					return err
				}
			}
		}
		// These are root's authoritative grants, not a public database projection. Empty snapshot
		// branches above additionally prove that no snapshot authority retains any backing.
		leaves, err := protectedCleanupNames(ctx, metadata)
		if err != nil {
			return err
		}
		if strings.Join(leaves, ",") != "lock,record.json" {
			return managedVMResourceValidationError("protected cleanup metadata retains unexpected entries")
		}
		if record.Directory.Inode != 0 {
			expected[id] = record.Directory
		}
	}
	if data, exists := plan.opened[protectedCleanupKey("", kind)]; exists {
		ids, err := protectedCleanupNames(ctx, data)
		if err != nil {
			return err
		}
		for _, id := range ids {
			identity, exists := expected[id]
			if !exists {
				return managedVMResourceValidationError("protected cleanup data object has no retired authority")
			}
			if err := plan.openDirectory(ctx, data, kind, id); err != nil {
				return err
			}
			fd := plan.opened[protectedCleanupKey(kind, id)]
			actual, err := protectedIdentity(fd)
			if err != nil || !sameProtectedNode(identity, actual) {
				return managedVMResourceAtomicError("protected cleanup object directory changed", err)
			}
			leaves, err := protectedCleanupNames(ctx, fd)
			if err != nil {
				return err
			}
			if len(leaves) != 0 {
				return managedVMResourceValidationError("protected cleanup object directory is not empty")
			}
			delete(expected, id)
		}
	}
	for id, identity := range expected {
		if !plan.recordedDirectory(protectedCacheDirectory{Parent: kind, Name: id, Identity: identity}) {
			return managedVMResourceValidationError("protected cleanup recorded object directory is missing")
		}
	}
	if kind == protectedBackingVolume {
		return plan.preflightVolumeQuarantines(ctx, expectedQuarantines)
	}
	return nil
}

func (plan *protectedCacheCleanupPlan) preflightSnapshotLockAuthority(ctx context.Context) error {
	authority, err := pinProtectedSnapshotLockAuthority(ctx, plan.anchor, plan.record)
	if err != nil {
		return err
	}
	plan.retained = append(plan.retained, authority)
	names, err := protectedCleanupNames(ctx, authority)
	if err != nil {
		return err
	}
	for _, name := range names {
		id, canonical := strings.CutSuffix(name, ".lock")
		if !canonical || !isCanonicalManagedResourceID(id, 64) {
			return managedVMResourceValidationError("protected snapshot lock inventory contains an unknown entry")
		}
		fd, err := openProtectedFile(ctx, authority, name, unix.O_RDWR)
		if err != nil {
			return err
		}
		plan.retained = append(plan.retained, fd)
		var stat, named unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return err
		}
		if stat.Size != 0 || stat.Mode != unix.S_IFREG|0600 {
			return managedVMResourceValidationError("protected snapshot lock metadata changed")
		}
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
			return managedVMResourceAtomicError("snapshot retains an active operation", err)
		}
		if err := unix.Fstatat(authority, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if stat.Dev != named.Dev || stat.Ino != named.Ino || named.Uid != 0 || named.Gid != 0 ||
			named.Mode != unix.S_IFREG|0600 || named.Nlink != 1 || named.Size != 0 {
			return managedVMResourceValidationError("protected snapshot lock identity changed")
		}
	}
	return nil
}

func (plan *protectedCacheCleanupPlan) preflightVolumeQuarantines(
	ctx context.Context,
	expected map[string]protectedBackingIdentity,
) error {
	if branch, exists := plan.opened[protectedCleanupKey("", "volume-removals")]; exists {
		ids, err := protectedCleanupNames(ctx, branch)
		if err != nil {
			return err
		}
		for _, id := range ids {
			identity, exists := expected[id]
			if !exists || !isCanonicalManagedResourceID(id, 64) {
				return managedVMResourceValidationError("volume quarantine has no terminal source-removal authority")
			}
			if err := plan.openDirectory(ctx, branch, "volume-removals", id); err != nil {
				return err
			}
			fd := plan.opened[protectedCleanupKey("volume-removals", id)]
			actual, err := protectedIdentity(fd)
			if err != nil || !sameProtectedNode(identity, actual) {
				return managedVMResourceAtomicError("volume quarantine identity changed", err)
			}
			leaves, err := protectedCleanupNames(ctx, fd)
			if err != nil {
				return err
			}
			if len(leaves) != 0 {
				return managedVMResourceValidationError("volume quarantine is not empty")
			}
			delete(expected, id)
		}
	}
	for id, identity := range expected {
		if !plan.recordedDirectory(protectedCacheDirectory{Parent: "volume-removals", Name: id, Identity: identity}) {
			return managedVMResourceValidationError("recorded terminal volume quarantine is missing")
		}
	}
	return nil
}

func (plan *protectedCacheCleanupPlan) removeParents(ctx context.Context) error {
	// Child directories precede their category parents. Retry removes only remaining admitted
	// entries from the immutable intent; AT_REMOVEDIR itself refuses any unexpected new leaf.
	for _, children := range []bool{true, false} {
		for _, directory := range plan.directories {
			if (directory.Parent != "") != children {
				continue
			}
			parent := plan.store
			if children {
				parent = plan.opened[protectedCleanupKey("", directory.Parent)]
			}
			if err := requireProtectedCleanupCache(ctx, plan.cache, plan.record); err != nil {
				return err
			}
			if err := removeProtectedEmptyDirectory(ctx, parent, directory.Name, directory.Identity); err != nil {
				return err
			}
		}
	}
	if plan.store >= 0 {
		if err := removeProtectedEmptyDirectory(
			ctx, plan.quarantine, protectedCacheCapturedStore, plan.record.Store,
		); err != nil {
			return err
		}
	}
	if err := plan.requireCapturedNamespace(ctx); err != nil {
		return err
	}
	// Absence after an interrupted unlink is not durable until BOTH namespace parents sync.
	return errors.Join(
		syncProtectedCacheDirectory(ctx, plan.quarantine),
		plan.deps.syncCacheParent(ctx, plan.cache.cacheFD),
	)
}

func removeProtectedEmptyDirectory(
	ctx context.Context,
	parent int,
	name string,
	identity protectedBackingIdentity,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var named unix.Stat_t
	if err := unix.Fstatat(parent, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if uint64(named.Dev) != identity.Device || named.Ino != identity.Inode || named.Uid != 0 || named.Gid != 0 ||
		named.Mode != unix.S_IFDIR|0700 {
		return managedVMResourceValidationError("protected cleanup directory identity changed before removal")
	}
	if err := unix.Unlinkat(parent, name, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	reader, err := unix.Openat2(parent, ".", &unix.OpenHow{
		Flags: unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: protectedBackingResolve,
	})
	if err != nil {
		return err
	}
	return errors.Join(unix.Fsync(reader), unix.Close(reader))
}
