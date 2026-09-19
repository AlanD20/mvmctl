package jailer

import (
	"context"
	"errors"
	"os"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
)

type launchChildSession struct {
	state *launchChildSessionState
}

type launchChildSessionState struct {
	// mu guards the root-only setup channel and its one-shot delivery and release.
	mu         sync.Mutex
	fd         int
	vmID       string
	pid        int
	sent       bool
	released   bool
	releaseErr error
}

// spawnBlockedLaunchChild creates only the fixed root child. All externally mutable effects
// remain behind the challenge/release gate consumed by registerLaunching.
func spawnBlockedLaunchChild(ctx context.Context, vmID string) (_ *blockedLaunchProcesses, _ *launchChildSession, returnErr error) {
	if ctx == nil || os.Geteuid() != 0 || os.Getegid() != 0 || !vmIDPattern.MatchString(vmID) {
		return nil, nil, processAuthorityError(vmID, "invalid blocked launch child admission", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	executable, err := system.PinSystemExecutable(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if executable != nil {
			returnErr = appendBlockedLaunchProcessCleanupError(returnErr, "release child executable admission",
				executable.Release(context.WithoutCancel(ctx)), vmID)
		}
	}()
	gatePair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	gateParent := os.NewFile(uintptr(gatePair[0]), "jailer-parent-gate")
	gateChild := os.NewFile(uintptr(gatePair[1]), "jailer-child-gate")
	defer func() {
		if gateParent != nil {
			returnErr = errors.Join(returnErr, gateParent.Close())
		}
		if gateChild != nil {
			returnErr = errors.Join(returnErr, gateChild.Close())
		}
	}()
	setupPair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	if err := unix.SetsockoptInt(setupPair[0], unix.SOL_SOCKET, unix.SO_PASSCRED, 1); err != nil {
		return nil, nil, errors.Join(err, unix.Close(setupPair[0]), unix.Close(setupPair[1]))
	}
	setupParent := os.NewFile(uintptr(setupPair[0]), "jailer-parent-setup")
	setupChild := os.NewFile(uintptr(setupPair[1]), "jailer-child-setup")
	defer func() {
		if setupParent != nil {
			returnErr = errors.Join(returnErr, setupParent.Close())
		}
		if setupChild != nil {
			returnErr = errors.Join(returnErr, setupChild.Close())
		}
	}()
	null, err := os.OpenFile("/dev/null", os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, null.Close()) }()
	pidfd := -1
	// This is the fixed Jailer child spawn boundary. No user executable, argument, environment,
	// working directory or inherited stdio enters os.StartProcess.
	process, err := os.StartProcess(infra.SystemBinaryPath, []string{infra.SystemBinaryPath, launchChildMarker}, &os.ProcAttr{
		Dir: "/", Env: []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"},
		Files: []*os.File{null, null, null, gateChild, setupChild},
		Sys:   &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNS, Setsid: true, PidFD: &pidfd},
	})
	if err != nil {
		return nil, nil, processAuthorityError(vmID, "spawn fixed Jailer child", err)
	}
	processPID := process.Pid
	processReleaseErr := process.Release()
	gateCloseErr := gateChild.Close()
	gateChild = nil
	setupCloseErr := setupChild.Close()
	setupChild = nil
	if pidfd < 0 {
		return nil, nil, processAuthorityError(vmID, "spawn did not return required child pidfd", processReleaseErr)
	}
	cleanupPIDFD := pidfd
	defer func() {
		if cleanupPIDFD >= 0 {
			cleanupCtx := context.WithoutCancel(ctx)
			returnErr = errors.Join(returnErr, terminateProcessThroughPIDFD(cleanupCtx, cleanupPIDFD),
				waitAndReapProcessThroughPIDFD(cleanupCtx, cleanupPIDFD), unix.Close(cleanupPIDFD))
		}
	}()
	if err := errors.Join(processReleaseErr, gateCloseErr, setupCloseErr); err != nil {
		return nil, nil, processAuthorityError(vmID, "close child-side spawn resources", err)
	}
	retainedPIDFD, err := unix.FcntlInt(uintptr(pidfd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	retainedGateFD, err := unix.FcntlInt(gateParent.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, nil, errors.Join(err, unix.Close(retainedPIDFD))
	}
	gate, err := newBlockedLaunchGate(ctx, realBlockedLaunchGateDeps(), retainedGateFD)
	if err != nil {
		return nil, nil, errors.Join(err, unix.Close(retainedGateFD), unix.Close(retainedPIDFD))
	}
	processes, err := newBlockedLaunchProcesses(ctx, realBlockedLaunchProcessDeps(), vmID,
		blockedLaunchProcessSource{pidfd: retainedPIDFD, gate: gate}, nil, executable)
	executable = nil
	if err != nil {
		return nil, nil, err
	}
	setupFD, err := unix.FcntlInt(setupParent.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, nil, errors.Join(err, processes.Release(context.WithoutCancel(ctx)))
	}
	closeErr := unix.Close(cleanupPIDFD)
	cleanupPIDFD = -1
	if closeErr != nil {
		return nil, nil, errors.Join(closeErr, unix.Close(setupFD),
			processes.terminateAndReap(context.WithoutCancel(ctx)), processes.Release(context.WithoutCancel(ctx)))
	}
	return processes, &launchChildSession{state: &launchChildSessionState{
		fd: setupFD, vmID: vmID, pid: processPID,
	}}, nil
}

func (session *launchChildSession) Release(ctx context.Context) error {
	if session == nil || session.state == nil {
		return nil
	}
	state := session.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	state.releaseErr = unix.Close(state.fd)
	state.fd = -1
	state.released = true
	return state.releaseErr
}
