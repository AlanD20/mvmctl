package authorityfs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// RequireHostResetNamespacesAbsent examines every fixed authority UID, including owners with no
// persistent network records. It creates and removes nothing and acquires no earlier VM lock.
// CALLER MUST HOLD: exclusive host admission, Jailer index, then network index throughout reset.
func (authority *Network) RequireHostResetNamespacesAbsent(ctx context.Context) error {
	if ctx == nil || authority == nil || authority.state == nil {
		return errors.New("authorityfs: host reset namespace inspection requires retained network authority")
	}
	state := authority.state
	state.mu.RLock()
	defer state.mu.RUnlock()
	if !state.active {
		return errors.New("authorityfs: host reset network authority is released")
	}
	if err := verifyDirectory(ctx, state.deps, state.authority, "network authority", state.policy, directoryMode, true); err != nil {
		return err
	}
	names, err := readHostResetNetworkNames(ctx, state.deps, state.authority)
	if err != nil {
		return err
	}
	for _, name := range names {
		switch name {
		case "index.lock", networkIndexLockName, firewallLockName, vmExecCapacityLockName:
			if err := inspectHostResetNetworkLock(ctx, state, state.authority, name); err != nil {
				return err
			}
			continue
		case "releases":
			fd, err := openHostResetNetworkDirectory(ctx, state, state.authority, name)
			if err != nil {
				return err
			}
			if err := state.deps.close(context.WithoutCancel(ctx), fd); err != nil {
				return err
			}
			continue
		}
		owner, err := strconv.ParseUint(name, 10, 32)
		if err != nil || owner == 0 || strconv.FormatUint(owner, 10) != name {
			return errors.New("authorityfs: host reset authority contains an unknown root entry")
		}
		if err := inspectHostResetNetworkOwner(ctx, state, name); err != nil {
			return err
		}
	}
	return nil
}

func inspectHostResetNetworkOwner(ctx context.Context, state *networkState, name string) (returnErr error) {
	fd, err := openHostResetNetworkDirectory(ctx, state, state.authority, name)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, state.deps.close(context.WithoutCancel(ctx), fd)) }()
	names, err := readHostResetNetworkNames(ctx, state.deps, fd)
	if err != nil {
		return err
	}
	for _, child := range names {
		switch child {
		case networkNamespaceDirectoryName, "loopmount":
			directory, err := openHostResetNetworkDirectory(ctx, state, fd, child)
			if err != nil {
				return err
			}
			var inspectErr error
			if child == networkNamespaceDirectoryName {
				var leaves []string
				leaves, inspectErr = readHostResetNetworkNames(ctx, state.deps, directory)
				if inspectErr == nil && len(leaves) != 0 {
					inspectErr = errors.New("authorityfs: host reset requires every VM namespace target to be absent")
				}
			}
			if err := errors.Join(inspectErr, state.deps.close(context.WithoutCancel(ctx), directory)); err != nil {
				return err
			}
		default:
			key := strings.TrimSuffix(child, ".lock")
			valid := child == key+".lock" && isLowerHex(key, vmKeyLength)
			if strings.HasPrefix(key, "network-") {
				valid = child == key+".lock" && isLowerHex(strings.TrimPrefix(key, "network-"), networkKeyLength)
			}
			if !valid {
				return errors.New("authorityfs: host reset authority owner contains an unknown entry")
			}
			if err := inspectHostResetNetworkLock(ctx, state, fd, child); err != nil {
				return err
			}
		}
	}
	return nil
}

func openHostResetNetworkDirectory(ctx context.Context, state *networkState, parent int, name string) (int, error) {
	fd, err := state.deps.openAt2(ctx, parent, name, &unix.OpenHow{Flags: directoryFlags, Resolve: resolveManaged})
	if err != nil {
		return -1, fmt.Errorf("authorityfs: open host reset namespace inventory directory: %w", err)
	}
	if err := verifyDirectory(ctx, state.deps, fd, name, state.policy, directoryMode, true); err != nil {
		return -1, closeRejectedFD(ctx, state.deps, fd, err)
	}
	return fd, nil
}

func inspectHostResetNetworkLock(ctx context.Context, state *networkState, parent int, name string) (returnErr error) {
	fd, err := state.deps.openAt2(ctx, parent, name, &unix.OpenHow{
		Flags: unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK, Resolve: resolveManaged,
	})
	if err != nil {
		return fmt.Errorf("authorityfs: open host reset lock metadata: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, state.deps.close(context.WithoutCancel(ctx), fd)) }()
	return verifyLock(ctx, state.deps, fd, name, state.policy)
}

func readHostResetNetworkNames(ctx context.Context, deps dependencies, parent int) (_ []string, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A fresh open file description avoids changing the retained capability's directory offset.
	fd, err := deps.openAt2(ctx, parent, ".", &unix.OpenHow{Flags: directoryFlags, Resolve: resolveManaged})
	if err != nil {
		return nil, fmt.Errorf("authorityfs: open host reset namespace inventory cursor: %w", err)
	}
	file := os.NewFile(uintptr(fd), "host-reset-network-inventory")
	if file == nil {
		return nil, errors.Join(errors.New("authorityfs: namespace inventory cursor is unavailable"),
			deps.close(context.WithoutCancel(ctx), fd))
	}
	defer func() { returnErr = errors.Join(returnErr, file.Close()) }()
	names, err := file.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("authorityfs: read host reset namespace inventory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return names, nil
}
