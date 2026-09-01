package agent

import (
	"context"
	"crypto/subtle"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"mvmctl/internal/lib/vmexecwire"
)

const sourcePublicationWait = 2 * time.Second

type sourceTransport struct {
	conn      net.Conn
	done      chan struct{}
	closeOnce sync.Once
}

func newSourceTransport(conn net.Conn) *sourceTransport {
	return &sourceTransport{conn: conn, done: make(chan struct{})}
}

func (transport *sourceTransport) close() {
	transport.closeOnce.Do(func() {
		_ = transport.conn.Close()
		close(transport.done)
	})
}

type sourceBinding struct {
	cleanupOnce sync.Once

	sessionID string
	transport *sourceTransport
	account   sourceAccount
	published chan struct{}

	root    processIdentity
	rootPin pinnedProcess
	peerPin pinnedProcess
	claimed bool
}

func (a *Agent) handleSourceConnection(ctx context.Context, reader io.Reader, conn net.Conn) {
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

	select {
	case <-ctx.Done():
		transport.close()
	case <-transport.done:
	}
	a.removePendingSource(bootstrap.SourceSessionID, transport)
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
	if user == "" || user == "root" {
		return nil, fmt.Errorf("source session requires an ordinary exec as a non-root user")
	}
	account, err := a.sourceProcesses.ResolveAccount(ctx, user)
	if err != nil {
		return nil, fmt.Errorf("resolve source-session user: %w", err)
	}
	if account.UID == 0 {
		return nil, fmt.Errorf("source session requires a non-root user")
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
) error {
	rootPin, err := a.sourceProcesses.Pin(ctx, pid)
	if err != nil {
		return fmt.Errorf("pin source command: %w", err)
	}
	root, err := stablePinnedIdentity(ctx, a.sourceProcesses, rootPin, pid)
	if err != nil {
		_ = rootPin.Close()
		return fmt.Errorf("identify source command: %w", err)
	}

	a.sourceMu.Lock()
	defer a.sourceMu.Unlock()
	current, exists := a.sourceBindings[binding.sessionID]
	if !exists || current != binding || binding.rootPin != nil {
		_ = rootPin.Close()
		return fmt.Errorf("source command binding is no longer reserved")
	}
	binding.root = root
	binding.rootPin = rootPin
	close(binding.published)
	a.notifySourceChangeLocked()
	return nil
}

func stablePinnedIdentity(
	ctx context.Context,
	effects sourceProcessEffects,
	pin pinnedProcess,
	pid int,
) (processIdentity, error) {
	if err := pin.Alive(ctx); err != nil {
		return processIdentity{}, err
	}
	first, err := effects.ReadIdentity(ctx, pid)
	if err != nil {
		return processIdentity{}, err
	}
	if first.PID != pid {
		return processIdentity{}, fmt.Errorf("process identity PID mismatch")
	}
	if err := pin.Alive(ctx); err != nil {
		return processIdentity{}, err
	}
	second, err := effects.ReadIdentity(ctx, pid)
	if err != nil {
		return processIdentity{}, err
	}
	if first != second {
		return processIdentity{}, fmt.Errorf("process identity changed during admission")
	}
	return first, nil
}

func (a *Agent) cleanupSourceBinding(binding *sourceBinding) {
	binding.cleanupOnce.Do(func() {
		a.sourceMu.Lock()
		if current, exists := a.sourceBindings[binding.sessionID]; exists && current == binding {
			delete(a.sourceBindings, binding.sessionID)
			a.notifySourceChangeLocked()
		}
		a.sourceMu.Unlock()

		if binding.peerPin != nil {
			_ = binding.peerPin.Close()
		}
		if binding.rootPin != nil {
			_ = binding.rootPin.Close()
		}
		binding.transport.close()
	})
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
	if a.sourceProcesses == nil {
		a.sourceProcesses = newSourceProcessEffects()
	}
}

func (a *Agent) notifySourceChangeLocked() {
	close(a.sourceChanged)
	a.sourceChanged = make(chan struct{})
}
