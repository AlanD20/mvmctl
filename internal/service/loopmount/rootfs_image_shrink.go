package loopmount

import (
	"context"
	"encoding/binary"
	"errors"
	"io"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
)

type rootfsImageShrinkAdapter struct {
	deps rootfsVMProvisionDeps
}

type completedImageShrinkCleanup struct{}

func (completedImageShrinkCleanup) CleanupOptimizeImage(context.Context) error { return nil }

// NewImageShrinkServer constructs durable image conversion, cleanup, and offline ext-family shrink authority.
// CRITICAL: This one-request receiver permanently unshares its locked OS thread. Conversion
// has a distinct receipt and publishes only a completed candidate; shrink never authorizes growth.
func NewImageShrinkServer() *Server {
	return &Server{
		rootfsAuthority:    systemRootfsAuthorityDeps(),
		optimizeEffects:    &rootfsImageShrinkAdapter{deps: newSystemRootfsVMProvisionAdapter(nil).deps},
		locks:              newSystemRootfsResourceLocks(),
		durableImageShrink: true,
	}
}

func validateImageShrinkPlan(filesystem Filesystem, plan ImageOptimizePlan) error {
	if plan.ConvertTo != nil {
		if filesystem != FilesystemBtrfs || *plan.ConvertTo != FilesystemExt4 || plan.Shrink ||
			plan.DeblobOS != nil || plan.FixFstab {
			return rootfsValidationError("image conversion requires a separate Btrfs-to-ext4-only request before cleanup or shrink")
		}
		return nil
	}
	if !plan.Shrink && plan.DeblobOS == nil && !plan.FixFstab {
		return rootfsValidationError("managed image receiver requires named cleanup, fstab, or shrink intent")
	}
	if err := filesystem.Validate(); err != nil {
		return err
	}
	if plan.Shrink && filesystem != FilesystemExt2 && filesystem != FilesystemExt3 && filesystem != FilesystemExt4 {
		return rootfsValidationError("offline image shrink requires an ext-family filesystem")
	}
	return nil
}

func (adapter *rootfsImageShrinkAdapter) OptimizeImage(
	ctx context.Context,
	lease *rootfsLease,
	filesystem Filesystem,
	plan ImageOptimizePlan,
) (outcome imageOptimizeEffectOutcome, returnErr error) {
	outcome = imageOptimizeEffectOutcome{
		state: imageOptimizeEffectStateUnchanged, filesystem: filesystem, lease: completedImageShrinkCleanup{},
	}
	if err := validateImageShrinkPlan(filesystem, plan); err != nil {
		return outcome, err
	}
	if plan.ConvertTo != nil {
		return convertRootfsImage(ctx, adapter.deps, lease)
	}
	admittedBytes, err := inspectRootfsProvisionLease(ctx, adapter.deps, lease)
	if err != nil {
		return outcome, err
	}
	transaction := lease.imageShrink
	if transaction == nil || transaction.record == nil {
		return outcome, rootfsAtomicError("image shrink durable authority is missing", nil)
	}
	if transaction.record.Phase == imageShrinkPrepared && (plan.DeblobOS != nil || plan.FixFstab) {
		content, contentErr := (&rootfsVMProvisionAdapter{deps: adapter.deps}).ProvisionVM(ctx, lease, filesystem,
			VMProvisionPlan{DeblobOS: plan.DeblobOS, FixFstab: plan.FixFstab})
		contentErr = appendRootfsCleanupError(contentErr, "release image content effects", content.cleanup(context.WithoutCancel(ctx)))
		switch content.state {
		case vmProvisionEffectStateUnchanged:
			outcome.state = imageOptimizeEffectStateUnchanged
		case vmProvisionEffectStateModified:
			outcome.state = imageOptimizeEffectStateModified
		case vmProvisionEffectStateModifiedDurabilityUncertain:
			outcome.state = imageOptimizeEffectStateModifiedDurabilityUncertain
		default:
			outcome.state = imageOptimizeEffectStateOutcomeUnknown
		}
		if contentErr != nil {
			return outcome, contentErr
		}
		if !content.state.allowsSuccess() {
			return outcome, rootfsAtomicError("managed image content did not produce a durable result", nil)
		}
	}
	if !plan.Shrink {
		if err := finalizeRootfsProvisionBacking(ctx, adapter.deps, lease, admittedBytes); err != nil {
			return outcome, err
		}
		transaction.record.TargetBytes = admittedBytes
		transaction.record.Phase = imageShrinkComplete
		if err := transaction.persist(ctx); err != nil {
			return outcome, err
		}
		outcome.imageBytes = admittedBytes
		return outcome, nil
	}
	if err := adapter.deps.lockOSThread(ctx); err != nil {
		return outcome, rootfsAtomicError("lock image shrink OS thread", err)
	}
	if err := ctx.Err(); err != nil {
		return outcome, appendRootfsCleanupError(rootfsAtomicError("image shrink context ended", err),
			"unlock image shrink OS thread", adapter.deps.unlockOSThread(context.WithoutCancel(ctx)))
	}
	if err := adapter.deps.unshare(ctx, unix.CLONE_NEWNS); err != nil {
		return outcome, appendRootfsCleanupError(rootfsAtomicError("create image shrink mount namespace", err),
			"unlock image shrink OS thread", adapter.deps.unlockOSThread(context.WithoutCancel(ctx)))
	}
	cleanupCtx := context.WithoutCancel(ctx)
	tracker := &rootfsProvisionTracker{
		modified: outcome.state == imageOptimizeEffectStateModified,
		durable:  outcome.state == imageOptimizeEffectStateModified,
	}
	var loop *rootfsLoopDevice
	var partitions *rootfsPartitionSet
	var source *rootfsSelectedSource
	defer func() {
		cleanupErr := errors.Join(source.Release(cleanupCtx), partitions.Release(cleanupCtx), loop.Release(cleanupCtx))
		returnErr = appendRootfsCleanupError(returnErr, "release offline image shrink devices", cleanupErr)
		switch {
		case tracker.outcomeUnknown:
			outcome.state = imageOptimizeEffectStateOutcomeUnknown
		case tracker.modified && (returnErr != nil || !tracker.durable):
			outcome.state = imageOptimizeEffectStateModifiedDurabilityUncertain
		case tracker.modified:
			outcome.state = imageOptimizeEffectStateModified
		}
	}()
	if err := adapter.deps.mount(ctx, "", "/", "", rootfsInspectPrivateFlags, ""); err != nil {
		return outcome, rootfsAtomicError("make image shrink mounts private", err)
	}
	loop, err = adapter.deps.attachLoop(ctx, lease)
	if err != nil {
		return outcome, err
	}
	partitions, err = adapter.deps.discoverPartitions(ctx, loop)
	if err != nil {
		return outcome, err
	}
	source, err = adapter.deps.probeFilesystem(ctx, filesystem, loop, partitions)
	if err != nil {
		return outcome, err
	}
	source.state.mu.Lock()
	raw := source.state.kind == rootfsMountSourceRaw
	source.state.mu.Unlock()
	if !raw {
		return outcome, rootfsValidationError("image shrink requires the extracted raw filesystem, not a partition table")
	}
	sourcePath, _, err := activeRootfsProvisionSource(ctx, adapter.deps, source, filesystem)
	if err != nil {
		return outcome, err
	}
	// A prepared replay retains the original backing length. Never truncate after an
	// interrupted tool until fsck, resize, fsck, and exact superblock admission succeed.
	if err := runImageShrinkTool(ctx, adapter.deps, []string{rootfsE2fsckPath, "-f", "-p", sourcePath}, true, tracker); err != nil {
		return outcome, err
	}
	if transaction.record.Phase == imageShrinkPrepared {
		if err := runImageShrinkTool(ctx, adapter.deps, []string{rootfsResize2fsPath, "-M", sourcePath}, false, tracker); err != nil {
			return outcome, err
		}
		if err := runImageShrinkTool(ctx, adapter.deps, []string{rootfsE2fsckPath, "-f", "-p", sourcePath}, true, tracker); err != nil {
			return outcome, err
		}
	}
	if _, _, err := activeRootfsProvisionSource(ctx, adapter.deps, source, filesystem); err != nil {
		return outcome, err
	}
	target, err := readImageShrinkSize(ctx, adapter.deps, source, admittedBytes)
	if err != nil {
		return outcome, err
	}
	if transaction.record.Phase == imageShrinkPrepared {
		// The filesystem must be durable before its size becomes replay authority.
		if err := adapter.deps.fsync(ctx, source.state.fd); err != nil {
			return outcome, rootfsCleanupError("sync shrunk image filesystem", err)
		}
		transaction.record.TargetBytes = target
		transaction.record.Phase = imageShrinkSized
		if err := transaction.persist(ctx); err != nil {
			return outcome, err
		}
	} else if target != transaction.record.TargetBytes {
		return outcome, rootfsAtomicError("shrunk filesystem no longer matches its durable target", nil)
	}
	// The block device must no longer retain the original capacity when truncating its backing inode.
	if err := errors.Join(source.Release(cleanupCtx), partitions.Release(cleanupCtx), loop.Release(cleanupCtx)); err != nil {
		return outcome, rootfsCleanupError("detach image shrink source before truncation", err)
	}
	if err := finalizeRootfsProvisionBacking(ctx, adapter.deps, lease, admittedBytes); err != nil {
		return outcome, err
	}
	if admittedBytes != target {
		lease.mu.Lock()
		err := adapter.deps.ftruncate(ctx, lease.rootfsFD, int64(target))
		lease.mu.Unlock()
		if err != nil {
			return outcome, rootfsAtomicError("truncate exact admitted image inode to durable shrink target", err)
		}
		tracker.markModified()
	}
	if err := finalizeRootfsProvisionBacking(ctx, adapter.deps, lease, target); err != nil {
		return outcome, err
	}
	transaction.record.Phase = imageShrinkComplete
	if err := transaction.persist(ctx); err != nil {
		return outcome, err
	}
	tracker.durable = true
	outcome.imageBytes = target
	return outcome, nil
}

func runImageShrinkTool(
	ctx context.Context,
	deps rootfsVMProvisionDeps,
	args []string,
	fsck bool,
	tracker *rootfsProvisionTracker,
) error {
	tracker.outcomeUnknown = true
	result, err := deps.run(ctx, args, system.RunCmdOpts{
		Capture: true, Check: false, Cwd: "/", Timeout: rootfsGrowToolTimeout,
		Env: map[string]string{"LANG": "C", "LC_ALL": "C"}, ReplaceEnv: true,
		MaxStdoutBytes: maxRootfsGrowToolStdoutBytes, MaxStderrBytes: maxRootfsGrowToolStderrBytes,
	})
	if err != nil || result == nil || result.ExitCode != 0 && !(fsck && (result.ExitCode == 1 || result.ExitCode == 2)) {
		return rootfsAtomicError("fixed offline image shrink tool did not complete successfully", err)
	}
	tracker.completeMutation()
	return nil
}

func readImageShrinkSize(
	ctx context.Context,
	deps rootfsVMProvisionDeps,
	source *rootfsSelectedSource,
	limit uint64,
) (uint64, error) {
	source.state.mu.Lock()
	defer source.state.mu.Unlock()
	if source.state.released || source.state.kind != rootfsMountSourceRaw {
		return 0, rootfsAtomicError("image shrink superblock authority is inactive", nil)
	}
	var first, second [1024]byte
	for _, buffer := range [][]byte{first[:], second[:]} {
		n, err := deps.pread(ctx, source.state.fd, buffer, 1024)
		if err != nil || n != len(buffer) {
			return 0, rootfsAtomicError("read bounded image shrink superblock", errors.Join(err, io.ErrUnexpectedEOF))
		}
	}
	if first != second || binary.LittleEndian.Uint16(first[0x38:]) != 0xef53 ||
		binary.LittleEndian.Uint16(first[0x3a:])&1 == 0 {
		return 0, rootfsAtomicError("image shrink superblock is unstable or not clean ext", nil)
	}
	logBlockSize := binary.LittleEndian.Uint32(first[0x18:])
	if logBlockSize > 6 {
		return 0, rootfsAtomicError("image shrink block size exceeds ext bounds", nil)
	}
	blockSize := uint64(1024) << logBlockSize
	blocks := uint64(binary.LittleEndian.Uint32(first[0x04:]))
	if binary.LittleEndian.Uint32(first[0x60:])&0x80 != 0 {
		blocks |= uint64(binary.LittleEndian.Uint32(first[0x150:])) << 32
	}
	if blocks == 0 || blocks > limit/blockSize {
		return 0, rootfsAtomicError("image shrink filesystem exceeds admitted backing bounds", nil)
	}
	return blocks * blockSize, nil
}
