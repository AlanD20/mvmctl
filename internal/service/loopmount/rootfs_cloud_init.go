package loopmount

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

type rootfsCloudInitSeed struct {
	metadata      []byte
	userData      []byte
	networkConfig []byte
	hasNetwork    bool
}

// readRootfsCloudInitSeed snapshots only the three fixed seed leaves beneath the already
// admitted VM directory. No caller path or seed executable crosses the privileged boundary.
func readRootfsCloudInitSeed(
	ctx context.Context,
	deps rootfsVMProvisionDeps,
	lease *rootfsLease,
) (_ *rootfsCloudInitSeed, returnErr error) {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.released || lease.parentFD < 0 || lease.access != rootfsReadWrite {
		return nil, rootfsAtomicError("cloud-init VM resource authority is inactive", nil)
	}
	directory, err := deps.openAt2(ctx, lease.parentFD, "cloud-init", &unix.OpenHow{
		Flags: rootfsGuestDirectoryFlags, Resolve: rootfsDescendantResolveFlags,
	})
	if err != nil {
		return nil, rootfsAtomicError("open fixed VM cloud-init seed directory", err)
	}
	defer func() {
		returnErr = appendRootfsCleanupError(returnErr, "close VM cloud-init seed directory", deps.close(context.WithoutCancel(ctx), directory))
	}()
	var directoryStat unix.Stat_t
	if err := deps.fstat(ctx, directory, &directoryStat); err != nil {
		return nil, rootfsAtomicError("inspect VM cloud-init seed directory", err)
	}
	if directoryStat.Mode&unix.S_IFMT != unix.S_IFDIR || directoryStat.Uid != lease.caller.UID ||
		directoryStat.Mode&07000 != 0 || directoryStat.Mode&0022 != 0 || directoryStat.Mode&0500 != 0500 {
		return nil, rootfsAtomicError("VM cloud-init seed directory has unsafe ownership or mode", nil)
	}
	seed := &rootfsCloudInitSeed{}
	remaining := maxRootfsAgentBytes
	for _, leaf := range []string{"meta-data", "user-data", "network-config"} {
		content, exists, err := readRootfsCloudInitLeaf(ctx, deps, directory, lease.caller.UID, leaf, remaining)
		if err != nil {
			return nil, err
		}
		if !exists && leaf != "network-config" {
			return nil, rootfsAtomicError("required VM cloud-init seed leaf is missing", nil)
		}
		remaining -= len(content)
		switch leaf {
		case "meta-data":
			seed.metadata = content
		case "user-data":
			seed.userData = content
		case "network-config":
			seed.networkConfig, seed.hasNetwork = content, exists
		}
	}
	return seed, nil
}

func readRootfsCloudInitLeaf(
	ctx context.Context,
	deps rootfsVMProvisionDeps,
	directory int,
	uid uint32,
	leaf string,
	limit int,
) (_ []byte, _ bool, returnErr error) {
	fd, err := deps.openAt2(ctx, directory, leaf, &unix.OpenHow{
		Flags: rootfsGuestReadFlags, Resolve: rootfsDescendantResolveFlags,
	})
	if errors.Is(err, unix.ENOENT) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, rootfsAtomicError("open fixed VM cloud-init seed leaf", err)
	}
	defer func() {
		returnErr = appendRootfsCleanupError(returnErr, "close VM cloud-init seed leaf", deps.close(context.WithoutCancel(ctx), fd))
	}()
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return nil, false, rootfsAtomicError("inspect VM cloud-init seed leaf", err)
	}
	if stat.Uid != uid || stat.Mode&0400 == 0 || stat.Mode&07133 != 0 {
		return nil, false, rootfsAtomicError("VM cloud-init seed leaf has unsafe ownership or mode", nil)
	}
	content, err := readStableRootfsGuestFile(ctx, deps.guestTreeDeps(), fd, limit)
	if err != nil {
		return nil, false, err
	}
	return content, true, nil
}

func applyRootfsCloudInitSeed(ctx context.Context, tree rootfsGuestTree) (uint32, error) {
	seed := tree.cloudInit
	if seed == nil {
		return 0, rootfsAtomicError("VM cloud-init seed was not admitted", nil)
	}
	for _, path := range []string{"/var", "/var/lib", "/var/lib/cloud", "/var/lib/cloud/seed", "/var/lib/cloud/seed/nocloud-net"} {
		if _, err := tree.ensureDirectory(ctx, path, 0755, 0, 0); err != nil {
			return 0, err
		}
	}
	files := []rootfsProvisionFile{
		{path: "/var/lib/cloud/seed/nocloud-net/meta-data", content: seed.metadata, mode: 0644},
		{path: "/var/lib/cloud/seed/nocloud-net/user-data", content: seed.userData, mode: 0600},
	}
	if seed.hasNetwork {
		files = append(files, rootfsProvisionFile{
			path: "/var/lib/cloud/seed/nocloud-net/network-config", content: seed.networkConfig, mode: 0644,
		})
	}
	var written uint32
	for _, file := range files {
		changed, err := tree.replaceFile(ctx, file)
		if changed {
			written++
		}
		if err != nil {
			return written, err
		}
	}
	return written, nil
}
