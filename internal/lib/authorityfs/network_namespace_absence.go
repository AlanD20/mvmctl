package authorityfs

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// RequireNamespaceAbsent checks only the fixed VM namespace target beneath this root-owned UID.
// No existing leaf, including an unmounted placeholder, is adopted or removed.
func (uid *NetworkUID) RequireNamespaceAbsent(ctx context.Context, hold *TopologyHold) (returnErr error) {
	if ctx == nil || uid == nil || uid.state == nil || hold == nil {
		return errors.New("authorityfs: namespace absence requires retained UID and VM authority")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	owner, key, err := hold.Binding()
	if err != nil {
		return err
	}
	state := uid.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.active || state.ownerUID != owner {
		return errors.New("authorityfs: namespace absence owner binding is not active")
	}
	fd, err := state.deps.openAt2(ctx, state.fd, networkNamespaceDirectoryName, &unix.OpenHow{
		Flags: directoryFlags, Resolve: resolveManaged,
	})
	if errors.Is(err, unix.ENOENT) {
		_, _, err = hold.Binding()
		return err
	}
	if err != nil {
		return fmt.Errorf("authorityfs: inspect namespace absence directory: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, state.deps.close(context.WithoutCancel(ctx), fd))
	}()
	if _, err := inspectNetworkNamespaceDirectory(ctx, state.deps, fd, state.policy); err != nil {
		return err
	}
	leaf, err := state.deps.openAt2(ctx, fd, key.value, &unix.OpenHow{
		Flags: unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: resolveBeneath,
	})
	if err == nil {
		return errors.Join(errors.New("authorityfs: VM namespace target still exists"),
			state.deps.close(context.WithoutCancel(ctx), leaf))
	}
	if !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("authorityfs: inspect VM namespace absence: %w", err)
	}
	_, _, err = hold.Binding()
	return err
}
