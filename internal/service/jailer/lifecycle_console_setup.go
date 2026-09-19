package jailer

import (
	"context"
	"encoding/json"
	"errors"

	"golang.org/x/sys/unix"
)

// prepare sends only outputs retained by this exact durable launching lease. It completes
// before the gate is released, so failures retain the launch's existing abort authority.
func (session *launchConsoleSession) prepare(ctx context.Context, lease *launchLease) error {
	if ctx == nil || session == nil || session.state == nil || lease == nil || lease.state == nil {
		return instanceStateError("", "console setup authority is incomplete")
	}
	state := lease.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || !state.registrationComplete || state.record.lifecycle != instanceLifecycleLaunching ||
		state.record.launch == nil || state.runtimeDirectory == nil || state.persistentOutputs == nil {
		return instanceStateError(state.record.vmID, "console setup requires prepared durable launch authority")
	}
	relay := session.state
	relay.mu.Lock()
	defer relay.mu.Unlock()
	if relay.released || relay.vmID != state.record.vmID || relay.caller.uid != state.record.ownerUID ||
		relay.caller.gid != state.record.launch.ownerGID || state.record.launch.relay.kind != instanceRelayPresent ||
		relay.pid != state.record.launch.relay.locator.pid {
		return instanceStateError(state.record.vmID, "console session does not match durable launch authority")
	}
	if relay.prepared {
		return nil
	}
	runtime := state.runtimeDirectory.state
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.released || runtime.removed || !runtime.leafCreated || runtime.vmID != state.record.vmID ||
		runtime.caller != relay.caller {
		return instanceStateError(state.record.vmID, "console runtime directory is unavailable")
	}
	outputs := state.persistentOutputs.state
	outputs.mu.Lock()
	defer outputs.mu.Unlock()
	if outputs.released || !outputs.ready || outputs.owner != relay.caller {
		return instanceStateError(state.record.vmID, "console persistent outputs are unavailable")
	}
	logFD := -1
	for _, output := range outputs.outputs {
		if output.name == managedPersistentConsole {
			logFD = output.fd
		}
	}
	if logFD < 0 {
		return instanceStateError(state.record.vmID, "console log descriptor is missing")
	}
	if !relay.sent {
		plan := launchConsolePlan{Schema: 1, VMID: relay.vmID, UID: relay.caller.uid, GID: relay.caller.gid}
		packet, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, err := unix.SendmsgN(relay.fd, packet, unix.UnixRights(runtime.vmDirectoryFD, logFD), nil,
				unix.MSG_DONTWAIT|unix.MSG_NOSIGNAL)
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
				if err := waitBlockedLaunchGate(ctx, relay.fd, unix.POLLOUT); err != nil {
					return err
				}
				continue
			}
			if err != nil || n != len(packet) {
				return processAuthorityError(relay.vmID, "send console setup packet", err)
			}
			relay.sent = true
			break
		}
	}
	ack := []byte{0}
	if err := readBlockedLaunchGateFull(ctx, relay.fd, ack); err != nil {
		return processAuthorityError(relay.vmID, "read console setup acknowledgement", err)
	}
	if ack[0] != 1 {
		return processAuthorityError(relay.vmID, "invalid console setup acknowledgement", nil)
	}
	relay.prepared = true
	return nil
}

// waitReady must follow launch gate release and precede successful detach. It proves the
// relay bound its socket; process identity alone would not prove interactive console readiness.
func (session *launchConsoleSession) waitReady(ctx context.Context) error {
	if ctx == nil || session == nil || session.state == nil {
		return instanceStateError("", "console readiness authority is incomplete")
	}
	state := session.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released || !state.prepared {
		return instanceStateError(state.vmID, "console setup is not prepared")
	}
	if state.ready {
		return nil
	}
	ack := []byte{0}
	if err := readBlockedLaunchGateFull(ctx, state.fd, ack); err != nil {
		return processAuthorityError(state.vmID, "await console listener readiness", err)
	}
	if ack[0] != 1 {
		return processAuthorityError(state.vmID, "invalid console listener readiness acknowledgement", nil)
	}
	state.ready = true
	return nil
}
