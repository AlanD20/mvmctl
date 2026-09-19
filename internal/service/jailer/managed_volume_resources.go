package jailer

import (
	"context"
	"math"
	"slices"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/model"
)

type vmVolumeResourceID string

type vmVolumeAccess uint8

const (
	vmVolumeReadOnly vmVolumeAccess = iota + 1
	vmVolumeReadWrite
)

// vmVolumeResourceSpec has no caller-selected basename or representation: data volumes are raw.
type vmVolumeResourceSpec struct {
	id            vmVolumeResourceID
	expectedBytes uint64
	access        vmVolumeAccess
	cacheType     string
}

type pinnedVMVolumeResource struct {
	spec     vmVolumeResourceSpec
	fd       int
	identity managedVMConfigStat
}

// vmVolumeResourceLease pins resource identity only; it grants no mount or storage-allocation authority.
// THREAD-SAFE: Copies share descriptor ownership and the first checked-close result.
type vmVolumeResourceLease struct {
	state *vmVolumeResourceState
}

type vmVolumeResourceState struct {
	// mu guards retained descriptors and close-once state across copied leases.
	mu            sync.Mutex
	deps          managedCacheDeps
	cacheIdentity managedCacheIdentity
	ownerUID      uint32
	volumes       []pinnedVMVolumeResource
	retained      []int
	released      bool
	releaseErr    error
}

func newVMVolumeResourceSpec(
	id string,
	format model.VolumeFormat,
	expectedBytes uint64,
	access vmVolumeAccess,
	cacheType string,
) (vmVolumeResourceSpec, error) {
	if format != model.VolumeFormatRaw {
		return vmVolumeResourceSpec{}, managedVMResourceValidationError("managed data volumes must use raw format")
	}
	spec := vmVolumeResourceSpec{
		id: vmVolumeResourceID(id), expectedBytes: expectedBytes, access: access, cacheType: cacheType,
	}
	if err := validateVMVolumeResourceSpec(spec); err != nil {
		return vmVolumeResourceSpec{}, err
	}
	return spec, nil
}

func validateVMVolumeResourceSpec(spec vmVolumeResourceSpec) error {
	if !isCanonicalManagedResourceID(string(spec.id), 64) {
		return managedVMResourceValidationError("managed volume ID must be 64 lowercase hexadecimal characters")
	}
	// This is file-offset representability, not an approved product capacity ceiling.
	if spec.expectedBytes == 0 || spec.expectedBytes > math.MaxInt64 {
		return managedVMResourceValidationError("managed raw volume requires a positive representable logical size")
	}
	switch spec.access {
	case vmVolumeReadOnly, vmVolumeReadWrite:
	default:
		return managedVMResourceValidationError("managed volume access selection is invalid")
	}
	if spec.cacheType != model.CacheTypeUnsafe && spec.cacheType != model.CacheTypeWriteback {
		return managedVMResourceValidationError("managed volume cache type is invalid")
	}
	return nil
}

func validateVMVolumeResourceSelection(volumes []vmVolumeResourceSpec) error {
	seen := make(map[vmVolumeResourceID]struct{}, len(volumes))
	for _, volume := range volumes {
		if err := validateVMVolumeResourceSpec(volume); err != nil {
			return err
		}
		if _, exists := seen[volume.id]; exists {
			return managedVMResourceValidationError("managed volume selection contains a duplicate identity")
		}
		seen[volume.id] = struct{}{}
	}
	return nil
}

func (spec vmVolumeResourceSpec) basename() string {
	return string(spec.id) + ".raw"
}

func (spec vmVolumeResourceSpec) jailPath() string {
	return "/volumes/" + spec.basename()
}

// pinVMVolumeResources consumes the cache lease only after successful descriptor admission.
// CRITICAL: No production launch or live attachment consumes this identity-only lease while per-volume
// and aggregate storage policy is unresolved. O_PATH admission neither reads nor copies volume contents.
func (cache *managedCacheLease) pinVMVolumeResources(
	ctx context.Context,
	intent vmFirecrackerLaunchIntent,
) (_ *vmVolumeResourceLease, returnErr error) {
	if err := validateActiveManagedCacheLease(cache); err != nil {
		return nil, err
	}
	if err := validateVMFirecrackerLaunchIntent(intent); err != nil {
		return nil, err
	}
	if len(intent.volumes) == 0 {
		return nil, managedVMResourceValidationError("managed volume selection is empty")
	}
	volumes := slices.Clone(intent.volumes)
	opened := make([]int, 0, len(volumes)+1)
	defer func() {
		if returnErr != nil {
			returnErr = appendManagedVMResourceCleanupError(
				returnErr, "close rejected managed volumes", closeManagedCacheFDs(ctx, cache.deps, opened),
			)
		}
	}()
	storeFD, err := openManagedVMResourceDirectory(
		ctx, cache.deps, cache.cacheFD, "volumes", "volume store", cache.ownerUID,
	)
	if err != nil {
		return nil, err
	}
	opened = append(opened, storeFD)
	pinned := make([]pinnedVMVolumeResource, 0, len(volumes))
	for _, volume := range volumes {
		mode := uint16(0400)
		if volume.access == vmVolumeReadWrite {
			mode = 0600
		}
		fd, openErr := openManagedVMResourceFile(
			ctx, cache.deps, storeFD, volume.basename(), managedVMRegularFilePolicy{
				name: "raw volume", minimumBytes: volume.expectedBytes, maximumBytes: volume.expectedBytes,
				exactBytes: volume.expectedBytes, requiredOwnerMode: mode,
			}, cache.ownerUID,
		)
		if openErr != nil {
			return nil, openErr
		}
		opened = append(opened, fd)
		identity, statErr := stableManagedVMConfigStat(ctx, cache.configDeps, fd)
		if statErr != nil {
			return nil, statErr
		}
		if identity.size != volume.expectedBytes || identity.uid != cache.ownerUID || identity.nlink != 1 ||
			identity.mode&unix.S_IFMT != unix.S_IFREG || identity.mode&mode != mode || identity.mode&07133 != 0 {
			return nil, managedVMResourceValidationError("managed raw volume metadata changed during admission")
		}
		after, statErr := stableManagedVMConfigStat(ctx, cache.configDeps, fd)
		if statErr != nil {
			return nil, statErr
		}
		if identity != after {
			return nil, managedVMResourceValidationError("managed raw volume metadata changed during admission")
		}
		pinned = append(pinned, pinnedVMVolumeResource{spec: volume, fd: fd, identity: identity})
	}
	retained := append(slices.Clone(cache.retained), opened...)
	lease := &vmVolumeResourceLease{state: &vmVolumeResourceState{
		deps: cache.deps, cacheIdentity: cache.identity, ownerUID: cache.ownerUID,
		volumes: pinned, retained: retained,
	}}
	cache.retained = nil
	cache.cacheFD = -1
	cache.ownerUID = 0
	cache.identity = managedCacheIdentity{}
	cache.configDeps = managedFirecrackerConfigDeps{}
	return lease, nil
}

func (lease *vmVolumeResourceLease) Release(ctx context.Context) error {
	if lease == nil {
		return nil
	}
	if lease.state == nil {
		return managedVMResourceAtomicError("managed volume lease is not active", nil)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	cleanupCtx := context.Background()
	if ctx == nil {
		state.releaseErr = managedVMResourceAtomicError("managed volume cleanup context is required", nil)
	} else {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	state.releaseErr = appendManagedVMResourceCleanupError(
		state.releaseErr, "release managed volume descriptors",
		closeManagedCacheFDs(cleanupCtx, state.deps, state.retained),
	)
	state.retained = nil
	state.volumes = nil
	state.ownerUID = 0
	state.cacheIdentity = managedCacheIdentity{}
	state.released = true
	return state.releaseErr
}
