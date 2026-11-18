package jailer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
)

const guestControlWorkerMarker = "__mvm_guest_control_worker_v1"

// GuestControlWorkerSetup contains immutable scope only; credentials are reopened from protected backing.
type GuestControlWorkerSetup struct {
	Principal  GuestControlPrincipal `json:"principal"`
	Generation uint64                `json:"generation"`
}

func IsGuestControlWorkerInvocation(args []string) bool {
	return len(args) > 0 && args[0] == guestControlWorkerMarker
}

// SpawnBlocked creates only the installed trusted mvm process, with root execution credentials.
// The child cannot bootstrap until the caller releases every VM and worker lock and calls Start.
func (lease *GuestControlWorkerLease) SpawnBlocked(ctx context.Context) (processOut *GuestControlWorkerProcess, returnErr error) {
	if lease == nil || lease.state == nil || os.Geteuid() != 0 || os.Getegid() != 0 {
		return nil, instanceValidationError("root worker spawn authority is required")
	}
	// A late descriptor/reinspection close failure still owns the child; never strand it.
	defer func() {
		if returnErr != nil && processOut != nil {
			returnErr = errors.Join(returnErr, processOut.Release(context.WithoutCancel(ctx)))
			processOut = nil
		}
	}()
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return nil, err
	}
	if state.cleanupOnly || state.store.record.Phase != "prepared" {
		return nil, instanceValidationError("worker is not prepared")
	}
	trusted, err := system.PinSystemExecutable(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, trusted.Release(context.WithoutCancel(ctx))) }()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parent := os.NewFile(uintptr(pair[0]), "worker-parent")
	child := os.NewFile(uintptr(pair[1]), "worker-child")
	defer func() { returnErr = errors.Join(returnErr, parent.Close(), child.Close()) }()
	null, err := os.OpenFile("/dev/null", os.O_RDWR|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, null.Close()) }()
	pidfd := -1
	spawned, err := os.StartProcess(infra.SystemBinaryPath, []string{infra.SystemBinaryPath, guestControlWorkerMarker}, &os.ProcAttr{
		Dir: "/", Env: []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"},
		Files: []*os.File{null, null, null, child},
		Sys:   &syscall.SysProcAttr{Setsid: true, PidFD: &pidfd, Credential: &syscall.Credential{Uid: 0, Gid: 0, Groups: []uint32{}}},
	})
	if err != nil {
		return nil, err
	}
	releaseErr := spawned.Release()
	if pidfd < 0 {
		return nil, instanceAtomicError("worker spawn requires pidfd", releaseErr)
	}
	accepted := false
	defer func() {
		if !accepted {
			returnErr = errors.Join(returnErr, terminateProcessThroughPIDFD(context.WithoutCancel(ctx), pidfd), waitAndReapProcessThroughPIDFD(context.WithoutCancel(ctx), pidfd))
		}
		returnErr = errors.Join(returnErr, unix.Close(pidfd))
	}()
	if releaseErr != nil {
		return nil, releaseErr
	}
	conn, err := net.FileConn(parent)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !accepted {
			returnErr = errors.Join(returnErr, conn.Close())
		}
	}()
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return nil, err
	}
	// Exact child executable/credentials/pidfd are verified before any setup is sent.
	process, err := captureGuestControlWorkerProcess(ctx, state.principal, pidfd, trusted)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !accepted {
			returnErr = errors.Join(returnErr, process.Release(context.WithoutCancel(ctx)))
		}
	}()
	setup := GuestControlWorkerSetup{Principal: state.principal, Generation: state.store.record.Generation}
	raw, err := json.Marshal(setup)
	if err != nil {
		return nil, err
	}
	// Fixed bounded setup frame, followed by a separately released one-byte bootstrap gate.
	if len(raw) > 4096 {
		return nil, instanceValidationError("worker setup exceeds bound")
	}
	frame := append([]byte{byte(len(raw) >> 8), byte(len(raw))}, raw...)
	if n, err := conn.Write(frame); err != nil || n != len(frame) {
		return nil, errors.Join(err, io.ErrShortWrite)
	}
	process.state.bootstrap = conn
	accepted = true
	return process, nil
}

// Start is called only after RecordSpawn and release of parent VM and worker authority.
// Closing the setup socket before the final handshake makes a parent crash fail closed.
func (process *GuestControlWorkerProcess) Start(ctx context.Context) (returnErr error) {
	if process == nil || process.state == nil {
		return instanceValidationError("worker spawn is unavailable")
	}
	s := process.state
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.released || s.bootstrap == nil || s.started {
		return instanceValidationError("worker bootstrap is unavailable")
	}
	conn := s.bootstrap
	defer func() {
		returnErr = errors.Join(returnErr, conn.Close())
		s.bootstrap = nil
		if returnErr != nil {
			returnErr = errors.Join(returnErr, terminateProcessThroughPIDFD(context.WithoutCancel(ctx), s.pidfd), waitAndReapProcessThroughPIDFD(context.WithoutCancel(ctx), s.pidfd))
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if _, err := conn.Write([]byte{1}); err != nil {
		return err
	}
	var ready [1]byte
	if _, err := io.ReadFull(conn, ready[:]); err != nil {
		return err
	}
	if ready[0] != 2 {
		return instanceValidationError("worker readiness is invalid")
	}
	if _, err := conn.Write([]byte{3}); err != nil {
		return err
	}
	s.started = true
	return nil
}

// ReceiveGuestControlWorkerSetup admits only the inherited root-owned socket before initialization.
func ReceiveGuestControlWorkerSetup(ctx context.Context, args []string) (_ GuestControlWorkerSetup, _ net.Conn, returnErr error) {
	var setup GuestControlWorkerSetup
	if ctx == nil || len(args) != 1 || args[0] != guestControlWorkerMarker || os.Geteuid() != 0 || os.Getegid() != 0 {
		return setup, nil, instanceValidationError("invalid private worker invocation")
	}
	file := os.NewFile(3, "worker-bootstrap")
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	peer, err := unix.GetsockoptUcred(3, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || peer.Uid != 0 || peer.Gid != 0 || peer.Pid <= 0 {
		return setup, nil, instanceAtomicError("worker bootstrap peer is not root", err)
	}
	trusted, err := system.PinSystemExecutable(ctx)
	if err != nil {
		return setup, nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, trusted.Release(context.WithoutCancel(ctx))) }()
	conn, err := net.FileConn(file)
	if err != nil {
		return setup, nil, err
	}
	accepted := false
	defer func() {
		if !accepted {
			returnErr = errors.Join(returnErr, conn.Close())
		}
	}()
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return setup, nil, err
	}
	var length [2]byte
	if _, err := io.ReadFull(conn, length[:]); err != nil {
		return setup, nil, err
	}
	n := int(length[0])<<8 | int(length[1])
	if n == 0 || n > 4096 {
		return setup, nil, instanceValidationError("invalid worker setup size")
	}
	raw := make([]byte, n)
	if _, err := io.ReadFull(conn, raw); err != nil {
		return setup, nil, err
	}
	if err := json.Unmarshal(raw, &setup); err != nil {
		return setup, nil, err
	}
	canonical, _ := json.Marshal(setup)
	if string(canonical) != string(raw) || setup.Generation == 0 {
		return setup, nil, instanceValidationError("invalid worker setup")
	}
	var gate [1]byte
	if _, err := io.ReadFull(conn, gate[:]); err != nil {
		return setup, nil, err
	}
	if gate[0] != 1 {
		return setup, nil, instanceValidationError("invalid worker bootstrap gate")
	}
	accepted = true
	return setup, conn, nil
}
