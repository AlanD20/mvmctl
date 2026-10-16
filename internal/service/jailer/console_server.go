package jailer

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

// ConsoleCaller is authenticated by the receiver, never supplied by the request.
type ConsoleCaller struct {
	UID uint32
	GID uint32
}

// ConsoleServer observes or stops only the relay recorded by root for one existing VM.
type ConsoleServer struct {
	authority *instanceAuthority
	recovery  processRecoveryDeps
	identity  processIdentityVerifierDeps
	runtime   managedVMRuntimeDeps
}

func NewConsoleServer() *ConsoleServer {
	return &ConsoleServer{authority: newInstanceAuthority(), recovery: realProcessRecoveryDeps(),
		identity: realProcessIdentityVerifierDeps(), runtime: realManagedVMRuntimeDeps()}
}

func (server *ConsoleServer) Observe(ctx context.Context, caller ConsoleCaller, request ConsoleObserveRequest) (
	result ConsoleObserveResult, returnErr error,
) {
	if err := request.Validate(); err != nil {
		return result, err
	}
	if ctx == nil || server == nil || server.authority == nil {
		return result, consoleAuthorityError(request.VMID, "console observation is unavailable", nil)
	}
	locked, err := server.authority.lockObservedInstance(
		ctx,
		instanceCaller{uid: caller.UID, gid: caller.GID},
		request.VMID,
	)
	if err != nil {
		if domainErr := errs.AsDomainError(err); domainErr != nil && domainErr.Code == errs.CodeVMNotFound &&
			domainErr.Err == nil && len(domainErr.Details) == 0 {
			return ConsoleObserveResult{VMID: request.VMID, State: ConsoleAbsent}, nil
		}
		return result, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "release console observation authority",
			locked.Release(context.WithoutCancel(ctx)))
	}()
	result = ConsoleObserveResult{VMID: request.VMID, State: ConsoleAbsent}
	record := locked.record
	if record.lifecycle == instanceLifecycleCleaned {
		return result, nil
	}
	if record.launch == nil {
		return ConsoleObserveResult{}, consoleAuthorityError(request.VMID, "console requires active authority", nil)
	}
	if server.identity.readBootID == nil {
		return ConsoleObserveResult{}, consoleAuthorityError(
			request.VMID,
			"console boot observation is unavailable",
			nil,
		)
	}
	bootID, err := server.identity.readBootID(ctx)
	if err != nil || !hostBootIDPattern.MatchString(bootID) {
		return ConsoleObserveResult{}, consoleAuthorityError(request.VMID, "read console host boot identity", err)
	}
	if bootID != record.launch.hostBootID {
		// A prior-boot relay cannot be live. Its PID must never select a current task or endpoint.
		return ConsoleObserveResult{VMID: request.VMID, State: ConsoleStopped}, nil
	}
	if record.lifecycle != instanceLifecycleActive || record.active == nil {
		return ConsoleObserveResult{}, consoleAuthorityError(request.VMID, "console requires active authority", nil)
	}
	if record.active.relay.kind == instanceRelayAbsent {
		return result, nil
	}
	result.State = ConsoleState(record.active.relay.lifecycle)
	if record.active.relay.lifecycle == instanceRelayStopped {
		return result, nil
	}
	relay, err := openRecordedConsoleRelay(ctx, server.recovery, server.identity, record)
	if err != nil {
		return ConsoleObserveResult{}, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "close observed console pidfd", relay.release(ctx))
	}()
	if relay.exited {
		// Observation never completes a pending stop or changes the journal.
		if record.active.relay.lifecycle == instanceRelayRunning {
			result.State = ConsoleStopped
		}
		return result, server.verifyBoot(ctx, record)
	}
	if record.active.relay.lifecycle == instanceRelayStopping {
		return result, server.verifyBoot(ctx, record)
	}
	if err := server.observeEndpoint(ctx, record); err != nil {
		return ConsoleObserveResult{}, err
	}
	if err := relay.verify(ctx, server.identity, record); err != nil {
		return ConsoleObserveResult{}, err
	}
	if err := server.verifyBoot(ctx, record); err != nil {
		return ConsoleObserveResult{}, err
	}
	pid := record.launch.relay.locator.pid
	result.PID = &pid
	result.SocketPath = consoleRuntimeSocketPath(record.ownerUID, record.vmID)
	return result, nil
}

func (server *ConsoleServer) Stop(ctx context.Context, caller ConsoleCaller, request ConsoleStopRequest) (
	result ConsoleStopResult, returnErr error,
) {
	if err := request.Validate(); err != nil {
		return result, err
	}
	if ctx == nil || server == nil || server.authority == nil {
		return result, consoleAuthorityError(request.VMID, "console stop is unavailable", nil)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	locked, err := server.authority.lockObservedInstance(
		ctx,
		instanceCaller{uid: caller.UID, gid: caller.GID},
		request.VMID,
	)
	if err != nil {
		if domainErr := errs.AsDomainError(err); domainErr != nil && domainErr.Code == errs.CodeVMNotFound &&
			domainErr.Err == nil && len(domainErr.Details) == 0 {
			return ConsoleStopResult{VMID: request.VMID}, nil
		}
		return result, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "release console stop authority",
			locked.Release(context.WithoutCancel(ctx)))
	}()
	return server.stopLocked(ctx, locked)
}

func (server *ConsoleServer) stopLocked(ctx context.Context, locked *lockedInstance) (
	result ConsoleStopResult, returnErr error,
) {
	record := cloneInstanceRecord(locked.record)
	result.VMID = record.vmID
	if record.lifecycle == instanceLifecycleCleaned {
		return result, nil
	}
	if record.lifecycle != instanceLifecycleActive || record.launch == nil || record.active == nil {
		return result, consoleAuthorityError(record.vmID, "console stop requires active authority", nil)
	}
	if err := server.verifyBoot(ctx, record); err != nil {
		return result, err
	}
	if record.active.relay.kind == instanceRelayAbsent || record.active.relay.lifecycle == instanceRelayStopped {
		return result, nil
	}
	relay, err := openRecordedConsoleRelay(ctx, server.recovery, server.identity, record)
	if err != nil {
		return result, err
	}
	defer func() {
		returnErr = appendInstanceOperationError(returnErr, "close stopped console pidfd", relay.release(ctx))
	}()
	result.Stopped = record.active.relay.lifecycle == instanceRelayStopping || !relay.exited
	if record.active.relay.lifecycle == instanceRelayRunning {
		record.active.relay.lifecycle = instanceRelayStopping
	}
	// Republish stopping on retry too: an earlier rename may have returned uncertain
	// directory durability. No signal follows until this request proves durable intent.
	if err := locked.directories.writeRecord(ctx, record); err != nil {
		return ConsoleStopResult{}, err
	}
	locked.record = cloneInstanceRecord(record)
	if err := server.verifyBoot(ctx, record); err != nil {
		return ConsoleStopResult{}, err
	}
	if !relay.exited {
		if err := relay.verify(ctx, server.identity, record); err != nil {
			return ConsoleStopResult{}, err
		}
		if err := server.verifyBoot(ctx, record); err != nil {
			return ConsoleStopResult{}, err
		}
		if err := server.recovery.terminatePIDFD(ctx, relay.pidfd); err != nil {
			return ConsoleStopResult{}, consoleAuthorityError(record.vmID, "signal exact console relay", err)
		}
		if err := server.recovery.waitReapPIDFD(ctx, relay.pidfd); err != nil {
			return ConsoleStopResult{}, consoleAuthorityError(record.vmID, "wait for console relay exit", err)
		}
		alive, err := server.recovery.pidfdAlive(ctx, relay.pidfd)
		if err != nil || alive {
			return ConsoleStopResult{}, consoleAuthorityError(record.vmID, "console exit is not proved", err)
		}
	}
	if err := server.verifyBoot(ctx, record); err != nil {
		return ConsoleStopResult{}, err
	}
	record.active.relay.lifecycle = instanceRelayStopped
	if err := locked.directories.writeRecord(ctx, record); err != nil {
		return ConsoleStopResult{}, err
	}
	locked.record = record
	return result, nil
}

func (server *ConsoleServer) verifyBoot(ctx context.Context, record instanceRecord) error {
	if server.identity.readBootID == nil {
		return consoleAuthorityError(record.vmID, "console boot observation is unavailable", nil)
	}
	bootID, err := server.identity.readBootID(ctx)
	if err != nil || bootID != record.launch.hostBootID {
		return consoleAuthorityError(record.vmID, "console host boot identity mismatch", err)
	}
	return nil
}

type recordedConsoleRelay struct {
	deps   processRecoveryDeps
	pidfd  int
	exited bool
}

func openRecordedConsoleRelay(ctx context.Context, recovery processRecoveryDeps,
	identity processIdentityVerifierDeps, record instanceRecord,
) (_ *recordedConsoleRelay, returnErr error) {
	if err := validateProcessRecoveryDeps(recovery); err != nil {
		return nil, consoleAuthorityError(record.vmID, "console process authority is incomplete", err)
	}
	if err := validateProcessIdentityVerifierDeps(identity); err != nil {
		return nil, consoleAuthorityError(record.vmID, "console identity authority is incomplete", err)
	}
	relay := &recordedConsoleRelay{deps: recovery, pidfd: -1}
	fd, err := recovery.pidfdOpen(ctx, record.launch.relay.locator.pid)
	if errors.Is(err, unix.ESRCH) {
		relay.exited = true
		return relay, nil
	}
	if err != nil {
		return nil, consoleAuthorityError(record.vmID, "open console pidfd", err)
	}
	relay.pidfd = fd
	defer func() {
		if returnErr != nil {
			returnErr = appendInstanceOperationError(returnErr, "close rejected console pidfd", relay.release(ctx))
		}
	}()
	flags, err := recovery.descriptorFlags(ctx, fd)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		return nil, consoleAuthorityError(record.vmID, "console pidfd is not close-on-exec", err)
	}
	alive, err := recovery.pidfdAlive(ctx, fd)
	if err != nil {
		return nil, consoleAuthorityError(record.vmID, "inspect console pidfd liveness", err)
	}
	if !alive {
		// A newly opened dead pidfd cannot prove which launch it once identified. Refuse
		// rather than treating a reused zombie PID as this relay's confirmed exit.
		return nil, consoleAuthorityError(record.vmID, "dead console pidfd has no complete locator proof", nil)
	}
	if err := relay.verify(ctx, identity, record); err != nil {
		return nil, err
	}
	return relay, nil
}

func (relay *recordedConsoleRelay) verify(ctx context.Context, identity processIdentityVerifierDeps,
	record instanceRecord,
) error {
	for range 2 {
		locator, err := relay.deps.deriveLocator(ctx, relay.pidfd)
		if err != nil || locator != record.launch.relay.locator {
			return consoleAuthorityError(record.vmID, "console process locator does not match authority", err)
		}
		if err := verifyLifecycleRelayIdentity(ctx, identity, record); err != nil {
			return err
		}
		alive, err := relay.deps.pidfdAlive(ctx, relay.pidfd)
		if err != nil || !alive {
			return consoleAuthorityError(record.vmID, "console process exited during verification", err)
		}
	}
	return nil
}

func (relay *recordedConsoleRelay) release(ctx context.Context) error {
	if relay.pidfd < 0 {
		return nil
	}
	fd := relay.pidfd
	relay.pidfd = -1
	return relay.deps.close(context.WithoutCancel(ctx), fd)
}
