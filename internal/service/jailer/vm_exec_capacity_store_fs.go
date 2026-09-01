package jailer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const (
	vmExecCapacityStateRoot = "/var/lib/mvmctl/allocations/exec"

	vmExecCapacityDirectoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	vmExecCapacityReadFlags      = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	vmExecCapacityTempFlags      = unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW
	vmExecCapacityDirectoryMode  = uint32(0700)
	vmExecCapacityRecordMode     = uint32(0600)
	vmExecCapacityTempAttempts   = 8
	vmExecCapacityIOChunk        = 1024

	vmExecCapacityResolve  = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS
	vmExecCapacityStatMask = unix.STATX_TYPE | unix.STATX_MODE | unix.STATX_NLINK | unix.STATX_UID |
		unix.STATX_GID | unix.STATX_INO | unix.STATX_SIZE | unix.STATX_MTIME | unix.STATX_CTIME
)

var (
	vmExecCapacityRecordNamePattern = regexp.MustCompile(`^[a-f0-9]{32}\.json$`)
	vmExecCapacityTempNamePattern   = regexp.MustCompile(`^\.mvm-exec-[a-f0-9]{32}\.tmp$`)
)

type vmExecCapacityStoreDeps struct {
	open            func(context.Context, string, int, uint32) (int, error)
	openAt2         func(context.Context, int, string, *unix.OpenHow) (int, error)
	mkdirAt         func(context.Context, int, string, uint32) error
	statx           func(context.Context, int, int, int, *unix.Statx_t) error
	fchown          func(context.Context, int, int, int) error
	fchmod          func(context.Context, int, uint32) error
	read            func(context.Context, int, []byte) (int, error)
	write           func(context.Context, int, []byte) (int, error)
	fsync           func(context.Context, int) error
	close           func(context.Context, int) error
	renameNoReplace func(context.Context, int, string, int, string) error
	unlinkAt        func(context.Context, int, string, int) error
	randomTempName  func(context.Context) (string, error)
	readDirNames    func(context.Context, int) ([]string, error)
}

type vmExecCapacityStorePolicy struct {
	rootPath    string
	stateRoot   string
	expectedUID uint32
	expectedGID uint32
}

type vmExecCapacityStore struct {
	state *vmExecCapacityStoreState
}

type vmExecCapacityStoreState struct {
	// mu keeps every operation's descriptor snapshot live until that operation finishes. Release takes the write side
	// before closing descriptors, so an fd cannot be closed and reused underneath an in-flight operation.
	mu         sync.RWMutex
	deps       vmExecCapacityStoreDeps
	policy     vmExecCapacityStorePolicy
	stateFD    int
	retained   []int
	released   bool
	releaseErr error
}

type vmExecCapacityFileEvidence struct {
	deviceMajor uint32
	deviceMinor uint32
	inode       uint64
	mode        uint16
	links       uint32
	uid         uint32
	gid         uint32
	size        uint64
	mtime       unix.StatxTimestamp
	ctime       unix.StatxTimestamp
}

func productionVMExecCapacityStorePolicy() vmExecCapacityStorePolicy {
	return vmExecCapacityStorePolicy{
		rootPath: "/", stateRoot: vmExecCapacityStateRoot, expectedUID: 0, expectedGID: 0,
	}
}

func openVMExecCapacityStore(
	ctx context.Context,
	deps vmExecCapacityStoreDeps,
	policy vmExecCapacityStorePolicy,
) (_ *vmExecCapacityStore, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, vmExecCapacityEffectError("open VM-exec capacity store", err)
	}
	components, err := vmExecCapacityStorePathComponents(policy.stateRoot)
	if err != nil {
		return nil, err
	}
	retained := make([]int, 0, len(components)+1)
	defer func() {
		if returnErr == nil {
			return
		}
		if err := closeVMExecCapacityFDs(context.WithoutCancel(ctx), deps, retained); err != nil {
			returnErr = appendVMExecCapacityError(returnErr, "close rejected VM-exec capacity store", err)
		}
	}()
	rootFD, err := deps.open(ctx, policy.rootPath, vmExecCapacityDirectoryFlags, 0)
	if err != nil {
		return nil, vmExecCapacityEffectError("open filesystem root for VM-exec capacity", err)
	}
	retained = append(retained, rootFD)
	if err := verifyVMExecCapacityDirectory(ctx, deps, rootFD, "filesystem root", policy, false); err != nil {
		return nil, err
	}
	parentFD := rootFD
	for index, name := range components {
		managed := index >= 2
		fd, openErr := openVMExecCapacityDirectory(ctx, deps, parentFD, name, managed, policy)
		if openErr != nil {
			return nil, openErr
		}
		retained = append(retained, fd)
		parentFD = fd
	}
	return &vmExecCapacityStore{state: &vmExecCapacityStoreState{
		deps: deps, policy: policy, stateFD: parentFD, retained: retained,
	}}, nil
}

func vmExecCapacityStorePathComponents(value string) ([]string, error) {
	if !strings.HasPrefix(value, "/") || path.Clean(value) != value {
		return nil, vmExecCapacityCorruptError("vm-exec capacity root is not canonical", nil)
	}
	names := strings.Split(strings.TrimPrefix(value, "/"), "/")
	if len(names) != 5 {
		return nil, vmExecCapacityCorruptError("vm-exec capacity root has unexpected depth", nil)
	}
	for _, name := range names {
		if name == "" || name == "." || name == ".." {
			return nil, vmExecCapacityCorruptError("vm-exec capacity root has an unsafe component", nil)
		}
	}
	return names, nil
}

func openVMExecCapacityDirectory(
	ctx context.Context,
	deps vmExecCapacityStoreDeps,
	parentFD int,
	name string,
	managed bool,
	policy vmExecCapacityStorePolicy,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, vmExecCapacityEffectError("open VM-exec capacity directory", err)
	}
	resolve := uint64(vmExecCapacityResolve)
	if managed {
		resolve |= unix.RESOLVE_NO_XDEV
	}
	how := &unix.OpenHow{Flags: vmExecCapacityDirectoryFlags, Resolve: resolve}
	fd, err := deps.openAt2(ctx, parentFD, name, how)
	if err == nil {
		if verifyErr := verifyVMExecCapacityDirectory(ctx, deps, fd, name, policy, managed); verifyErr != nil {
			return -1, closeRejectedVMExecCapacityFD(ctx, deps, fd, verifyErr)
		}
		return fd, nil
	}
	if !managed || !errors.Is(err, unix.ENOENT) {
		return -1, vmExecCapacityEffectError("open VM-exec capacity directory "+name, err)
	}
	if err := ctx.Err(); err != nil {
		return -1, vmExecCapacityEffectError("create VM-exec capacity directory "+name, err)
	}
	mkdirErr := deps.mkdirAt(ctx, parentFD, name, vmExecCapacityDirectoryMode)
	created := mkdirErr == nil
	if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
		return -1, vmExecCapacityEffectError("create VM-exec capacity directory "+name, mkdirErr)
	}
	fd, err = deps.openAt2(ctx, parentFD, name, how)
	if err != nil {
		return -1, vmExecCapacityEffectError("open created VM-exec capacity directory "+name, err)
	}
	if !created {
		if verifyErr := verifyVMExecCapacityDirectory(ctx, deps, fd, name, policy, true); verifyErr != nil {
			return -1, closeRejectedVMExecCapacityFD(ctx, deps, fd, verifyErr)
		}
		return fd, nil
	}
	if err := deps.fchown(ctx, fd, int(policy.expectedUID), int(policy.expectedGID)); err != nil {
		return -1, closeRejectedVMExecCapacityFD(
			ctx, deps, fd, vmExecCapacityEffectError("set VM-exec capacity directory owner", err),
		)
	}
	if err := deps.fchmod(ctx, fd, vmExecCapacityDirectoryMode); err != nil {
		return -1, closeRejectedVMExecCapacityFD(
			ctx, deps, fd, vmExecCapacityEffectError("set VM-exec capacity directory mode", err),
		)
	}
	if err := verifyVMExecCapacityDirectory(ctx, deps, fd, name, policy, true); err != nil {
		return -1, closeRejectedVMExecCapacityFD(ctx, deps, fd, err)
	}
	if err := deps.fsync(ctx, fd); err != nil {
		return -1, closeRejectedVMExecCapacityFD(
			ctx, deps, fd, vmExecCapacityEffectError("sync created VM-exec capacity directory", err),
		)
	}
	if err := deps.fsync(ctx, parentFD); err != nil {
		return -1, closeRejectedVMExecCapacityFD(
			ctx, deps, fd, vmExecCapacityEffectError("sync VM-exec capacity parent directory", err),
		)
	}
	return fd, nil
}

func verifyVMExecCapacityDirectory(
	ctx context.Context,
	deps vmExecCapacityStoreDeps,
	fd int,
	description string,
	policy vmExecCapacityStorePolicy,
	exact bool,
) error {
	evidence, err := inspectVMExecCapacityFile(ctx, deps, fd)
	if err != nil {
		return vmExecCapacityEffectError("inspect VM-exec capacity directory "+description, err)
	}
	if evidence.mode&unix.S_IFMT != unix.S_IFDIR {
		return vmExecCapacityCorruptError("vm-exec capacity path is not a directory", nil)
	}
	if evidence.uid != policy.expectedUID || evidence.mode&0022 != 0 {
		return vmExecCapacityCorruptError("vm-exec capacity directory has unsafe ownership or mode", nil)
	}
	if exact && (evidence.gid != policy.expectedGID || evidence.mode&07777 != uint16(vmExecCapacityDirectoryMode)) {
		return vmExecCapacityCorruptError("managed VM-exec capacity directory has unexpected owner or mode", nil)
	}
	return nil
}

func inspectVMExecCapacityFile(
	ctx context.Context,
	deps vmExecCapacityStoreDeps,
	fd int,
) (vmExecCapacityFileEvidence, error) {
	var stat unix.Statx_t
	if err := deps.statx(
		ctx,
		fd,
		unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW,
		vmExecCapacityStatMask,
		&stat,
	); err != nil {
		return vmExecCapacityFileEvidence{}, err
	}
	if stat.Mask&vmExecCapacityStatMask != vmExecCapacityStatMask {
		return vmExecCapacityFileEvidence{}, errors.New("statx result is incomplete")
	}
	return vmExecCapacityFileEvidence{
		deviceMajor: stat.Dev_major,
		deviceMinor: stat.Dev_minor,
		inode:       stat.Ino,
		mode:        stat.Mode,
		links:       stat.Nlink,
		uid:         stat.Uid,
		gid:         stat.Gid,
		size:        stat.Size,
		mtime:       stat.Mtime,
		ctime:       stat.Ctime,
	}, nil
}

func verifyVMExecCapacityRecordFile(
	ctx context.Context,
	deps vmExecCapacityStoreDeps,
	fd int,
	policy vmExecCapacityStorePolicy,
) (vmExecCapacityFileEvidence, error) {
	evidence, err := inspectVMExecCapacityFile(ctx, deps, fd)
	if err != nil {
		return vmExecCapacityFileEvidence{}, vmExecCapacityEffectError("inspect VM-exec capacity record", err)
	}
	if evidence.mode&unix.S_IFMT != unix.S_IFREG || evidence.links != 1 {
		return vmExecCapacityFileEvidence{}, vmExecCapacityCorruptError(
			"vm-exec capacity record is not a one-link regular file",
			nil,
		)
	}
	if evidence.uid != policy.expectedUID || evidence.gid != policy.expectedGID ||
		evidence.mode&07777 != uint16(vmExecCapacityRecordMode) {
		return vmExecCapacityFileEvidence{}, vmExecCapacityCorruptError(
			"vm-exec capacity record has unexpected owner or mode",
			nil,
		)
	}
	if evidence.size == 0 || evidence.size > maxVMExecCapacityRecordBytes {
		return vmExecCapacityFileEvidence{}, vmExecCapacityCorruptError("vm-exec capacity record size is invalid", nil)
	}
	return evidence, nil
}

func sameVMExecCapacityFile(first, second vmExecCapacityFileEvidence) bool {
	return first == second
}

// Release closes the descriptors owned by the VM-exec capacity store exactly once and caches the first result.
func (store *vmExecCapacityStore) Release(ctx context.Context) error {
	if store == nil || store.state == nil {
		return nil
	}
	state := store.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	state.releaseErr = closeVMExecCapacityFDs(context.WithoutCancel(ctx), state.deps, state.retained)
	state.stateFD = -1
	state.retained = nil
	state.released = true
	if state.releaseErr != nil {
		state.releaseErr = vmExecCapacityEffectError("release VM-exec capacity store", state.releaseErr)
	}
	return state.releaseErr
}

func (store *vmExecCapacityStore) beginOperation() (
	vmExecCapacityStoreDeps,
	vmExecCapacityStorePolicy,
	int,
	func(),
	error,
) {
	if store == nil || store.state == nil {
		return vmExecCapacityStoreDeps{}, vmExecCapacityStorePolicy{}, -1, nil,
			vmExecCapacityCorruptError("vm-exec capacity store is unavailable", nil)
	}
	state := store.state
	state.mu.RLock()
	if state.released || state.stateFD < 0 {
		state.mu.RUnlock()
		return vmExecCapacityStoreDeps{}, vmExecCapacityStorePolicy{}, -1, nil,
			vmExecCapacityCorruptError("vm-exec capacity store is released", nil)
	}
	return state.deps, state.policy, state.stateFD, state.mu.RUnlock, nil
}

func closeRejectedVMExecCapacityFD(
	ctx context.Context,
	deps vmExecCapacityStoreDeps,
	fd int,
	primary error,
) error {
	if err := deps.close(context.WithoutCancel(ctx), fd); err != nil {
		return appendVMExecCapacityError(primary, "close rejected VM-exec capacity descriptor", err)
	}
	return primary
}

func closeVMExecCapacityFDs(ctx context.Context, deps vmExecCapacityStoreDeps, fds []int) error {
	var result error
	for index := len(fds) - 1; index >= 0; index-- {
		if err := deps.close(context.WithoutCancel(ctx), fds[index]); err != nil {
			result = errors.Join(result, fmt.Errorf("fd %d: %w", fds[index], err))
		}
	}
	return result
}

func randomVMExecCapacityTempName(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return ".mvm-exec-" + hex.EncodeToString(value[:]) + ".tmp", nil
}

func readVMExecCapacityDirectoryNames(ctx context.Context, fd int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	duplicateFD, err := unix.Openat2(fd, ".", &unix.OpenHow{
		Flags: vmExecCapacityDirectoryFlags, Resolve: vmExecCapacityResolve,
	})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(duplicateFD), "vm-exec-capacity-directory")
	if file == nil {
		return nil, errors.Join(errors.New("create VM-exec capacity directory reader"), unix.Close(duplicateFD))
	}
	entries, readErr := file.ReadDir(-1)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names, nil
}

func realVMExecCapacityStoreDeps() vmExecCapacityStoreDeps {
	return vmExecCapacityStoreDeps{
		open: func(_ context.Context, name string, flags int, mode uint32) (int, error) {
			return unix.Open(name, flags, mode)
		},
		openAt2: func(_ context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
			return unix.Openat2(parentFD, name, how)
		},
		mkdirAt: func(_ context.Context, parentFD int, name string, mode uint32) error {
			return unix.Mkdirat(parentFD, name, mode)
		},
		statx: func(_ context.Context, fd, flags, mask int, stat *unix.Statx_t) error {
			return unix.Statx(fd, "", flags, mask, stat)
		},
		fchown: func(_ context.Context, fd, uid, gid int) error { return unix.Fchown(fd, uid, gid) },
		fchmod: func(_ context.Context, fd int, mode uint32) error { return unix.Fchmod(fd, mode) },
		read:   func(_ context.Context, fd int, value []byte) (int, error) { return unix.Read(fd, value) },
		write:  func(_ context.Context, fd int, value []byte) (int, error) { return unix.Write(fd, value) },
		fsync:  func(_ context.Context, fd int) error { return unix.Fsync(fd) },
		close:  func(_ context.Context, fd int) error { return unix.Close(fd) },
		renameNoReplace: func(_ context.Context, oldFD int, oldName string, newFD int, newName string) error {
			return unix.Renameat2(oldFD, oldName, newFD, newName, unix.RENAME_NOREPLACE)
		},
		unlinkAt: func(_ context.Context, parentFD int, name string, flags int) error {
			return unix.Unlinkat(parentFD, name, flags)
		},
		randomTempName: randomVMExecCapacityTempName,
		readDirNames:   readVMExecCapacityDirectoryNames,
	}
}
