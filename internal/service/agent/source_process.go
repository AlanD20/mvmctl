package agent

import (
	"context"
	"fmt"
	"net"
	"slices"
)

const maxSourceParentDepth = 64

type sourceAccount struct {
	UID uint32
	GID uint32
}

type peerCredentials struct {
	PID int
	UID uint32
	GID uint32
}

type processIdentity struct {
	PID       int
	ParentPID int
	StartTime uint64
}

type pinnedProcess interface {
	Alive(ctx context.Context) error
	Exited(ctx context.Context) (bool, error)
	Close() error
}

type sourceProcessEffects interface {
	ResolveAccount(ctx context.Context, username string) (sourceAccount, error)
	PeerCredentials(ctx context.Context, conn net.Conn) (peerCredentials, pinnedProcess, error)
	AdoptProcess(ctx context.Context, pid int, pidFD int) (pinnedProcess, error)
	AdoptSourceCommand(ctx context.Context, pid int, pidFD int) (pinnedProcess, error)
	ReadIdentity(ctx context.Context, pid int) (processIdentity, error)
}

func (a *Agent) claimSourceForPeer(ctx context.Context, conn net.Conn) (*sourceBinding, error) {
	credentials, peerPin, err := a.sourceProcesses.PeerCredentials(ctx, conn)
	if err != nil {
		return nil, fmt.Errorf("read local peer credentials: %w", err)
	}
	claimed := false
	defer func() {
		if !claimed {
			// A rejected peer has no later owner for its atomically acquired pidfd.
			_ = peerPin.Close()
		}
	}()
	if _, err := stablePinnedIdentity(ctx, a.sourceProcesses, peerPin, credentials.PID); err != nil {
		return nil, fmt.Errorf("identify local peer: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, sourcePublicationWait)
	defer cancel()
	for {
		bindings, unpublished, changed := a.sourceBindingSnapshot()
		matches := make([]*sourceBinding, 0, 1)
		for _, binding := range bindings {
			if credentials.UID != binding.account.UID || credentials.GID != binding.account.GID {
				continue
			}
			match, matchErr := processDescendsFrom(
				waitCtx,
				a.sourceProcesses,
				peerPin,
				credentials.PID,
				binding.rootPin,
				binding.root,
			)
			if matchErr != nil {
				return nil, matchErr
			}
			if match {
				matches = append(matches, binding)
			}
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf("local peer matches multiple source command bindings")
		}
		if len(matches) == 1 {
			binding := matches[0]
			a.sourceMu.Lock()
			current, exists := a.sourceBindings[binding.sessionID]
			if !exists || current != binding || binding.claimed {
				a.sourceMu.Unlock()
				return nil, fmt.Errorf("source command binding was already claimed")
			}
			binding.claimed = true
			binding.peerPin = peerPin
			a.notifySourceChangeLocked()
			a.sourceMu.Unlock()
			claimed = true
			return binding, nil
		}
		if !unpublished {
			return nil, fmt.Errorf("local peer is not a descendant of one live source command")
		}
		select {
		case <-waitCtx.Done():
			return nil, fmt.Errorf("wait for source command publication: %w", waitCtx.Err())
		case <-changed:
		}
	}
}

func (a *Agent) sourceBindingSnapshot() ([]*sourceBinding, bool, <-chan struct{}) {
	a.sourceMu.Lock()
	defer a.sourceMu.Unlock()
	a.ensureSourceStateLocked()
	published := make([]*sourceBinding, 0, len(a.sourceBindings))
	unpublished := false
	for _, binding := range a.sourceBindings {
		select {
		case <-binding.published:
			if !binding.claimed {
				published = append(published, binding)
			}
		default:
			unpublished = true
		}
	}
	return published, unpublished, a.sourceChanged
}

func processDescendsFrom(
	ctx context.Context,
	effects sourceProcessEffects,
	peerPin pinnedProcess,
	peerPID int,
	rootPin pinnedProcess,
	root processIdentity,
) (bool, error) {
	if peerPin == nil || rootPin == nil {
		return false, fmt.Errorf("source process pins are required")
	}
	if err := peerPin.Alive(ctx); err != nil {
		return false, fmt.Errorf("local peer is not live: %w", err)
	}
	if err := rootPin.Alive(ctx); err != nil {
		return false, fmt.Errorf("source command is not live: %w", err)
	}
	first, firstMatch, err := sourceParentPath(ctx, effects, peerPID, root)
	if err != nil {
		return false, err
	}
	if err := peerPin.Alive(ctx); err != nil {
		return false, fmt.Errorf("local peer died during admission: %w", err)
	}
	if err := rootPin.Alive(ctx); err != nil {
		return false, fmt.Errorf("source command died during admission: %w", err)
	}
	second, secondMatch, err := sourceParentPath(ctx, effects, peerPID, root)
	if err != nil {
		return false, err
	}
	if err := peerPin.Alive(ctx); err != nil {
		return false, fmt.Errorf("local peer died before claim: %w", err)
	}
	if err := rootPin.Alive(ctx); err != nil {
		return false, fmt.Errorf("source command died before claim: %w", err)
	}
	if !slices.Equal(first, second) || firstMatch != secondMatch {
		return false, fmt.Errorf("local peer process ancestry changed during admission")
	}
	return firstMatch, nil
}

func sourceParentPath(
	ctx context.Context,
	effects sourceProcessEffects,
	peerPID int,
	root processIdentity,
) ([]processIdentity, bool, error) {
	path := make([]processIdentity, 0, 4)
	seen := make(map[int]struct{})
	pid := peerPID
	for range maxSourceParentDepth {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if pid <= 0 {
			return nil, false, fmt.Errorf("local peer ancestry contains an invalid PID")
		}
		if _, exists := seen[pid]; exists {
			return nil, false, fmt.Errorf("local peer ancestry contains a cycle")
		}
		seen[pid] = struct{}{}
		identity, err := effects.ReadIdentity(ctx, pid)
		if err != nil {
			return nil, false, fmt.Errorf("read process identity for PID %d: %w", pid, err)
		}
		if identity.PID != pid || identity.StartTime == 0 || identity.ParentPID < 0 {
			return nil, false, fmt.Errorf("process identity for PID %d is malformed", pid)
		}
		path = append(path, identity)
		if identity.PID == root.PID && identity.StartTime == root.StartTime {
			return path, true, nil
		}
		if identity.ParentPID <= 1 {
			return path, false, nil
		}
		pid = identity.ParentPID
	}
	return nil, false, fmt.Errorf("local peer ancestry exceeds %d processes", maxSourceParentDepth)
}
