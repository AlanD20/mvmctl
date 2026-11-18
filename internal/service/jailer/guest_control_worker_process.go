package jailer

import (
	"context"
	"errors"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
)

// GuestControlWorkerProcess is minted only inside the closed trusted spawn boundary. There is no
// exported constructor accepting caller PID/pidfd data. Runtime spawn/gate integration is not active yet.
type GuestControlWorkerProcess struct {
	state *guestControlWorkerProcessState
}

type guestControlWorkerProcessState struct {
	mu         sync.Mutex
	principal  GuestControlPrincipal
	record     guestControlWorkerProcessRecord
	pidfd      int
	released   bool
	releaseErr error
}

type guestControlWorkerProcessRecord struct {
	PID            int                             `json:"pid"`
	StartTimeTicks uint64                          `json:"start_time_ticks"`
	ExpectedUID    uint32                          `json:"expected_uid"`
	ExpectedGID    uint32                          `json:"expected_gid"`
	Executable     system.PinnedExecutableIdentity `json:"executable"`
	MountDevice    uint64                          `json:"mount_device"`
	MountInode     uint64                          `json:"mount_inode"`
	NetworkDevice  uint64                          `json:"network_device"`
	NetworkInode   uint64                          `json:"network_inode"`
	RootDevice     uint64                          `json:"root_device"`
	RootInode      uint64                          `json:"root_inode"`
}

func validateGuestControlWorkerProcessRecord(record guestControlWorkerProcessRecord) error {
	e := record.Executable
	if record.PID <= 0 || record.StartTimeTicks == 0 || record.ExpectedUID != 0 || record.ExpectedGID != 0 ||
		e.Device == 0 || e.Inode == 0 || e.SizeBytes == 0 || e.SizeBytes > maxProcessSystemExecutableBytes ||
		e.Mode != unix.S_IFREG|0755 || e.UID != 0 || e.GID != 0 || e.Links != 1 ||
		!isCanonicalManagedResourceID(e.SHA256, 64) || record.MountDevice == 0 || record.MountInode == 0 ||
		record.NetworkDevice == 0 || record.NetworkInode == 0 || record.RootDevice == 0 || record.RootInode == 0 {
		return instanceValidationError("Guest Control worker root process proof is invalid")
	}
	return nil
}

func guestControlWorkerProcessesEqual(a, b *guestControlWorkerProcessRecord) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Called only after the future fixed-binary child gate authenticates the exact spawned process.
// The trusted executable capability must have been captured BEFORE spawning, not discovered from PID.
func captureGuestControlWorkerProcess(
	ctx context.Context, principal GuestControlPrincipal, pidfd int, trusted *system.PinnedExecutable,
) (_ *GuestControlWorkerProcess, returnErr error) {
	expected, err := trusted.Reinspect(ctx)
	if err != nil {
		return nil, err
	}
	fd, err := unix.FcntlInt(uintptr(pidfd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			returnErr = errors.Join(returnErr, unix.Close(fd))
		}
	}()
	locator, err := deriveProcessLocatorFromPIDFD(ctx, fd)
	if err != nil {
		return nil, err
	}
	record, err := observeGuestControlWorkerProcess(ctx, fd, locator, principal.VMID, expected)
	if err != nil {
		return nil, err
	}
	accepted = true
	return &GuestControlWorkerProcess{state: &guestControlWorkerProcessState{
		principal: principal, record: record, pidfd: fd}}, nil
}

func observeGuestControlWorkerProcess(
	ctx context.Context, pidfd int, locator instanceProcessLocator, vmID string,
	expected system.PinnedExecutableIdentity,
) (_ guestControlWorkerProcessRecord, returnErr error) {
	deps := realProcessIdentityVerifierDeps()
	alive, err := processPIDFDAlive(ctx, pidfd)
	if err != nil || !alive {
		return guestControlWorkerProcessRecord{}, processVerificationError(vmID, "worker is not live", err)
	}
	if err := requireExactPIDFDLocator(ctx, deps, pidfd, locator, vmID, "Guest Control worker"); err != nil {
		return guestControlWorkerProcessRecord{}, err
	}
	observed, err := observeProcessIdentity(ctx, deps, vmID, "Guest Control worker", locator)
	if err != nil {
		return guestControlWorkerProcessRecord{}, err
	}
	if err := requireExpectedProcessCredentials(observed.process, 0, 0, "Guest Control worker"); err != nil {
		return guestControlWorkerProcessRecord{}, err
	}
	directory, err := deps.openProcessDirectory(ctx, locator.pid)
	if err != nil {
		return guestControlWorkerProcessRecord{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, deps.close(context.WithoutCancel(ctx), directory)) }()
	executable, err := deps.pinProcessObject(ctx, directory, processExecutableObject)
	if err != nil {
		return guestControlWorkerProcessRecord{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, deps.close(context.WithoutCancel(ctx), executable)) }()
	before, err := inspectProcessExecutable(ctx, deps, executable)
	if err != nil {
		return guestControlWorkerProcessRecord{}, err
	}
	digest, after, err := hashProcessExecutable(ctx, deps, executable, before)
	if err != nil {
		return guestControlWorkerProcessRecord{}, err
	}
	actual := system.PinnedExecutableIdentity{Device: after.device, Inode: after.inode, SizeBytes: after.sizeBytes,
		Mode: after.mode, UID: after.uid, GID: after.gid, Links: after.links, SHA256: digest}
	if actual != expected || !sameExecutableObject(observed.executable, after) {
		return guestControlWorkerProcessRecord{}, processVerificationError(
			vmID, "worker executable differs from captured trusted image", nil)
	}
	if err := requireExactPIDFDLocator(ctx, deps, pidfd, locator, vmID, "Guest Control worker"); err != nil {
		return guestControlWorkerProcessRecord{}, err
	}
	alive, err = processPIDFDAlive(ctx, pidfd)
	if err != nil || !alive {
		return guestControlWorkerProcessRecord{}, processVerificationError(vmID, "worker exited during proof", err)
	}
	record := guestControlWorkerProcessRecord{PID: locator.pid, StartTimeTicks: locator.startTimeTicks,
		Executable: actual, MountDevice: observed.mountNamespace.device, MountInode: observed.mountNamespace.inode,
		NetworkDevice: observed.networkNamespace.device, NetworkInode: observed.networkNamespace.inode,
		RootDevice: observed.root.device, RootInode: observed.root.inode}
	if err := validateGuestControlWorkerProcessRecord(record); err != nil {
		return guestControlWorkerProcessRecord{}, err
	}
	return record, nil
}

func reopenGuestControlWorkerProcess(
	ctx context.Context, record guestControlWorkerRecord,
) (_ int, absent bool, returnErr error) {
	if record.Process == nil {
		return -1, true, nil
	}
	if err := validateGuestControlWorkerProcessRecord(*record.Process); err != nil {
		return -1, false, err
	}
	boot, err := realProcessIdentityVerifierDeps().readBootID(ctx)
	if err != nil {
		return -1, false, err
	}
	if boot != record.BootID {
		return -1, true, nil
	} // A process from another boot cannot survive the reboot.
	fd, err := unix.PidfdOpen(record.Process.PID, 0)
	if errors.Is(err, unix.ESRCH) {
		return -1, true, nil
	}
	if err != nil {
		return -1, false, err
	}
	accepted := false
	defer func() {
		if !accepted {
			returnErr = errors.Join(returnErr, unix.Close(fd))
		}
	}()
	alive, err := processPIDFDAlive(ctx, fd)
	if err != nil {
		return -1, false, err
	}
	if !alive {
		return -1, true, nil
	}
	locator := instanceProcessLocator{pid: record.Process.PID, startTimeTicks: record.Process.StartTimeTicks}
	actual, err := observeGuestControlWorkerProcess(ctx, fd, locator, record.VMID, record.Process.Executable)
	if err != nil {
		return -1, false, err
	}
	if actual != *record.Process {
		return -1, false, instanceValidationError("worker process rebound since durable capture")
	}
	accepted = true
	return fd, false, nil
}

func (process *GuestControlWorkerProcess) Release(ctx context.Context) error {
	if process == nil || process.state == nil {
		return nil
	}
	state := process.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	state.released = true
	if state.pidfd >= 0 {
		state.releaseErr = unix.Close(state.pidfd)
		state.pidfd = -1
	}
	return state.releaseErr
}
