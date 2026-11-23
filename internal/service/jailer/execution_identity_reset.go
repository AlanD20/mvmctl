package jailer

import (
	"context"
	"errors"
	"strings"
	"sync"
)

type executionIdentityResetGuard struct {
	state *executionIdentityResetGuardState
}

type executionIdentityResetGuardState struct {
	// mu guards the retained allocator lock and close-once release across copied guards.
	mu         sync.Mutex
	store      *executionIdentityStore
	released   bool
	releaseErr error
}

// acquireExecutionIdentityResetGuard admits only absent or completely retired allocations.
// CALLER MUST HOLD: exclusive host admission and the global instance index, retained through reset.
// A stopped VM's retained account deliberately blocks reset until normal VM removal retires it.
func acquireExecutionIdentityResetGuard(ctx context.Context) (_ *executionIdentityResetGuard, returnErr error) {
	store, err := openExecutionIdentityStore(ctx, false)
	if err != nil {
		return nil, err
	}
	guard := &executionIdentityResetGuard{state: &executionIdentityResetGuardState{store: store}}
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, guard.Release(context.WithoutCancel(ctx)))
		}
	}()
	if store != nil {
		names, err := readInstanceDirectoryNames(ctx, store.directory)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if name == "allocator.lock" {
				continue
			}
			vmID, valid := strings.CutSuffix(name, ".json")
			if !valid || !vmIDPattern.MatchString(vmID) {
				return nil, instanceAtomicError("host reset found unresolved execution allocation entries", nil)
			}
			record, found, err := store.read(ctx, vmID)
			if err != nil || !found {
				return nil, instanceAtomicError("host reset execution reservation disappeared", err)
			}
			if record.Phase != executionIdentityRetired {
				return nil, instanceStateError(
					vmID,
					"host reset requires VM removal to retire every execution account; stopped or ambiguous allocations remain",
				)
			}
		}
	}
	// Account creation writes local host databases. Scan those even when the allocation tree
	// is absent so deleting authority metadata cannot make an orphaned account look quiescent.
	for _, database := range []string{"passwd", "group"} {
		raw, err := readExecutionIdentityAccountFile(ctx, database, false)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			row := strings.Split(line, ":")
			if isExecutionIdentityAccountName(row[0]) ||
				(database == "passwd" && len(row) == 7 && strings.HasPrefix(row[4], "mvmctl-execution-v1/")) {
				return nil, instanceAtomicError("host reset found an unretired or orphaned VM execution account", nil)
			}
		}
	}
	return guard, nil
}

func isExecutionIdentityAccountName(name string) bool {
	if len(name) != 30 || !strings.HasPrefix(name, "mvm-") {
		return false
	}
	for _, character := range name[4:] {
		if (character < 'a' || character > 'z') && (character < '2' || character > '7') {
			return false
		}
	}
	return true
}

func (guard *executionIdentityResetGuard) Release(ctx context.Context) error {
	if guard == nil || guard.state == nil {
		return nil
	}
	state := guard.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.released {
		if state.store != nil {
			state.releaseErr = state.store.close(processCleanupContext(ctx))
			state.store = nil
		}
		state.released = true
	}
	return state.releaseErr
}
