package network

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
)

type networkStoreDirectoryClass uint8

const (
	networkStoreAncestorDirectory networkStoreDirectoryClass = iota
	networkStoreStateBaseDirectory
	networkStorePrivateDirectory
)

const (
	networkStoreDirectoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	networkStoreReadFlags      = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	networkStoreTempFlags      = unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW

	networkStoreDirectoryMode = uint32(0700)
	networkStoreRecordMode    = uint32(0600)
	networkStoreTempAttempts  = 8
	networkStoreIOChunk       = 1024

	networkStoreResolve = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS |
		unix.RESOLVE_NO_MAGICLINKS
)

type networkStoreDeps struct {
	open            func(context.Context, string, int, uint32) (int, error)
	openAt2         func(context.Context, int, string, *unix.OpenHow) (int, error)
	mkdirAt         func(context.Context, int, string, uint32) error
	fstat           func(context.Context, int, *unix.Stat_t) error
	fchown          func(context.Context, int, int, int) error
	fchmod          func(context.Context, int, uint32) error
	read            func(context.Context, int, []byte) (int, error)
	write           func(context.Context, int, []byte) (int, error)
	fsync           func(context.Context, int) error
	close           func(context.Context, int) error
	renameNoReplace func(context.Context, int, string, int, string) error
	unlinkAt        func(context.Context, int, string, int) error
	randomName      func(context.Context) (string, error)
	readDirNames    func(context.Context, int) ([]string, error)
	currentBootID   func(context.Context) (string, error)
}

type networkStorePolicy struct {
	rootPath    string
	stateRoot   string
	expectedUID uint32
	expectedGID uint32
}

type networkStoreRoot struct {
	mu         sync.Mutex
	deps       networkStoreDeps
	policy     networkStorePolicy
	stateFD    int
	retained   []int
	released   bool
	releaseErr error
}

type networkStoreUID struct {
	mu         sync.Mutex
	deps       networkStoreDeps
	policy     networkStorePolicy
	uid        uint32
	stateFD    int
	released   bool
	releaseErr error
}

func productionNetworkStorePolicy() networkStorePolicy {
	return networkStorePolicy{
		rootPath: "/", stateRoot: infra.NetworkAuthorityRoot, expectedUID: 0, expectedGID: 0,
	}
}

func openNetworkStoreRoot(
	ctx context.Context,
	deps networkStoreDeps,
	policy networkStorePolicy,
) (_ *networkStoreRoot, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, networkAuthorityEffectError("open network authority root", err)
	}
	retained := make([]int, 0, 5)
	defer func() {
		if returnErr == nil {
			return
		}
		if err := closeNetworkStoreFDs(context.WithoutCancel(ctx), deps, retained); err != nil {
			returnErr = appendNetworkAuthorityError(returnErr, "close rejected network authority root", err)
		}
	}()
	rootFD, err := deps.open(ctx, policy.rootPath, networkStoreDirectoryFlags, 0)
	if err != nil {
		return nil, networkAuthorityEffectError("open filesystem root for network authority", err)
	}
	retained = append(retained, rootFD)
	if err := verifyNetworkStoreDirectory(ctx, deps, rootFD, "filesystem root", policy, 0, false); err != nil {
		return nil, err
	}
	components, err := networkStorePathComponents(policy.stateRoot)
	if err != nil {
		return nil, err
	}
	parentFD := rootFD
	for index, name := range components {
		// NOTE: Guest completion markers share mvmctl; only the network authority subtree is private.
		directoryClass := networkStorePathDirectoryClass(index)
		fd, openErr := openNetworkStoreDirectoryClass(ctx, deps, parentFD, name, directoryClass, policy)
		if openErr != nil {
			return nil, openErr
		}
		retained = append(retained, fd)
		parentFD = fd
	}
	return &networkStoreRoot{deps: deps, policy: policy, stateFD: parentFD, retained: retained}, nil
}

func networkStorePathDirectoryClass(index int) networkStoreDirectoryClass {
	switch {
	case index < 2:
		return networkStoreAncestorDirectory
	case index == 2:
		return networkStoreStateBaseDirectory
	default:
		return networkStorePrivateDirectory
	}
}

func networkStorePathComponents(value string) ([4]string, error) {
	if !strings.HasPrefix(value, "/") || path.Clean(value) != value {
		return [4]string{}, networkAuthorityCorruptError("network authority root is not canonical", nil)
	}
	names := strings.Split(strings.TrimPrefix(value, "/"), "/")
	if len(names) != 4 {
		return [4]string{}, networkAuthorityCorruptError("network authority root has unexpected depth", nil)
	}
	var result [4]string
	for index, name := range names {
		if name == "" || name == "." || name == ".." {
			return [4]string{}, networkAuthorityCorruptError("network authority root has an unsafe component", nil)
		}
		result[index] = name
	}
	return result, nil
}

func openNetworkStoreDirectory(
	ctx context.Context,
	deps networkStoreDeps,
	parentFD int,
	name string,
	managed bool,
	policy networkStorePolicy,
) (int, error) {
	directoryClass := networkStoreAncestorDirectory
	if managed {
		directoryClass = networkStorePrivateDirectory
	}
	return openNetworkStoreDirectoryClass(ctx, deps, parentFD, name, directoryClass, policy)
}

func openNetworkStoreDirectoryClass(
	ctx context.Context,
	deps networkStoreDeps,
	parentFD int,
	name string,
	directoryClass networkStoreDirectoryClass,
	policy networkStorePolicy,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, networkAuthorityEffectError("open network authority directory "+name, err)
	}
	how := &unix.OpenHow{Flags: networkStoreDirectoryFlags, Resolve: networkStoreResolve}
	fd, err := deps.openAt2(ctx, parentFD, name, how)
	if err == nil {
		if verifyErr := verifyNetworkStoreDirectory(
			ctx, deps, fd, name, policy, networkStoreDirectoryMode, directoryClass == networkStorePrivateDirectory,
		); verifyErr != nil {
			return -1, closeRejectedNetworkStoreFD(ctx, deps, fd, verifyErr)
		}
		return fd, nil
	}
	if directoryClass == networkStoreAncestorDirectory || !errors.Is(err, unix.ENOENT) {
		return -1, networkAuthorityEffectError("open network authority directory "+name, err)
	}
	if err := ctx.Err(); err != nil {
		return -1, networkAuthorityEffectError("create network authority directory "+name, err)
	}
	mkdirErr := deps.mkdirAt(ctx, parentFD, name, networkStoreDirectoryMode)
	created := mkdirErr == nil
	if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
		return -1, networkAuthorityEffectError("create network authority directory "+name, mkdirErr)
	}
	fd, err = deps.openAt2(ctx, parentFD, name, how)
	if err != nil {
		return -1, networkAuthorityEffectError("open created network authority directory "+name, err)
	}
	if !created {
		if verifyErr := verifyNetworkStoreDirectory(
			ctx, deps, fd, name, policy, networkStoreDirectoryMode, directoryClass == networkStorePrivateDirectory,
		); verifyErr != nil {
			return -1, closeRejectedNetworkStoreFD(ctx, deps, fd, verifyErr)
		}
		return fd, nil
	}
	if err := deps.fchown(ctx, fd, int(policy.expectedUID), int(policy.expectedGID)); err != nil {
		return -1, closeRejectedNetworkStoreFD(
			ctx, deps, fd, networkAuthorityEffectError("set network authority directory owner", err),
		)
	}
	if err := deps.fchmod(ctx, fd, networkStoreDirectoryMode); err != nil {
		return -1, closeRejectedNetworkStoreFD(
			ctx, deps, fd, networkAuthorityEffectError("set network authority directory mode", err),
		)
	}
	if err := verifyNetworkStoreDirectory(
		ctx, deps, fd, name, policy, networkStoreDirectoryMode, true,
	); err != nil {
		return -1, closeRejectedNetworkStoreFD(ctx, deps, fd, err)
	}
	if err := deps.fsync(ctx, fd); err != nil {
		return -1, closeRejectedNetworkStoreFD(
			ctx, deps, fd, networkAuthorityEffectError("sync created network authority directory", err),
		)
	}
	if err := deps.fsync(ctx, parentFD); err != nil {
		return -1, closeRejectedNetworkStoreFD(
			ctx, deps, fd, networkAuthorityEffectError("sync network authority parent directory", err),
		)
	}
	return fd, nil
}

func verifyNetworkStoreDirectory(
	ctx context.Context,
	deps networkStoreDeps,
	fd int,
	description string,
	policy networkStorePolicy,
	mode uint32,
	exact bool,
) error {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return networkAuthorityEffectError("inspect network authority directory "+description, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return networkAuthorityCorruptError("network authority path is not a directory", nil)
	}
	if stat.Uid != policy.expectedUID || stat.Mode&0022 != 0 {
		return networkAuthorityCorruptError("network authority directory has unsafe ownership or mode", nil)
	}
	if exact && (stat.Gid != policy.expectedGID || stat.Mode&07777 != mode) {
		return networkAuthorityCorruptError("managed network authority directory has unexpected owner or mode", nil)
	}
	return nil
}

func (root *networkStoreRoot) openUID(ctx context.Context, uid uint32, create bool) (*networkStoreUID, error) {
	if uid == 0 {
		return nil, networkAuthorityValidationError("network authority UID must be non-zero")
	}
	if root == nil || root.stateFD < 0 {
		return nil, networkAuthorityCorruptError("network authority root is released", nil)
	}
	name := strconv.FormatUint(uint64(uid), 10)
	if !create {
		if err := ctx.Err(); err != nil {
			return nil, networkAuthorityEffectError("open indexed network authority directory", err)
		}
		how := &unix.OpenHow{Flags: networkStoreDirectoryFlags, Resolve: networkStoreResolve}
		fd, err := root.deps.openAt2(ctx, root.stateFD, name, how)
		if err != nil {
			return nil, networkAuthorityEffectError("open indexed network authority directory", err)
		}
		if err := verifyNetworkStoreDirectory(
			ctx, root.deps, fd, "network owner", root.policy, networkStoreDirectoryMode, true,
		); err != nil {
			return nil, closeRejectedNetworkStoreFD(ctx, root.deps, fd, err)
		}
		return &networkStoreUID{deps: root.deps, policy: root.policy, uid: uid, stateFD: fd}, nil
	}
	fd, err := openNetworkStoreDirectory(ctx, root.deps, root.stateFD, name, true, root.policy)
	if err != nil {
		return nil, err
	}
	return &networkStoreUID{deps: root.deps, policy: root.policy, uid: uid, stateFD: fd}, nil
}

func (root *networkStoreRoot) Release(ctx context.Context) error {
	if root == nil {
		return nil
	}
	root.mu.Lock()
	defer root.mu.Unlock()
	if root.released {
		return root.releaseErr
	}
	root.released = true
	err := closeNetworkStoreFDs(context.WithoutCancel(ctx), root.deps, root.retained)
	root.retained = nil
	root.stateFD = -1
	if err != nil {
		root.releaseErr = networkAuthorityEffectError("release network authority root descriptors", err)
	}
	return root.releaseErr
}

func (uid *networkStoreUID) Release(ctx context.Context) error {
	if uid == nil {
		return nil
	}
	uid.mu.Lock()
	defer uid.mu.Unlock()
	if uid.released {
		return uid.releaseErr
	}
	uid.released = true
	fd := uid.stateFD
	uid.stateFD = -1
	if fd >= 0 {
		if err := uid.deps.close(context.WithoutCancel(ctx), fd); err != nil {
			uid.releaseErr = networkAuthorityEffectError("release network authority UID descriptor", err)
		}
	}
	return uid.releaseErr
}

func closeRejectedNetworkStoreFD(ctx context.Context, deps networkStoreDeps, fd int, primary error) error {
	if err := deps.close(context.WithoutCancel(ctx), fd); err != nil {
		return appendNetworkAuthorityError(primary, "close rejected network authority descriptor", err)
	}
	return primary
}

func closeNetworkStoreFDs(ctx context.Context, deps networkStoreDeps, fds []int) error {
	var result error
	for index := len(fds) - 1; index >= 0; index-- {
		if err := deps.close(context.WithoutCancel(ctx), fds[index]); err != nil {
			result = errors.Join(result, fmt.Errorf("fd %d: %w", fds[index], err))
		}
	}
	return result
}

func readAllNetworkAuthorityRecord(ctx context.Context, deps networkStoreDeps, fd int) ([]byte, error) {
	result := make([]byte, 0, networkStoreIOChunk)
	buffer := make([]byte, networkStoreIOChunk)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := deps.read(ctx, fd, buffer)
		if count < 0 || count > len(buffer) {
			return nil, fmt.Errorf("invalid read count %d", count)
		}
		if len(result)+count > maxNetworkAuthorityRecordBytes {
			return nil, fmt.Errorf("network authority record exceeds size limit")
		}
		result = append(result, buffer[:count]...)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return result, nil
			}
			return nil, err
		}
		if count == 0 {
			return result, nil
		}
	}
}

func randomNetworkStoreTempName(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return ".mvm-network-" + hex.EncodeToString(value[:]) + ".tmp", nil
}

func readNetworkStoreDirectoryNames(ctx context.Context, fd int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	duplicateFD, err := unix.Openat2(fd, ".", &unix.OpenHow{
		Flags:   networkStoreDirectoryFlags,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(duplicateFD), "network-authority-directory")
	if file == nil {
		return nil, errors.Join(errors.New("create network authority directory reader"), unix.Close(duplicateFD))
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

func readCurrentBootID(ctx context.Context) (_ string, returnErr error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fd, err := unix.Open(
		"/proc/sys/kernel/random/boot_id",
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK,
		0,
	)
	if err != nil {
		return "", err
	}
	defer func() {
		if err := unix.Close(fd); err != nil {
			returnErr = errors.Join(returnErr, fmt.Errorf("close host boot identity: %w", err))
		}
	}()
	buffer := make([]byte, 38)
	used := 0
	for used < len(buffer) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		count, err := unix.Read(fd, buffer[used:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return "", err
		}
		if count == 0 {
			break
		}
		used += count
	}
	if used == len(buffer) {
		return "", errors.New("host boot identity exceeds size limit")
	}
	value := string(buffer[:used])
	if len(value) == 37 && value[36] == '\n' {
		value = value[:36]
	}
	if !bootIDPattern.MatchString(value) {
		return "", errors.New("host boot identity is invalid")
	}
	return value, nil
}

func realNetworkStoreDeps() networkStoreDeps {
	return networkStoreDeps{
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
		randomName:    randomNetworkStoreTempName,
		readDirNames:  readNetworkStoreDirectoryNames,
		currentBootID: readCurrentBootID,
	}
}
