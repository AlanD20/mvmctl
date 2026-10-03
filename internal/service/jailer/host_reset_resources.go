package jailer

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

// openResetDirectory never creates a resource hierarchy. A missing child is proved
// relative to its retained root-owned parent; other errors never mean absence.
func (guard *HostResetGuard) openResetDirectory(
	ctx context.Context,
	parent int,
	name string,
	mode uint32,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, instanceAtomicError("open host reset resource directory", err)
	}
	fd, err := unix.Openat2(parent, name, &unix.OpenHow{
		Flags:   instanceDirectoryFlags,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if errors.Is(err, unix.ENOENT) {
		return -1, nil
	}
	if err != nil {
		return -1, instanceAtomicError("open fixed host reset resource directory", err)
	}
	guard.state.retained = append(guard.state.retained, fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return -1, instanceAtomicError("inspect host reset resource directory", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != 0 || stat.Gid != 0 || stat.Mode&0o022 != 0 ||
		stat.Ino == 0 ||
		stat.Nlink == 0 {
		return -1, instanceAtomicError("host reset resource directory metadata is unsafe", nil)
	}
	if mode != 0 && stat.Mode&0o7777 != mode {
		return -1, instanceAtomicError("host reset resource directory mode is not exact", nil)
	}
	return fd, nil
}

func (guard *HostResetGuard) requireResetDirectoryEmpty(ctx context.Context, fd int, description string) error {
	names, err := guard.state.roots.deps.readDirNames(ctx, fd)
	if err != nil {
		return instanceAtomicError("enumerate "+description, err)
	}
	if len(names) != 0 {
		return instanceAtomicError(description+" contains residual or unknown VM resources", nil)
	}
	return nil
}

func (guard *HostResetGuard) requireNoJailResources(ctx context.Context) error {
	parent := guard.state.roots.retained[0]
	for index, name := range []string{"var", "lib", "mvmctl", "jailer"} {
		mode := uint32(0)
		if index >= 2 {
			mode = 0755
		}
		if index == 3 {
			// ADR-0016 keeps the Jailer base private after per-VM cleanup.
			mode = 0700
		}
		fd, err := guard.openResetDirectory(ctx, parent, name, mode)
		if err != nil {
			return err
		}
		if fd < 0 {
			return nil
		}
		parent = fd
	}
	names, err := guard.state.roots.deps.readDirNames(ctx, parent)
	if err != nil {
		return instanceAtomicError("enumerate fixed Jailer root", err)
	}
	for _, name := range names {
		if name != "firecracker" {
			return instanceAtomicError("host reset found an unknown Jailer root entry", nil)
		}
		fd, err := guard.openResetDirectory(ctx, parent, name, 0700)
		if err != nil {
			return err
		}
		if fd < 0 {
			return instanceAtomicError("Jailer root entry disappeared during reset admission", nil)
		}
		if err := guard.requireResetDirectoryEmpty(ctx, fd, "fixed Firecracker jail root"); err != nil {
			return err
		}
	}
	return nil
}

func (guard *HostResetGuard) requireNoRuntimeResources(ctx context.Context) error {
	if err := guard.requireNoPrivateRuntimeResources(ctx); err != nil {
		return err
	}
	parent := guard.state.roots.retained[0]
	for index, name := range []string{"run", "mvmctl", "runtime"} {
		mode := uint32(0)
		if index > 0 {
			mode = managedVMRuntimeParentMode
		}
		fd, err := guard.openResetDirectory(ctx, parent, name, mode)
		if err != nil {
			return err
		}
		if fd < 0 {
			return nil
		}
		parent = fd
	}
	names, err := guard.state.roots.deps.readDirNames(ctx, parent)
	if err != nil {
		return instanceAtomicError("enumerate managed runtime owners", err)
	}
	for _, name := range names {
		if _, valid := validateCanonicalUIDName(name); !valid {
			return instanceAtomicError("host reset found a noncanonical runtime owner", nil)
		}
		fd, err := guard.openResetDirectory(ctx, parent, name, managedVMRuntimeParentMode)
		if err != nil {
			return err
		}
		if fd < 0 {
			return instanceAtomicError("runtime owner disappeared during reset admission", nil)
		}
		if err := guard.requireResetDirectoryEmpty(ctx, fd, "managed runtime owner"); err != nil {
			return err
		}
	}
	return nil
}

func (guard *HostResetGuard) requireNoPrivateRuntimeResources(ctx context.Context) error {
	parent := guard.state.roots.retained[0]
	for index, name := range []string{"run", "mvmctl", "vmm-runtime"} {
		mode := uint32(0)
		if index == 1 {
			mode = managedVMRuntimeParentMode
		}
		if index == 2 {
			mode = 0700
		}
		fd, err := guard.openResetDirectory(ctx, parent, name, mode)
		if err != nil {
			return err
		}
		if fd < 0 {
			return nil
		}
		parent = fd
	}
	names, err := guard.state.roots.deps.readDirNames(ctx, parent)
	if err != nil {
		return instanceAtomicError("enumerate private runtime owners", err)
	}
	for _, name := range names {
		if _, valid := validateCanonicalUIDName(name); !valid {
			return instanceAtomicError("private runtime owner is not canonical", nil)
		}
		fd, err := guard.openResetDirectory(ctx, parent, name, 0700)
		if err != nil {
			return err
		}
		if fd < 0 {
			return instanceAtomicError("private runtime owner disappeared", nil)
		}
		if err := guard.requireResetDirectoryEmpty(ctx, fd, "private VMM runtime owner"); err != nil {
			return err
		}
	}
	return nil
}

func (guard *HostResetGuard) requireNoCgroupResources(ctx context.Context) error {
	deps := realManagedVMCgroupDeps()
	readDeps := managedVMCgroupReadDepsFrom(deps)
	rootFD, err := deps.openRoot(ctx)
	if err != nil {
		return instanceAtomicError("open reset cgroup-v2 root", err)
	}
	guard.state.retained = append(guard.state.retained, rootFD)
	root, err := inspectManagedVMCgroupRoot(ctx, readDeps, rootFD, "reset cgroup-v2 root")
	if err != nil {
		return err
	}
	how := unix.OpenHow{Flags: managedVMCgroupDirectoryFlags, Resolve: managedVMCgroupResolveFlags}
	parentFD, err := deps.openAt2(ctx, rootFD, infra.JailerCgroupParent, &how)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return instanceAtomicError("open reset managed cgroup parent", err)
	}
	guard.state.retained = append(guard.state.retained, parentFD)
	parent, err := inspectManagedVMCgroupChild(
		ctx,
		readDeps,
		parentFD,
		"reset managed cgroup parent",
		managedVMCgroupParentMode,
		root,
	)
	if err != nil {
		return err
	}
	names, err := guard.state.roots.deps.readDirNames(ctx, parentFD)
	if err != nil {
		return instanceAtomicError("enumerate reset managed cgroup parent", err)
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return instanceAtomicError("scan reset managed cgroup parent", err)
		}
		fd, err := deps.openAt2(ctx, parentFD, name, &unix.OpenHow{
			Flags: unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: managedVMCgroupResolveFlags,
		})
		if err != nil {
			return instanceAtomicError("pin reset cgroup entry", err)
		}
		var stat unix.Stat_t
		inspectErr := unix.Fstat(fd, &stat)
		closeErr := deps.close(context.WithoutCancel(ctx), fd)
		if inspectErr != nil || closeErr != nil {
			return instanceAtomicError("inspect reset cgroup entry", errors.Join(inspectErr, closeErr))
		}
		// A genuine cgroup-v2 hierarchy exposes only kernel-created regular control
		// files. Every directory, including an empty or foreign-named one, blocks reset.
		if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != 0 || stat.Gid != 0 || stat.Mode&0o022 != 0 {
			return instanceAtomicError("host reset found a residual or foreign managed cgroup", nil)
		}
	}
	procsFD, err := deps.openAt2(ctx, parentFD, "cgroup.procs", &unix.OpenHow{
		Flags: unix.O_RDONLY | unix.O_NONBLOCK | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: managedVMCgroupResolveFlags,
	})
	if err != nil {
		return instanceAtomicError("open reset parent process membership", err)
	}
	guard.state.retained = append(guard.state.retained, procsFD)
	procs, err := inspectManagedVMCgroupRegularFile(
		ctx,
		deps,
		procsFD,
		"reset parent process membership",
		managedVMCgroupProcsMode,
	)
	if err != nil {
		return err
	}
	if procs.mount != parent.mount || procs.deviceMajor != parent.deviceMajor ||
		procs.deviceMinor != parent.deviceMinor {
		return instanceAtomicError("reset parent process membership is a substituted object", nil)
	}
	var one [1]byte
	count, err := deps.pread(ctx, procsFD, one[:], 0)
	if err != nil {
		return instanceAtomicError("read reset parent process membership", err)
	}
	if count != 0 {
		return instanceAtomicError("managed cgroup parent still contains processes", nil)
	}
	return nil
}
