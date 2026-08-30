package firewall

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

var generationRecordTempNamePattern = regexp.MustCompile(`^\.generation-[a-f0-9]{32}\.tmp$`)

const (
	generationRecordDirectoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	generationRecordReadFlags      = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	generationRecordTempFlags      = unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW

	generationRecordDirectoryMode = uint32(0700)
	generationRecordFileMode      = uint32(0600)
	generationRecordTempAttempts  = 8
	generationRecordIOChunk       = 1024
	generationRecordName          = "generation.json"

	generationRecordTraversalResolve = unix.RESOLVE_BENEATH |
		unix.RESOLVE_NO_SYMLINKS |
		unix.RESOLVE_NO_MAGICLINKS
	generationRecordManagedResolve = generationRecordTraversalResolve | unix.RESOLVE_NO_XDEV
)

type generationRecordStoreDeps struct {
	open            func(context.Context, string, int, uint32) (int, error)
	openAt2         func(context.Context, int, string, *unix.OpenHow) (int, error)
	mkdirAt         func(context.Context, int, string, uint32) error
	fstat           func(context.Context, int, *unix.Stat_t) error
	fchown          func(context.Context, int, int, int) error
	fchmod          func(context.Context, int, uint32) error
	pread           func(context.Context, int, []byte, int64) (int, error)
	write           func(context.Context, int, []byte) (int, error)
	fsync           func(context.Context, int) error
	close           func(context.Context, int) error
	renameNoReplace func(context.Context, int, string, int, string) error
	unlinkAt        func(context.Context, int, string, int) error
	randomName      func(context.Context) (string, error)
}

type generationRecordStorePolicy struct {
	rootPath    string
	expectedUID uint32
	expectedGID uint32
}

type generationRecordFileEvidence struct {
	device uint64
	inode  uint64
	mode   uint32
	uid    uint32
	gid    uint32
	nlink  uint64
	size   int64
	mtime  unix.Timespec
	ctime  unix.Timespec
}

type generationRecordStore struct {
	state *generationRecordStoreState
}

type generationRecordStoreState struct {
	// mu guards the retained descriptors and every lifecycle field below.
	mu         sync.Mutex
	deps       generationRecordStoreDeps
	policy     generationRecordStorePolicy
	directory  int
	retained   []int
	active     bool
	released   bool
	releaseErr error
}

func productionGenerationRecordStorePolicy() generationRecordStorePolicy {
	return generationRecordStorePolicy{rootPath: "/", expectedUID: 0, expectedGID: 0}
}

func openGenerationRecordStore(
	ctx context.Context,
	deps generationRecordStoreDeps,
	policy generationRecordStorePolicy,
) (_ *generationRecordStore, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, generationAuthorityError("open firewall generation record store", err, nil)
	}
	names, err := generationRecordPathComponents(infra.FirewallGenerationRoot)
	if err != nil {
		return nil, err
	}
	retained := make([]int, 0, len(names)+1)
	defer func() {
		if returnErr == nil {
			return
		}
		if err := closeGenerationRecordFDs(context.WithoutCancel(ctx), deps, retained); err != nil {
			returnErr = appendGenerationCleanupError(
				returnErr,
				"close rejected firewall generation store",
				err,
			)
		}
	}()

	rootFD, err := deps.open(ctx, policy.rootPath, generationRecordDirectoryFlags, 0)
	if err != nil {
		return nil, generationAuthorityError("open filesystem root for firewall generation record", err, nil)
	}
	retained = append(retained, rootFD)
	if err := verifyGenerationRecordDirectory(ctx, deps, rootFD, "filesystem root", policy, false); err != nil {
		return nil, err
	}

	parentFD := rootFD
	for index, name := range names {
		managed := index >= 2
		// CRITICAL: /var, /var/lib, and the managed mvmctl root may each be a deliberate local mount boundary.
		// RESOLVE_NO_XDEV begins only after mvmctl is pinned, so no later firewall authority component can cross it.
		resolve := uint64(generationRecordTraversalResolve)
		if index >= 3 {
			resolve = generationRecordManagedResolve
		}
		fd, openErr := openGenerationRecordDirectory(ctx, deps, parentFD, name, managed, resolve, policy)
		if openErr != nil {
			return nil, openErr
		}
		retained = append(retained, fd)
		parentFD = fd
	}
	return &generationRecordStore{state: &generationRecordStoreState{
		deps: deps, policy: policy, directory: parentFD, retained: retained, active: true,
	}}, nil
}

func generationRecordPathComponents(value string) ([4]string, error) {
	if !strings.HasPrefix(value, "/") || path.Clean(value) != value {
		return [4]string{}, generationAuthorityError("firewall generation root constant is not canonical", nil, nil)
	}
	names := strings.Split(strings.TrimPrefix(value, "/"), "/")
	if len(names) != 4 || names[0] != "var" || names[1] != "lib" || names[2] != "mvmctl" ||
		names[3] != "firewall" {
		return [4]string{}, generationAuthorityError(
			"firewall generation root constant has unexpected identity",
			nil,
			nil,
		)
	}
	return [4]string{names[0], names[1], names[2], names[3]}, nil
}

func openGenerationRecordDirectory(
	ctx context.Context,
	deps generationRecordStoreDeps,
	parentFD int,
	name string,
	managed bool,
	resolve uint64,
	policy generationRecordStorePolicy,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, generationAuthorityError("open firewall generation directory "+name, err, nil)
	}
	how := &unix.OpenHow{Flags: generationRecordDirectoryFlags, Resolve: resolve}
	fd, err := deps.openAt2(ctx, parentFD, name, how)
	if err == nil {
		if verifyErr := verifyGenerationRecordDirectory(ctx, deps, fd, name, policy, managed); verifyErr != nil {
			return -1, closeRejectedGenerationRecordFD(ctx, deps, fd, verifyErr)
		}
		return fd, nil
	}
	if !managed || !errors.Is(err, unix.ENOENT) {
		return -1, generationAuthorityError("open firewall generation directory "+name, err, nil)
	}
	if err := ctx.Err(); err != nil {
		return -1, generationAuthorityError("create firewall generation directory "+name, err, nil)
	}
	mkdirErr := deps.mkdirAt(ctx, parentFD, name, generationRecordDirectoryMode)
	created := mkdirErr == nil
	if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
		return -1, generationAuthorityError("create firewall generation directory "+name, mkdirErr, nil)
	}
	fd, err = deps.openAt2(ctx, parentFD, name, how)
	if err != nil {
		return -1, generationAuthorityError("open created firewall generation directory "+name, err, nil)
	}
	if !created {
		if verifyErr := verifyGenerationRecordDirectory(ctx, deps, fd, name, policy, true); verifyErr != nil {
			return -1, closeRejectedGenerationRecordFD(ctx, deps, fd, verifyErr)
		}
		return fd, nil
	}
	if err := deps.fchown(ctx, fd, int(policy.expectedUID), int(policy.expectedGID)); err != nil {
		return -1, closeRejectedGenerationRecordFD(
			ctx,
			deps,
			fd,
			generationAuthorityError("set firewall generation directory owner "+name, err, nil),
		)
	}
	if err := deps.fchmod(ctx, fd, generationRecordDirectoryMode); err != nil {
		return -1, closeRejectedGenerationRecordFD(
			ctx,
			deps,
			fd,
			generationAuthorityError("set firewall generation directory mode "+name, err, nil),
		)
	}
	if err := verifyGenerationRecordDirectory(ctx, deps, fd, name, policy, true); err != nil {
		return -1, closeRejectedGenerationRecordFD(ctx, deps, fd, err)
	}
	if err := deps.fsync(ctx, fd); err != nil {
		return -1, closeRejectedGenerationRecordFD(
			ctx,
			deps,
			fd,
			generationAuthorityError("sync created firewall generation directory "+name, err, nil),
		)
	}
	if err := deps.fsync(ctx, parentFD); err != nil {
		return -1, closeRejectedGenerationRecordFD(
			ctx,
			deps,
			fd,
			generationAuthorityError("sync parent of firewall generation directory "+name, err, nil),
		)
	}
	return fd, nil
}

func verifyGenerationRecordDirectory(
	ctx context.Context,
	deps generationRecordStoreDeps,
	fd int,
	name string,
	policy generationRecordStorePolicy,
	managed bool,
) error {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return generationAuthorityError("inspect firewall generation directory "+name, err, nil)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return generationAuthorityError("firewall generation path "+name+" is not a directory", nil, nil)
	}
	if stat.Uid != policy.expectedUID || stat.Mode&0022 != 0 {
		return generationAuthorityError("firewall generation directory "+name+" has unsafe owner or mode", nil, nil)
	}
	if managed && (stat.Gid != policy.expectedGID || stat.Mode&07777 != generationRecordDirectoryMode) {
		return generationAuthorityError(
			"managed firewall generation directory "+name+" has unexpected owner or mode",
			nil,
			nil,
		)
	}
	return nil
}

// Release closes the store descriptors in exact reverse order once while retaining values and ignoring cancellation.
func (store *generationRecordStore) Release(ctx context.Context) error {
	if store == nil || store.state == nil {
		return nil
	}
	state := store.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	if !state.active {
		return nil
	}
	err := closeGenerationRecordFDs(context.WithoutCancel(ctx), state.deps, state.retained)
	state.directory = -1
	state.retained = nil
	state.active = false
	state.released = true
	if err != nil {
		state.releaseErr = generationCleanupError("release firewall generation record store", err)
	}
	return state.releaseErr
}

func closeRejectedGenerationRecordFD(
	ctx context.Context,
	deps generationRecordStoreDeps,
	fd int,
	primary error,
) error {
	if err := deps.close(context.WithoutCancel(ctx), fd); err != nil {
		return appendGenerationCleanupError(primary, "close rejected firewall generation descriptor", err)
	}
	return primary
}

func closeGenerationRecordFDs(ctx context.Context, deps generationRecordStoreDeps, fds []int) error {
	var result error
	for index := len(fds) - 1; index >= 0; index-- {
		if err := deps.close(context.WithoutCancel(ctx), fds[index]); err != nil {
			result = errors.Join(result, fmt.Errorf("descriptor %d: %w", fds[index], err))
		}
	}
	return result
}

func randomGenerationRecordName(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return ".generation-" + hex.EncodeToString(value[:]) + ".tmp", nil
}

func realGenerationRecordStoreDeps() generationRecordStoreDeps {
	return generationRecordStoreDeps{
		open: func(_ context.Context, name string, flags int, mode uint32) (int, error) {
			return unix.Open(name, flags, mode)
		},
		openAt2: func(_ context.Context, parentFD int, name string, how *unix.OpenHow) (int, error) {
			return unix.Openat2(parentFD, name, how)
		},
		mkdirAt: func(_ context.Context, parentFD int, name string, mode uint32) error {
			return unix.Mkdirat(parentFD, name, mode)
		},
		fstat:  func(_ context.Context, fd int, stat *unix.Stat_t) error { return unix.Fstat(fd, stat) },
		fchown: func(_ context.Context, fd, uid, gid int) error { return unix.Fchown(fd, uid, gid) },
		fchmod: func(_ context.Context, fd int, mode uint32) error { return unix.Fchmod(fd, mode) },
		pread: func(_ context.Context, fd int, value []byte, offset int64) (int, error) {
			return unix.Pread(fd, value, offset)
		},
		write: func(_ context.Context, fd int, value []byte) (int, error) { return unix.Write(fd, value) },
		fsync: func(_ context.Context, fd int) error { return unix.Fsync(fd) },
		close: func(_ context.Context, fd int) error { return unix.Close(fd) },
		renameNoReplace: func(_ context.Context, oldFD int, oldName string, newFD int, newName string) error {
			return unix.Renameat2(oldFD, oldName, newFD, newName, unix.RENAME_NOREPLACE)
		},
		unlinkAt: func(_ context.Context, parentFD int, name string, flags int) error {
			return unix.Unlinkat(parentFD, name, flags)
		},
		randomName: randomGenerationRecordName,
	}
}
