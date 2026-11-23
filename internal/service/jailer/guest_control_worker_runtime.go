package jailer

import (
	"context"
	"errors"
	"io"
	"net"
	"os"

	"golang.org/x/sys/unix"
)

// AdmitGuestControlWorker rechecks current VM and the exact recorded root process before readiness.
// It releases all admission locks before the bootstrap acknowledgement can block.
func AdmitGuestControlWorker(
	ctx context.Context,
	setup GuestControlWorkerSetup,
	session *GuestControlSession,
	bootstrap net.Conn,
) (returnErr error) {
	principal, err := session.Principal()
	if err != nil {
		return err
	}
	if principal != setup.Principal {
		return instanceValidationError("worker session incarnation differs")
	}
	if err := admitGuestControlWorkerRecord(ctx, setup); err != nil {
		return err
	}
	if _, err := bootstrap.Write([]byte{2}); err != nil {
		return err
	}
	var ack [1]byte
	if _, err := io.ReadFull(bootstrap, ack[:]); err != nil {
		return err
	}
	if ack[0] != 3 {
		return instanceValidationError("invalid worker handoff acknowledgement")
	}
	return bootstrap.Close()
}

func admitGuestControlWorkerRecord(ctx context.Context, setup GuestControlWorkerSetup) (returnErr error) {
	p := setup.Principal
	admission, err := NewFloatingIPAdmissionServer().Open(ctx, p.OwnerUID, p.OwnerGID, FloatingIPAdmissionRequest{VMID: p.VMID})
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, admission.Release(context.WithoutCancel(ctx))) }()
	lease, err := NewGuestControlWorkerServer().Lock(ctx, admission)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, lease.Release(context.WithoutCancel(ctx))) }()
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	record := state.store.record
	if !state.store.found || state.principal != p || record.principal() != p || record.Generation != setup.Generation ||
		record.Phase != "spawned" ||
		record.Process == nil ||
		record.Process.PID != os.Getpid() {
		return instanceValidationError("worker bootstrap record differs from exact child")
	}
	fd, absent, err := reopenGuestControlWorkerProcess(ctx, record)
	if err != nil {
		return err
	}
	if absent {
		return instanceValidationError("worker bootstrap process is absent")
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	if record.Revision == ^uint64(0) {
		return instanceValidationError("worker revision is exhausted")
	}
	record.Revision++
	record.Phase = "ready"
	return state.store.persist(ctx, record)
}

// RecheckGuestControlWorker fences reconnects to the same recorded worker and VM incarnation.
func RecheckGuestControlWorker(ctx context.Context, setup GuestControlWorkerSetup) (returnErr error) {
	p := setup.Principal
	admission, err := NewFloatingIPAdmissionServer().Open(ctx, p.OwnerUID, p.OwnerGID, FloatingIPAdmissionRequest{VMID: p.VMID})
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, admission.Release(context.WithoutCancel(ctx))) }()
	lease, err := NewGuestControlWorkerServer().Lock(ctx, admission)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, lease.Release(context.WithoutCancel(ctx))) }()
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return err
	}
	record := state.store.record
	if !state.store.found || state.principal != p || record.principal() != p || record.Generation != setup.Generation ||
		record.Phase != "ready" ||
		record.Process == nil ||
		record.Process.PID != os.Getpid() {
		return instanceValidationError("worker is no longer current")
	}
	return nil
}
