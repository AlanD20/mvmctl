package jailer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

// retainedCgroupProcessVerifier bridges to process authority that already retains the pidfd. Its implementation must
// prove the locator both before and after this component reads membership; a numeric PID alone cannot satisfy it.
type retainedCgroupProcessVerifier interface {
	verifyBeforeCgroupMembership(context.Context, instanceProcessLocator, processObservation) error
	verifyAfterCgroupMembership(context.Context, instanceProcessLocator, processObservation) error
}

// VerifyMembership proves that the exact retained process is the sole member of this pinned leaf. Concurrent
// verification, removal, and release fail closed while the proof runs without holding the lease mutex.
func (lease *managedVMCgroupLease) VerifyMembership(
	ctx context.Context,
	locator instanceProcessLocator,
	observation processObservation,
	verifier retainedCgroupProcessVerifier,
) error {
	if lease == nil || lease.state == nil {
		return instanceStateError("", "managed VM cgroup lease is not active")
	}
	state := lease.state
	state.mu.Lock()
	if ctx == nil {
		state.mu.Unlock()
		return instanceAtomicError("verify managed VM cgroup membership: context is required", nil)
	}
	if state.verifying {
		state.mu.Unlock()
		return instanceStateError(state.vmID, "managed VM cgroup membership verification is in progress")
	}
	if state.released || state.removed || state.rootFD < 0 || state.parentFD < 0 || state.leafFD < 0 ||
		len(state.retained) != 3 {
		state.mu.Unlock()
		return instanceStateError(state.vmID, "managed VM cgroup lease is not active")
	}
	if err := ctx.Err(); err != nil {
		state.mu.Unlock()
		return instanceAtomicError("verify managed VM cgroup membership", err)
	}
	if verifier == nil {
		state.mu.Unlock()
		return instanceAtomicError("retained cgroup process verifier is required", nil)
	}
	state.verifying = true
	proof := managedVMCgroupLeaseState{
		deps:   state.deps,
		vmID:   state.vmID,
		leafFD: state.leafFD,
		leaf:   state.leaf,
	}
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		state.verifying = false
		state.mu.Unlock()
	}()

	if locator.pid <= 0 || locator.startTimeTicks == 0 || observation.startTimeTicks != locator.startTimeTicks {
		return processVerificationError(proof.vmID, "cgroup process observation does not match its locator", nil)
	}
	wantCgroup := infra.CgroupV2Root + "/" + infra.JailerCgroupParent + "/" + proof.vmID
	if observation.cgroupPath != wantCgroup {
		return processVerificationError(
			proof.vmID,
			"process observation does not match the derived VM cgroup",
			nil,
		)
	}
	if err := verifier.verifyBeforeCgroupMembership(ctx, locator, observation); err != nil {
		return processVerificationError(proof.vmID, "verify retained process before cgroup membership", err)
	}
	members, err := readManagedVMCgroupMembership(ctx, &proof)
	if err != nil {
		return err
	}
	if len(members) != 1 || members[0] != locator.pid {
		return processVerificationError(
			proof.vmID,
			"managed VM cgroup membership is not the retained process",
			nil,
		)
	}
	if err := verifier.verifyAfterCgroupMembership(ctx, locator, observation); err != nil {
		return processVerificationError(proof.vmID, "verify retained process after cgroup membership", err)
	}
	return nil
}

func readManagedVMCgroupMembership(
	ctx context.Context,
	state *managedVMCgroupLeaseState,
) (_ []int, returnErr error) {
	how := unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: uint64(managedVMCgroupResolveFlags),
	}
	fd, err := state.deps.openAt2(ctx, state.leafFD, "cgroup.procs", &how)
	if err != nil {
		return nil, processVerificationError(state.vmID, "open pinned cgroup membership", err)
	}
	defer func() {
		returnErr = appendInstanceOperationError(
			returnErr,
			"close pinned cgroup membership",
			state.deps.close(context.WithoutCancel(ctx), fd),
		)
	}()
	identity, err := inspectManagedVMCgroupRegularFile(
		ctx,
		state.deps,
		fd,
		"cgroup.procs",
		managedVMCgroupProcsMode,
	)
	if err != nil {
		return nil, err
	}
	if identity.mount != state.leaf.mount || identity.deviceMajor != state.leaf.deviceMajor ||
		identity.deviceMinor != state.leaf.deviceMinor {
		return nil, processVerificationError(state.vmID, "cgroup membership is outside the pinned cgroup-v2 mount", nil)
	}
	buffer, err := readAllManagedVMCgroupMembership(ctx, state, fd)
	if err != nil {
		return nil, err
	}
	members, err := parseManagedVMCgroupMembership(buffer)
	if err != nil {
		return nil, processVerificationError(state.vmID, "parse pinned cgroup membership", err)
	}
	return members, nil
}

func readAllManagedVMCgroupMembership(
	ctx context.Context,
	state *managedVMCgroupLeaseState,
	fd int,
) ([]byte, error) {
	const chunkBytes = 4096
	raw := make([]byte, 0, chunkBytes)
	var offset int64
	for {
		remaining := managedVMCgroupMaxProcs + 1 - len(raw)
		readSize := min(chunkBytes, remaining)
		chunk := make([]byte, readSize)
		count, err := state.deps.pread(ctx, fd, chunk, offset)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return nil, processVerificationError(state.vmID, "read pinned cgroup membership", err)
		}
		if count < 0 || count > len(chunk) {
			return nil, processVerificationError(state.vmID, "pinned cgroup membership read count is invalid", nil)
		}
		if count == 0 {
			return raw, nil
		}
		raw = append(raw, chunk[:count]...)
		if len(raw) > managedVMCgroupMaxProcs {
			return nil, processVerificationError(state.vmID, "pinned cgroup membership exceeds its bound", nil)
		}
		offset += int64(count)
	}
}

func parseManagedVMCgroupMembership(raw []byte) ([]int, error) {
	if len(raw) == 0 {
		return []int{}, nil
	}
	if raw[len(raw)-1] != '\n' {
		return nil, fmt.Errorf("cgroup membership is not newline terminated")
	}
	lines := strings.Split(string(raw[:len(raw)-1]), "\n")
	members := make([]int, 0, len(lines))
	for _, line := range lines {
		if line == "" || (len(line) > 1 && line[0] == '0') {
			return nil, fmt.Errorf("cgroup membership is not canonical")
		}
		pid, err := strconv.Atoi(line)
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("cgroup membership contains an invalid process ID")
		}
		members = append(members, pid)
	}
	return members, nil
}
