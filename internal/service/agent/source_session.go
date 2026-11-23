package agent

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"mvmctl/internal/lib/vmexecwire"
)

const (
	sourcePublicationWait = 2 * time.Second
	sourceDisconnectPoll  = 50 * time.Millisecond
)

type sourceTransport struct {
	conn      net.Conn
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	deadline  time.Time

	watchStop      chan struct{}
	watchDone      chan struct{}
	watchStartOnce sync.Once
	watchStopOnce  sync.Once
	watchErr       error
}

func newSourceTransport(conn net.Conn) *sourceTransport {
	return &sourceTransport{
		conn:      conn,
		done:      make(chan struct{}),
		deadline:  time.Now().Add(vmexecwire.MaxExecutionDuration),
		watchStop: make(chan struct{}),
		watchDone: make(chan struct{}),
	}
}

func (transport *sourceTransport) close() error {
	transport.closeOnce.Do(func() {
		transport.closeErr = transport.conn.Close()
		close(transport.done)
	})
	return transport.closeErr
}

func (transport *sourceTransport) startDisconnectWatch(ctx context.Context) {
	transport.watchStartOnce.Do(func() {
		go func() {
			transport.watchErr = transport.watchHostDisconnect(ctx)
			close(transport.watchDone)
		}()
	})
}

func (transport *sourceTransport) watchHostDisconnect(ctx context.Context) error {
	var probe [1]byte
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-transport.done:
			return nil
		case <-transport.watchStop:
			return nil
		default:
		}
		if !time.Now().Before(transport.deadline) {
			return fmt.Errorf("source transport deadline exceeded")
		}
		pollDeadline := time.Now().Add(sourceDisconnectPoll)
		if transport.deadline.Before(pollDeadline) {
			pollDeadline = transport.deadline
		}
		if err := transport.conn.SetReadDeadline(pollDeadline); err != nil {
			return err
		}
		read, err := transport.conn.Read(probe[:])
		if read > 0 {
			return fmt.Errorf("source host sent data before a guest request")
		}
		if err == nil {
			return fmt.Errorf("source host read made no progress")
		}
		// os.File.Read wraps its poll deadline in fs.PathError, which is not a net.Error.
		if errors.Is(err, os.ErrDeadlineExceeded) {
			continue
		}
		return err
	}
}

func (transport *sourceTransport) handoffReadOwner() error {
	transport.startDisconnectWatch(context.Background())
	transport.watchStopOnce.Do(func() { close(transport.watchStop) })
	// Force a deadline-polled monitor to observe the ownership handoff promptly.
	_ = transport.conn.SetReadDeadline(time.Now())
	<-transport.watchDone
	if transport.watchErr != nil {
		return transport.watchErr
	}
	select {
	case <-transport.done:
		return fmt.Errorf("source transport closed before relay handoff")
	default:
	}
	return transport.conn.SetReadDeadline(transport.deadline)
}

type sourceBinding struct {
	cleanupOnce sync.Once
	cleanupErr  error

	sessionID string
	transport *sourceTransport
	account   sourceAccount
	published chan struct{}

	root    processIdentity
	rootPin pinnedProcess
	peerPin pinnedProcess
	claimed bool
}

func (a *Agent) handleSourceConnection(
	ctx context.Context,
	reader io.Reader,
	conn net.Conn,
	deadline time.Time,
) {
	bootstrap, err := vmexecwire.DecodeSourceBootstrap(ctx, reader)
	if err != nil {
		return
	}
	// CRITICAL: Authentication happens before the session identity is consulted
	// or inserted, so an unauthenticated peer cannot reserve agent state.
	if subtle.ConstantTimeCompare([]byte(bootstrap.AgentToken), []byte(a.token)) != 1 {
		return
	}

	transport := newSourceTransport(conn)
	transport.deadline = deadline
	if !a.registerPendingSource(bootstrap.SourceSessionID, transport) {
		return
	}
	if err := vmexecwire.EncodeSourceReady(ctx, conn, vmexecwire.SourceReady{
		SourceSessionID: bootstrap.SourceSessionID,
	}); err != nil {
		a.removePendingSource(bootstrap.SourceSessionID, transport)
		transport.close()
		return
	}
	transport.startDisconnectWatch(ctx)

	watchDone := transport.watchDone
	for {
		select {
		case <-ctx.Done():
			transport.close()
			watchDone = nil
		case <-transport.done:
			a.removePendingSource(bootstrap.SourceSessionID, transport)
			return
		case <-watchDone:
			watchDone = nil
			if transport.watchErr != nil {
				a.disconnectSourceTransport(bootstrap.SourceSessionID, transport)
				return
			}
		}
	}
}

func (a *Agent) disconnectSourceTransport(sessionID string, transport *sourceTransport) {
	a.sourceMu.Lock()
	binding := a.sourceBindings[sessionID]
	if binding != nil && binding.transport != transport {
		binding = nil
	}
	if pending := a.pendingSources[sessionID]; pending == transport {
		delete(a.pendingSources, sessionID)
		a.notifySourceChangeLocked()
	}
	a.sourceMu.Unlock()
	if binding != nil {
		a.cleanupSourceBinding(binding)
		return
	}
	transport.close()
}

func (a *Agent) registerPendingSource(sessionID string, transport *sourceTransport) bool {
	a.sourceMu.Lock()
	defer a.sourceMu.Unlock()
	a.ensureSourceStateLocked()
	if _, exists := a.pendingSources[sessionID]; exists {
		return false
	}
	if _, exists := a.sourceBindings[sessionID]; exists {
		return false
	}
	a.pendingSources[sessionID] = transport
	a.notifySourceChangeLocked()
	return true
}

func (a *Agent) removePendingSource(sessionID string, transport *sourceTransport) {
	a.sourceMu.Lock()
	defer a.sourceMu.Unlock()
	if current, exists := a.pendingSources[sessionID]; exists && current == transport {
		delete(a.pendingSources, sessionID)
		a.notifySourceChangeLocked()
	}
}

func (a *Agent) reserveSourceSession(
	ctx context.Context,
	sessionID string,
	user string,
) (*sourceBinding, error) {
	if err := (vmexecwire.SourceReady{SourceSessionID: sessionID}).Validate(); err != nil {
		return nil, err
	}
	accountName := user
	if accountName == "" {
		accountName = "root"
	}
	account, err := a.sourceProcesses.ResolveAccount(ctx, accountName)
	if err != nil {
		return nil, fmt.Errorf("resolve source-session user: %w", err)
	}

	a.sourceMu.Lock()
	defer a.sourceMu.Unlock()
	a.ensureSourceStateLocked()
	transport, exists := a.pendingSources[sessionID]
	if !exists {
		return nil, fmt.Errorf("source session is not pending")
	}
	delete(a.pendingSources, sessionID)
	binding := &sourceBinding{
		sessionID: sessionID,
		transport: transport,
		account:   account,
		published: make(chan struct{}),
	}
	a.sourceBindings[sessionID] = binding
	a.notifySourceChangeLocked()
	return binding, nil
}

func (a *Agent) publishSourceBinding(
	ctx context.Context,
	binding *sourceBinding,
	pid int,
	pidFD int,
) (bool, error) {
	rootPin, err := a.sourceProcesses.AdoptSourceCommand(ctx, pid, pidFD)
	if err != nil {
		return false, fmt.Errorf("pin source command: %w", err)
	}
	root, exited, err := observeStablePinnedIdentity(ctx, a.sourceProcesses, rootPin, pid)
	if err != nil {
		return false, errors.Join(fmt.Errorf("identify source command: %w", err), rootPin.Close())
	}

	a.sourceMu.Lock()
	current, exists := a.sourceBindings[binding.sessionID]
	if !exists || current != binding || binding.rootPin != nil || binding.claimed {
		a.sourceMu.Unlock()
		return false, errors.Join(fmt.Errorf("source command binding is no longer reserved"), rootPin.Close())
	}
	select {
	case <-binding.published:
		a.sourceMu.Unlock()
		return false, errors.Join(fmt.Errorf("source command binding is already published"), rootPin.Close())
	default:
	}
	select {
	case <-binding.transport.done:
		a.sourceMu.Unlock()
		return false, errors.Join(fmt.Errorf("source command transport is closed"), rootPin.Close())
	default:
	}
	if err := ctx.Err(); err != nil {
		a.sourceMu.Unlock()
		return false, errors.Join(err, rootPin.Close())
	}
	if exited {
		// CRITICAL: Completion only retires an unused session. It never publishes process authority.
		delete(a.sourceBindings, binding.sessionID)
		a.notifySourceChangeLocked()
		a.sourceMu.Unlock()
		if err := errors.Join(rootPin.Close(), a.cleanupSourceBinding(binding), ctx.Err()); err != nil {
			return false, err
		}
		return true, nil
	}
	binding.root = root
	binding.rootPin = rootPin
	close(binding.published)
	a.notifySourceChangeLocked()
	a.sourceMu.Unlock()
	return false, nil
}

func stablePinnedIdentity(
	ctx context.Context,
	effects sourceProcessEffects,
	pin pinnedProcess,
	pid int,
) (processIdentity, error) {
	identity, exited, err := observeStablePinnedIdentity(ctx, effects, pin, pid)
	if err != nil {
		return processIdentity{}, err
	}
	if exited {
		return processIdentity{}, fmt.Errorf("process has exited or is a zombie")
	}
	return identity, nil
}

func observeStablePinnedIdentity(
	ctx context.Context,
	effects sourceProcessEffects,
	pin pinnedProcess,
	pid int,
) (processIdentity, bool, error) {
	if exited, err := pin.Exited(ctx); exited || err != nil {
		return processIdentity{}, exited, err
	}
	first, err := effects.ReadIdentity(ctx, pid)
	if err != nil {
		return processIdentity{}, false, err
	}
	if first.PID != pid {
		return processIdentity{}, false, fmt.Errorf("process identity PID mismatch")
	}
	if exited, err := pin.Exited(ctx); exited || err != nil {
		return processIdentity{}, exited, err
	}
	second, err := effects.ReadIdentity(ctx, pid)
	if err != nil {
		return processIdentity{}, false, err
	}
	if first != second {
		return processIdentity{}, false, fmt.Errorf("process identity changed during admission")
	}
	if exited, err := pin.Exited(ctx); exited || err != nil {
		return processIdentity{}, exited, err
	}
	return first, false, nil
}

func (a *Agent) cleanupSourceBinding(binding *sourceBinding) error {
	binding.cleanupOnce.Do(func() {
		a.sourceMu.Lock()
		if current, exists := a.sourceBindings[binding.sessionID]; exists && current == binding {
			delete(a.sourceBindings, binding.sessionID)
			a.notifySourceChangeLocked()
		}
		a.sourceMu.Unlock()

		if binding.peerPin != nil {
			binding.cleanupErr = errors.Join(binding.cleanupErr, binding.peerPin.Close())
		}
		if binding.rootPin != nil {
			binding.cleanupErr = errors.Join(binding.cleanupErr, binding.rootPin.Close())
		}
		binding.cleanupErr = errors.Join(binding.cleanupErr, binding.transport.close())
	})
	return binding.cleanupErr
}

func (a *Agent) closeAllSourceSessions() {
	a.sourceMu.Lock()
	a.ensureSourceStateLocked()
	pending := make([]*sourceTransport, 0, len(a.pendingSources))
	for _, transport := range a.pendingSources {
		pending = append(pending, transport)
	}
	bindings := make([]*sourceBinding, 0, len(a.sourceBindings))
	for _, binding := range a.sourceBindings {
		bindings = append(bindings, binding)
	}
	a.pendingSources = make(map[string]*sourceTransport)
	a.sourceBindings = make(map[string]*sourceBinding)
	a.notifySourceChangeLocked()
	a.sourceMu.Unlock()

	for _, transport := range pending {
		transport.close()
	}
	for _, binding := range bindings {
		a.cleanupSourceBinding(binding)
	}
}

func (a *Agent) ensureSourceStateLocked() {
	if a.pendingSources == nil {
		a.pendingSources = make(map[string]*sourceTransport)
	}
	if a.sourceBindings == nil {
		a.sourceBindings = make(map[string]*sourceBinding)
	}
	if a.sourceChanged == nil {
		a.sourceChanged = make(chan struct{})
	}
	if a.sourceAdmission == nil {
		a.sourceAdmission = make(chan struct{}, maxSourceAdmissions)
	}
	if a.sourceProcesses == nil {
		a.sourceProcesses = newSourceProcessEffects()
	}
}

func (a *Agent) notifySourceChangeLocked() {
	close(a.sourceChanged)
	a.sourceChanged = make(chan struct{})
}
