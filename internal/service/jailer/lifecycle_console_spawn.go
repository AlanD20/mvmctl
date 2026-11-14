package jailer

import (
	"context"
	"errors"
	"os"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

type launchConsoleSession struct {
	state *launchConsoleSessionState
	// slave is borrowed only by the root-built Firecracker descriptor packet. Release closes it.
	slave *os.File
}

type launchConsoleSessionState struct {
	// mu guards descriptor ownership and one-shot setup/readiness across copied sessions.
	mu         sync.Mutex
	fd         int
	vmID       string
	pid        int
	caller     instanceCaller
	slave      *os.File
	sent       bool
	prepared   bool
	ready      bool
	released   bool
	releaseErr error
}

// spawnBlockedLaunchConsole is the closed relay spawn boundary from ADR-0016. Only the
// authenticated caller identity and VM ID enter it; all inherited channels and PTYs are created here.
// CRITICAL: os.StartProcess drops all UID/GID credentials and clears supplementary groups before
// exec. The relay keeps the host namespaces and never receives a caller command or path.
func spawnBlockedLaunchConsole(ctx context.Context, vmID string, caller instanceCaller) (
	sourceOut *blockedLaunchProcessSource, sessionOut *launchConsoleSession, returnErr error,
) {
	if ctx == nil || os.Geteuid() != 0 || os.Getegid() != 0 || !vmIDPattern.MatchString(vmID) {
		return nil, nil, processAuthorityError(vmID, "invalid blocked console admission", nil)
	}
	if err := validateInstanceCaller(caller); err != nil {
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	// Checked close failures after assembling the result still belong to this spawn. Never
	// return an error that strands a live relay whose pidfd the coordinator did not consume.
	defer func() {
		if returnErr != nil && sourceOut != nil {
			cleanupCtx := context.WithoutCancel(ctx)
			returnErr = errors.Join(returnErr, sourceOut.gate.Release(cleanupCtx),
				terminateProcessThroughPIDFD(cleanupCtx, sourceOut.pidfd),
				waitAndReapProcessThroughPIDFD(cleanupCtx, sourceOut.pidfd), unix.Close(sourceOut.pidfd),
				sessionOut.Release(cleanupCtx))
			sourceOut, sessionOut = nil, nil
		}
	}()
	master, slave, err := openLaunchConsolePTY(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, master.Close())
		if slave != nil {
			returnErr = errors.Join(returnErr, slave.Close())
		}
	}()
	gatePair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	gateParent := os.NewFile(uintptr(gatePair[0]), "console-parent-gate")
	gateChild := os.NewFile(uintptr(gatePair[1]), "console-child-gate")
	defer func() {
		returnErr = errors.Join(returnErr, gateParent.Close(), gateChild.Close())
	}()
	setupPair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	setupParent := os.NewFile(uintptr(setupPair[0]), "console-parent-setup")
	setupChild := os.NewFile(uintptr(setupPair[1]), "console-child-setup")
	defer func() {
		returnErr = errors.Join(returnErr, setupParent.Close(), setupChild.Close())
	}()
	null, err := os.OpenFile("/dev/null", os.O_RDWR|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, null.Close()) }()
	pidfd := -1
	process, err := os.StartProcess(infra.SystemBinaryPath,
		[]string{infra.SystemBinaryPath, launchConsoleMarker}, &os.ProcAttr{
			Dir: "/", Env: []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"},
			Files: []*os.File{null, null, null, gateChild, setupChild, master},
			Sys: &syscall.SysProcAttr{Setsid: true, PidFD: &pidfd,
				Credential: &syscall.Credential{Uid: caller.uid, Gid: caller.gid, Groups: []uint32{}}},
		})
	if err != nil {
		return nil, nil, processAuthorityError(vmID, "spawn fixed console relay", err)
	}
	pid := process.Pid
	releaseErr := process.Release()
	if pidfd < 0 {
		return nil, nil, processAuthorityError(vmID, "console spawn did not return required pidfd", releaseErr)
	}
	defer func() {
		if pidfd >= 0 {
			cleanupCtx := context.WithoutCancel(ctx)
			returnErr = errors.Join(returnErr, terminateProcessThroughPIDFD(cleanupCtx, pidfd),
				waitAndReapProcessThroughPIDFD(cleanupCtx, pidfd), unix.Close(pidfd))
		}
	}()
	if releaseErr != nil {
		return nil, nil, releaseErr
	}
	gateFD, err := unix.FcntlInt(gateParent.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	gate, err := newBlockedLaunchGate(ctx, realBlockedLaunchGateDeps(), gateFD)
	if err != nil {
		return nil, nil, errors.Join(err, unix.Close(gateFD))
	}
	setupFD, err := unix.FcntlInt(setupParent.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, nil, errors.Join(err, gate.Release(context.WithoutCancel(ctx)))
	}
	source := &blockedLaunchProcessSource{pidfd: pidfd, gate: gate}
	pidfd = -1
	session := &launchConsoleSession{slave: slave, state: &launchConsoleSessionState{
		fd: setupFD, vmID: vmID, pid: pid, caller: caller, slave: slave,
	}}
	slave = nil
	return source, session, nil
}

func openLaunchConsolePTY(ctx context.Context) (_ *os.File, _ *os.File, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, master.Close())
		}
	}()
	if err := unix.IoctlSetPointerInt(int(master.Fd()), unix.TIOCSPTLCK, 0); err != nil {
		return nil, nil, err
	}
	// TIOCGPTPEER opens this exact master's slave without resolving a /dev/pts pathname.
	fd, _, errno := unix.Syscall(unix.SYS_IOCTL, master.Fd(), unix.TIOCGPTPEER,
		unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC)
	if errno != 0 {
		return nil, nil, errno
	}
	return master, os.NewFile(fd, "console-child-pty"), nil
}

func (session *launchConsoleSession) Release(ctx context.Context) error {
	if session == nil || session.state == nil {
		return nil
	}
	state := session.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	state.releaseErr = errors.Join(unix.Close(state.fd), state.slave.Close())
	state.fd = -1
	state.slave = nil
	state.released = true
	return state.releaseErr
}
