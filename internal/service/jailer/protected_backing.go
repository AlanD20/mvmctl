package jailer

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	protectedBackingRootfs      = "rootfs"
	protectedBackingVolume      = "volume"
	protectedBackingKernel      = "kernel"
	protectedBackingCloudInit   = "cloud-init"
	protectedBackingDirectory   = ".mvm-protected"
	protectedBackingRecordLimit = 1 << 20
)

type protectedBackingBinding struct {
	kind          string
	logicalID     string
	ownerUID      uint32
	cacheIdentity managedCacheIdentity
	identity      managedVMConfigStat
	bytes         uint64
	generation    uint64
}

type protectedBackingIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Size   int64  `json:"size"`
}

type protectedBackingCache struct {
	Major uint32 `json:"major"`
	Minor uint32 `json:"minor"`
	Inode uint64 `json:"inode"`
}

type protectedBackingAnchor struct {
	Version       uint32                    `json:"version"`
	Owner         uint32                    `json:"owner"`
	OwnerGID      uint32                    `json:"owner_gid"`
	Cache         protectedBackingCache     `json:"cache"`
	Locator       string                    `json:"locator"`
	Store         protectedBackingIdentity  `json:"store"`
	SnapshotLocks protectedBackingIdentity  `json:"snapshot_locks"`
	Retirement    *protectedCacheRetirement `json:"retirement,omitempty"`
}

type protectedBackingReference struct {
	VMID         string `json:"vm_id"`
	ExecutionUID uint32 `json:"execution_uid"`
	Writable     bool   `json:"writable"`
}

type protectedBackingRecord struct {
	Version         uint32                        `json:"version"`
	Kind            string                        `json:"kind"`
	ID              string                        `json:"id"`
	Owner           uint32                        `json:"owner"`
	Cache           protectedBackingCache         `json:"cache"`
	Directory       protectedBackingIdentity      `json:"directory"`
	Source          protectedBackingIdentity      `json:"source"`
	SourceCTimeSec  int64                         `json:"source_ctime_sec,omitempty"`
	SourceCTimeNsec int64                         `json:"source_ctime_nsec,omitempty"`
	Backing         protectedBackingIdentity      `json:"backing"`
	Phase           string                        `json:"phase"`
	GrowBytes       uint64                        `json:"grow_bytes,omitempty"`
	References      []protectedBackingReference   `json:"references"`
	NextReferences  []protectedBackingReference   `json:"next_references,omitempty"`
	SnapshotSource  bool                          `json:"snapshot_source"`
	KernelDigest    string                        `json:"kernel_digest,omitempty"`
	Generation      uint64                        `json:"generation,omitempty"`
	InitialRemoval  *initialVolumeRemovalProgress `json:"initial_removal,omitempty"`
}

// protectedBackingLease retains the exact protected object and its root-owned logical-object lock.
// THREAD-SAFE: mu guards descriptor ownership, the durable record, and close-once state.
type protectedBackingLease struct {
	// kernelGeneration is root-allocated; zero denotes non-lineage backing consumers.
	kernelGeneration uint64
	mu               sync.Mutex
	fd               int
	identity         managedVMConfigStat
	ownerUID         uint32
	cacheIdentity    managedCacheIdentity
	metadataFD       int
	logicalFD        int
	directoryFD      int
	lockFD           int
	retained         []int
	record           protectedBackingRecord
	released         bool
	releaseErr       error
}

func (cache *managedCacheLease) protectVMRootfs(
	ctx context.Context, vmID vmResourceID, expectedBytes uint64,
) (*protectedBackingLease, error) {
	if !isCanonicalManagedResourceID(string(vmID), 32) || expectedBytes < managedVMRootfsMinBytes ||
		expectedBytes > managedVMRootfsMaxBytes {
		return nil, managedVMResourceValidationError("protected rootfs selection is invalid")
	}
	return protectBacking(ctx, cache, protectedBackingRootfs, string(vmID), expectedBytes, true)
}

func (cache *managedCacheLease) protectVMVolume(
	ctx context.Context, spec vmVolumeResourceSpec,
) (*protectedBackingLease, error) {
	if err := validateVMVolumeResourceSpec(spec); err != nil {
		return nil, err
	}
	return protectBacking(ctx, cache, protectedBackingVolume, string(spec.id), spec.expectedBytes, true)
}

func (cache *managedCacheLease) protectVMKernel(
	ctx context.Context,
	kernelID kernelResourceID,
) (*protectedBackingLease, error) {
	return protectBacking(ctx, cache, protectedBackingKernel, string(kernelID), 0, true)
}

func (cache *managedCacheLease) protectVMCloudInit(
	ctx context.Context,
	vmID vmResourceID,
) (*protectedBackingLease, error) {
	return protectBacking(ctx, cache, protectedBackingCloudInit, string(vmID), 0, true)
}

func protectedKindIDLength(kind string) int {
	switch kind {
	case protectedBackingRootfs, protectedBackingCloudInit:
		return 32
	case protectedBackingVolume, protectedBackingKernel:
		return 64
	default:
		return 0
	}
}

func protectedCacheIdentity(identity managedCacheIdentity) protectedBackingCache {
	return protectedBackingCache{Major: identity.deviceMajor, Minor: identity.deviceMinor, Inode: identity.inode}
}

func protectedCacheKey(identity managedCacheIdentity) string {
	return fmt.Sprintf("%d-%d-%d", identity.deviceMajor, identity.deviceMinor, identity.inode)
}

func (lease *protectedBackingLease) Binding(ctx context.Context) (protectedBackingBinding, error) {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := lease.active(ctx); err != nil {
		return protectedBackingBinding{}, err
	}
	return protectedBackingBinding{
		kind: lease.record.Kind, logicalID: lease.record.ID, ownerUID: lease.ownerUID,
		cacheIdentity: lease.cacheIdentity, identity: lease.identity, bytes: uint64(lease.record.Backing.Size),
		generation: lease.record.Generation,
	}, nil
}

func (lease *protectedBackingLease) active(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return managedVMResourceAtomicError("use protected backing", err)
	}
	if lease.released || lease.fd < 0 || lease.lockFD < 0 || lease.record.Phase != "ready" {
		return managedVMResourceValidationError("protected backing lease is not active")
	}
	return nil
}

// RequireExclusiveCapture keeps the object lock held until Release; attach, detach, and other
// captures cannot change the writer set while the capture owner quiesces its exact VM process.
func (lease *protectedBackingLease) RequireExclusiveCapture(ctx context.Context, vmID string) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := lease.active(ctx); err != nil {
		return err
	}
	found := false
	for _, reference := range lease.record.References {
		found = found || reference.VMID == vmID
		if reference.Writable && reference.VMID != vmID {
			return managedVMResourceValidationError("another VM retains writable access to the captured backing")
		}
	}
	if !found {
		return managedVMResourceValidationError("capture VM has no protected backing reference")
	}
	return nil
}

// RequireStoppedCapture is used only after lifecycle authority proves the source is cleaned.
// The retained object lock excludes new writers throughout the stopped disk capture.
func (lease *protectedBackingLease) RequireStoppedCapture(ctx context.Context) error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := lease.active(ctx); err != nil {
		return err
	}
	for _, reference := range lease.record.References {
		if reference.Writable {
			return managedVMResourceValidationError("stopped capture backing retains a writable VM grant")
		}
	}
	return nil
}

func (lease *protectedBackingLease) Release(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released {
		return lease.releaseErr
	}
	for index := len(lease.retained) - 1; index >= 0; index-- {
		lease.releaseErr = errors.Join(lease.releaseErr, unix.Close(lease.retained[index]))
	}
	lease.retained = nil
	lease.fd = -1
	lease.lockFD = -1
	lease.released = true
	if lease.releaseErr != nil {
		lease.releaseErr = managedVMResourceCleanupError("release protected backing", lease.releaseErr)
	}
	return lease.releaseErr
}

// reopenProtectedBacking accepts only the binding retained in root's lifecycle record. It never
// imports an operator file and never initializes a missing store or object.
func reopenProtectedBacking(
	ctx context.Context,
	binding protectedBackingBinding,
) (_ *protectedBackingLease, returnErr error) {
	if binding.ownerUID == 0 || binding.bytes == 0 || binding.bytes > math.MaxInt64 {
		return nil, managedVMResourceValidationError("protected backing binding is incomplete")
	}
	anchorFD, fds, err := openProtectedAnchor(ctx, binding.ownerUID, binding.cacheIdentity, false)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, closeProtectedFDs(fds)) }()
	var anchor protectedBackingAnchor
	if err := readProtectedRecord(ctx, anchorFD, "store.json", &anchor); err != nil {
		return nil, err
	}
	if err := validateProtectedStoreLayout(ctx, anchorFD, anchor); err != nil {
		return nil, err
	}
	if anchor.Owner != binding.ownerUID || anchor.Cache != protectedCacheIdentity(binding.cacheIdentity) {
		return nil, managedVMResourceValidationError("protected store anchor does not match lifecycle authority")
	}
	locator, err := parseManagedCacheLocator(anchor.Locator)
	if err != nil {
		return nil, err
	}
	cache, err := pinManagedCache(ctx, realManagedCacheDeps(), productionManagedCachePolicy(),
		instanceCaller{uid: binding.ownerUID, gid: anchor.OwnerGID}, locator)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, cache.Release(context.WithoutCancel(ctx))) }()
	if protectedCacheIdentity(cache.identity) != anchor.Cache {
		return nil, managedVMResourceValidationError("protected backing cache identity changed")
	}
	lease, err := protectBacking(ctx, cache, binding.kind, binding.logicalID, binding.bytes, false)
	if err != nil {
		return nil, err
	}
	if err := lease.requireBinding(binding); err != nil {
		return nil, errors.Join(
			err,
			lease.Release(context.WithoutCancel(ctx)),
		)
	}
	return lease, nil
}

func (lease *protectedBackingLease) requireBinding(binding protectedBackingBinding) error {
	if lease.record.Generation != binding.generation || lease.identity.deviceMajor != binding.identity.deviceMajor ||
		lease.identity.deviceMinor != binding.identity.deviceMinor || lease.identity.inode != binding.identity.inode ||
		lease.identity.size != binding.bytes {
		return managedVMResourceValidationError(
			"protected backing inode or generation differs from lifecycle authority",
		)
	}
	return nil
}

func protectedLocator(cache *managedCacheLease) string {
	return "/" + strings.Join(cache.locator.components, "/")
}

func openProtectedAnchor(
	ctx context.Context, ownerUID uint32, cacheIdentity managedCacheIdentity, create bool,
) (_ int, _ []int, returnErr error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, nil, managedVMResourceAtomicError("open protected backing authority root", err)
	}
	fds := []int{fd}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, closeProtectedFDs(fds))
		}
	}()
	for _, component := range []string{"var", "lib", "mvmctl"} {
		fd, err = openProtectedDirectory(ctx, fd, component, false, false)
		if err != nil {
			return -1, nil, err
		}
		fds = append(fds, fd)
	}
	for _, component := range []string{"protected-backing", strconv.FormatUint(uint64(ownerUID), 10), protectedCacheKey(cacheIdentity)} {
		fd, err = openProtectedDirectory(ctx, fd, component, create, true)
		if err != nil {
			return -1, nil, err
		}
		fds = append(fds, fd)
	}
	return fd, fds, nil
}
