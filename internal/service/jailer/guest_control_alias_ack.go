package jailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"

	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/guestcontrolwire"
)

// GuestControlAliasAcknowledgement records one agent ACK, never packet or replay authority.
// Digest binds the complete ordered resource/generation/address projection. Query requires a current live worker.
type GuestControlAliasAcknowledgement struct {
	Schema           uint32                `json:"schema"`
	Principal        GuestControlPrincipal `json:"principal"`
	WorkerGeneration uint64                `json:"worker_generation"`
	SessionID        string                `json:"session_id"`
	SnapshotID       string                `json:"snapshot_id"`
	Digest           string                `json:"digest"`
}

func (receipt GuestControlAliasAcknowledgement) validate() error {
	p := receipt.Principal
	record := guestControlWorkerRecord{
		Schema:           1,
		ResourceOwnerUID: p.OwnerUID,
		ResourceOwnerGID: p.OwnerGID,
		VMID:             p.VMID,
		BootID:           p.BootID,
		LaunchGeneration: p.LaunchGeneration,
		ExecutionUID:     p.ExecutionUID,
		Generation:       receipt.WorkerGeneration,
		Revision:         receipt.WorkerGeneration,
		Phase:            "prepared",
	}
	if receipt.Schema != 1 {
		return instanceValidationError("invalid operator alias ACK schema")
	}
	if err := validateGuestControlWorkerRecord(record); err != nil {
		return err
	}
	return (guestcontrolwire.FloatingIPAliasAck{SchemaVersion: 1, Feature: "floating_ip_alias_ack", SessionID: receipt.SessionID, SnapshotID: receipt.SnapshotID, Digest: receipt.Digest, Accepted: true}).Validate()
}

// RecordFloatingIPAliasAcknowledgement accepts only this session's private confirmed ACK and exact current worker.
// All root locks are acquired after the network snapshot and transport exchange have completed.
func (session *GuestControlSession) RecordFloatingIPAliasAcknowledgement(
	ctx context.Context,
	setup GuestControlWorkerSetup,
	snapshot guestcontrolwire.FloatingIPAliasSnapshot,
) (returnErr error) {
	if ctx == nil || session == nil || session.state == nil {
		return instanceValidationError("operator alias ACK session is unavailable")
	}
	digest, err := snapshot.Digest()
	if err != nil {
		return err
	}
	session.state.mu.Lock()
	confirmed := session.state.aliasConfirmed
	valid := !session.state.closed && session.state.principal == setup.Principal && confirmed != nil &&
		confirmed.SnapshotID == snapshot.ID &&
		confirmed.Digest == digest
	var ack guestcontrolwire.FloatingIPAliasAck
	if valid {
		ack = *confirmed
	}
	session.state.mu.Unlock()
	if !valid {
		return instanceValidationError("operator alias ACK is not confirmed on this session")
	}
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
		return instanceValidationError("operator alias ACK worker is no longer current")
	}
	fd, absent, err := reopenGuestControlWorkerProcess(ctx, record)
	if err != nil {
		return err
	}
	if absent {
		return instanceValidationError("operator alias ACK worker is absent")
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	receipt := GuestControlAliasAcknowledgement{
		Schema:           1,
		Principal:        p,
		WorkerGeneration: setup.Generation,
		SessionID:        ack.SessionID,
		SnapshotID:       ack.SnapshotID,
		Digest:           ack.Digest,
	}
	return state.store.writeAliasAcknowledgement(ctx, receipt)
}

func (lease *GuestControlWorkerLease) FloatingIPAliasAcknowledgement(
	ctx context.Context,
) (_ GuestControlAliasAcknowledgement, found bool, returnErr error) {
	if lease == nil || lease.state == nil {
		return GuestControlAliasAcknowledgement{}, false, instanceValidationError(
			"operator alias ACK worker lease is unavailable",
		)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return GuestControlAliasAcknowledgement{}, false, err
	}
	record := state.store.record
	if !state.store.found || record.Phase != "ready" || record.principal() != state.principal {
		return GuestControlAliasAcknowledgement{}, false, nil
	}
	fd, absent, err := reopenGuestControlWorkerProcess(ctx, record)
	if err != nil {
		return GuestControlAliasAcknowledgement{}, false, err
	}
	if absent {
		return GuestControlAliasAcknowledgement{}, false, nil
	}
	defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	receipt, found, err := state.store.readAliasAcknowledgement(ctx)
	if err != nil || !found {
		return GuestControlAliasAcknowledgement{}, false, err
	}
	if receipt.Principal != state.principal || receipt.WorkerGeneration != record.Generation {
		return GuestControlAliasAcknowledgement{}, false, nil
	}
	return receipt, true, nil
}

// FloatingIPAliasCleanupAcknowledgement observes only empty-snapshot cleanup history.
// A stopped worker may prove cleanup, never current association or authority to restore an alias.
// The caller must additionally require a changed snapshot ID from its pre-mutation baseline.
func (lease *GuestControlWorkerLease) FloatingIPAliasCleanupAcknowledgement(
	ctx context.Context,
) (_ GuestControlAliasAcknowledgement, found bool, returnErr error) {
	if lease == nil || lease.state == nil {
		return GuestControlAliasAcknowledgement{}, false, instanceValidationError(
			"operator alias cleanup ACK worker lease is unavailable",
		)
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := state.ready(ctx); err != nil {
		return GuestControlAliasAcknowledgement{}, false, err
	}
	record := state.store.record
	if !state.store.found || record.principal() != state.principal ||
		record.Phase != "ready" && record.Phase != "stopped" {
		return GuestControlAliasAcknowledgement{}, false, nil
	}
	receipt, found, err := state.store.readAliasAcknowledgement(ctx)
	if err != nil || !found {
		return GuestControlAliasAcknowledgement{}, false, err
	}
	admitted, err := admitFloatingIPAliasCleanupAcknowledgement(record, receipt, state.principal)
	if err != nil || !admitted {
		return GuestControlAliasAcknowledgement{}, false, err
	}
	fd, absent, err := reopenGuestControlWorkerProcess(ctx, record)
	if err != nil {
		return GuestControlAliasAcknowledgement{}, false, err
	}
	if !absent {
		defer func() { returnErr = errors.Join(returnErr, unix.Close(fd)) }()
	}
	if record.Phase == "ready" && absent || record.Phase == "stopped" && !absent {
		return GuestControlAliasAcknowledgement{}, false, nil
	}
	return receipt, true, nil
}

func admitFloatingIPAliasCleanupAcknowledgement(
	record guestControlWorkerRecord, receipt GuestControlAliasAcknowledgement, principal GuestControlPrincipal,
) (bool, error) {
	if err := receipt.validate(); err != nil {
		return false, err
	}
	if record.principal() != principal || receipt.Principal != principal ||
		receipt.WorkerGeneration != record.Generation ||
		record.Phase != "ready" && record.Phase != "stopped" {
		return false, nil
	}
	// Snapshot IDs correlate individual exchanges. The canonical authority digest deliberately covers only aliases.
	empty := guestcontrolwire.FloatingIPAliasSnapshot{
		ID:      "00000000-0000-4000-8000-000000000000",
		Aliases: []guestcontrolwire.FloatingIPAlias{},
	}
	digest, err := empty.Digest()
	if err != nil {
		return false, err
	}
	return receipt.Digest == digest, nil
}

func (store *guestControlWorkerStore) readAliasAcknowledgement(
	ctx context.Context,
) (_ GuestControlAliasAcknowledgement, found bool, returnErr error) {
	d := store.directories
	fd, err := d.deps.openAt(ctx, d.stateFD, store.vmID+".aliases.json", instanceReadFlags, 0)
	if errors.Is(err, unix.ENOENT) {
		return GuestControlAliasAcknowledgement{}, false, nil
	}
	if err != nil {
		return GuestControlAliasAcknowledgement{}, false, err
	}
	defer func() { returnErr = errors.Join(returnErr, d.deps.close(context.WithoutCancel(ctx), fd)) }()
	if err := verifyGuestControlWorkerFile(ctx, d.deps, fd, "operator alias ACK", d.policy); err != nil {
		return GuestControlAliasAcknowledgement{}, false, err
	}
	var before, after unix.Stat_t
	if err := d.deps.fstat(ctx, fd, &before); err != nil {
		return GuestControlAliasAcknowledgement{}, false, err
	}
	raw, err := readAllInstanceRecord(ctx, d.deps, fd)
	if err != nil {
		return GuestControlAliasAcknowledgement{}, false, err
	}
	if err := d.deps.fstat(ctx, fd, &after); err != nil || !guestControlWorkerRecordStatEqual(before, after) {
		return GuestControlAliasAcknowledgement{}, false, instanceAtomicError(
			"operator alias ACK changed during read",
			err,
		)
	}
	var receipt GuestControlAliasAcknowledgement
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return GuestControlAliasAcknowledgement{}, false, err
	}
	canonical, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(raw, canonical) {
		return GuestControlAliasAcknowledgement{}, false, instanceValidationError("operator alias ACK is not canonical")
	}
	if err := receipt.validate(); err != nil {
		return GuestControlAliasAcknowledgement{}, false, err
	}
	if receipt.Principal.OwnerUID != d.uid || receipt.Principal.VMID != store.vmID {
		return GuestControlAliasAcknowledgement{}, false, instanceValidationError("operator alias ACK scope differs")
	}
	return receipt, true, nil
}

func (store *guestControlWorkerStore) writeAliasAcknowledgement(
	ctx context.Context,
	receipt GuestControlAliasAcknowledgement,
) (returnErr error) {
	if err := receipt.validate(); err != nil {
		return err
	}
	if receipt.Principal != store.record.principal() || receipt.WorkerGeneration != store.record.Generation {
		return instanceValidationError("operator alias ACK publication binding differs")
	}
	// Reject foreign existing files before replacement. An old worker receipt is harmless and may be replaced under this lock.
	if _, _, err := store.readAliasAcknowledgement(ctx); err != nil {
		return err
	}
	raw, err := json.Marshal(receipt)
	if err != nil || len(raw) > maxInstanceRecordBytes {
		return instanceAtomicError("encode bounded operator alias ACK", err)
	}
	d := store.directories
	name, fd, err := createInstanceRecordTemp(ctx, d)
	if err != nil {
		return err
	}
	open := true
	published := false
	defer func() {
		cleanup := context.WithoutCancel(ctx)
		if open {
			returnErr = errors.Join(returnErr, d.deps.close(cleanup, fd))
		}
		if !published {
			err := d.deps.unlinkAt(cleanup, d.stateFD, name, 0)
			if !errors.Is(err, unix.ENOENT) {
				returnErr = errors.Join(returnErr, err)
			}
		}
	}()
	if err := writeAllInstanceRecord(ctx, d.deps, fd, raw); err != nil {
		return err
	}
	if err := d.deps.fchown(ctx, fd, int(d.policy.expectedUID), int(d.policy.expectedGID)); err != nil {
		return err
	}
	if err := d.deps.fchmod(ctx, fd, 0600); err != nil {
		return err
	}
	if err := verifyGuestControlWorkerFile(ctx, d.deps, fd, "operator alias ACK candidate", d.policy); err != nil {
		return err
	}
	if err := d.deps.fsync(ctx, fd); err != nil {
		return err
	}
	closeErr := d.deps.close(context.WithoutCancel(ctx), fd)
	open = false
	if closeErr != nil {
		return closeErr
	}
	if _, _, err := store.readAliasAcknowledgement(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := d.deps.renameAt(ctx, d.stateFD, name, d.stateFD, store.vmID+".aliases.json"); err != nil {
		return err
	}
	published = true
	if err := d.deps.fsync(ctx, d.stateFD); err != nil {
		return err
	}
	observed, found, err := store.readAliasAcknowledgement(ctx)
	if err != nil || !found || observed != receipt {
		return instanceAtomicError("operator alias ACK publication readback differs", err)
	}
	return nil
}
