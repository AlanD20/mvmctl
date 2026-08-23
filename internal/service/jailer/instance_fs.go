package jailer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const (
	instanceDirectoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	instanceReadFlags      = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	instanceInspectFlags   = unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW
	instanceTempFlags      = unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW
	instanceLockFlags      = unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	instanceLockCreate     = instanceLockFlags | unix.O_CREAT | unix.O_EXCL

	instanceManagedDirMode = uint32(0700)
	instanceStateFileMode  = uint32(0600)
	instanceTempAttempts   = 8
	instanceIOChunk        = 4096
	instanceLockPoll       = 10 * time.Millisecond
)

type instanceAuthorityDeps struct {
	open         func(context.Context, string, int, uint32) (int, error)
	openAt       func(context.Context, int, string, int, uint32) (int, error)
	mkdirAt      func(context.Context, int, string, uint32) error
	fstat        func(context.Context, int, *unix.Stat_t) error
	fchown       func(context.Context, int, int, int) error
	fchmod       func(context.Context, int, uint32) error
	read         func(context.Context, int, []byte) (int, error)
	write        func(context.Context, int, []byte) (int, error)
	fsync        func(context.Context, int) error
	close        func(context.Context, int) error
	renameAt     func(context.Context, int, string, int, string) error
	unlinkAt     func(context.Context, int, string, int) error
	flock        func(context.Context, int, int) error
	randomName   func(context.Context) (string, error)
	readDirNames func(context.Context, int) ([]string, error)
	waitLock     func(context.Context) error
}

type instanceAuthorityPolicy struct {
	rootPath    string
	expectedUID uint32
	expectedGID uint32
}

type instanceAuthorityRoots struct {
	deps       instanceAuthorityDeps
	policy     instanceAuthorityPolicy
	stateFD    int
	runtimeFD  int
	releasesFD int
	retained   []int
}

type instanceUIDDirectories struct {
	deps      instanceAuthorityDeps
	policy    instanceAuthorityPolicy
	uid       uint32
	stateFD   int
	runtimeFD int
}

func productionInstanceAuthorityPolicy() instanceAuthorityPolicy {
	return instanceAuthorityPolicy{
		rootPath:    "/",
		expectedUID: 0,
		expectedGID: 0,
	}
}

func openInstanceAuthorityRoots(
	ctx context.Context,
	deps instanceAuthorityDeps,
	policy instanceAuthorityPolicy,
) (_ *instanceAuthorityRoots, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, instanceAtomicError("open instance authority roots", err)
	}
	stateNames, err := instanceAuthorityPathComponents(infra.JailerInstanceRoot, 4)
	if err != nil {
		return nil, err
	}
	runtimeNames, err := instanceAuthorityPathComponents(infra.JailerRuntimeRoot, 2)
	if err != nil {
		return nil, err
	}
	retained := make([]int, 0, 8)
	defer func() {
		if returnErr == nil {
			return
		}
		if err := closeInstanceFDs(ctx, deps, retained); err != nil {
			returnErr = joinInstanceAtomicError(returnErr, "close rejected instance authority roots", err)
		}
	}()

	rootFD, err := deps.open(ctx, policy.rootPath, instanceDirectoryFlags, 0)
	if err != nil {
		return nil, instanceAtomicError("open filesystem root for instance authority", err)
	}
	retained = append(retained, rootFD)
	if err := verifyInstanceDirectory(ctx, deps, rootFD, "filesystem root", policy, false); err != nil {
		return nil, err
	}

	varFD, err := openInstanceDirectory(ctx, deps, rootFD, stateNames[0], false, false, policy)
	if err != nil {
		return nil, err
	}
	retained = append(retained, varFD)
	libFD, err := openInstanceDirectory(ctx, deps, varFD, stateNames[1], false, false, policy)
	if err != nil {
		return nil, err
	}
	retained = append(retained, libFD)
	stateBaseFD, err := openInstanceDirectory(ctx, deps, libFD, stateNames[2], true, false, policy)
	if err != nil {
		return nil, err
	}
	retained = append(retained, stateBaseFD)
	stateFD, err := openInstanceDirectory(ctx, deps, stateBaseFD, stateNames[3], true, true, policy)
	if err != nil {
		return nil, err
	}
	retained = append(retained, stateFD)

	runFD, err := openInstanceDirectory(ctx, deps, rootFD, runtimeNames[0], false, false, policy)
	if err != nil {
		return nil, err
	}
	retained = append(retained, runFD)
	runtimeFD, err := openInstanceDirectory(ctx, deps, runFD, runtimeNames[1], true, true, policy)
	if err != nil {
		return nil, err
	}
	retained = append(retained, runtimeFD)
	releasesFD, err := openInstanceDirectory(ctx, deps, runtimeFD, "releases", true, true, policy)
	if err != nil {
		return nil, err
	}
	retained = append(retained, releasesFD)

	return &instanceAuthorityRoots{
		deps:       deps,
		policy:     policy,
		stateFD:    stateFD,
		runtimeFD:  runtimeFD,
		releasesFD: releasesFD,
		retained:   retained,
	}, nil
}

func instanceAuthorityPathComponents(value string, expectedCount int) ([]string, error) {
	if !strings.HasPrefix(value, "/") || path.Clean(value) != value {
		return nil, instanceAtomicError("instance authority root constant is not canonical", nil)
	}
	names := strings.Split(strings.TrimPrefix(value, "/"), "/")
	if len(names) != expectedCount {
		return nil, instanceAtomicError("instance authority root constant has unexpected depth", nil)
	}
	for _, name := range names {
		if name == "" || name == "." || name == ".." {
			return nil, instanceAtomicError("instance authority root constant has an unsafe component", nil)
		}
	}
	return names, nil
}

func (roots *instanceAuthorityRoots) openUIDDirectories(
	ctx context.Context,
	uid uint32,
) (_ *instanceUIDDirectories, returnErr error) {
	if uid == 0 {
		return nil, instanceValidationError("instance authority UID must be non-zero")
	}
	name := strconv.FormatUint(uint64(uid), 10)
	stateFD, err := openInstanceDirectory(ctx, roots.deps, roots.stateFD, name, true, true, roots.policy)
	if err != nil {
		return nil, err
	}
	runtimeFD, err := openInstanceDirectory(ctx, roots.deps, roots.runtimeFD, name, true, true, roots.policy)
	if err != nil {
		if closeErr := roots.deps.close(context.WithoutCancel(ctx), stateFD); closeErr != nil {
			err = joinInstanceAtomicError(err, "close instance UID state directory", closeErr)
		}
		return nil, err
	}
	return &instanceUIDDirectories{
		deps:      roots.deps,
		policy:    roots.policy,
		uid:       uid,
		stateFD:   stateFD,
		runtimeFD: runtimeFD,
	}, nil
}

func openInstanceDirectory(
	ctx context.Context,
	deps instanceAuthorityDeps,
	parentFD int,
	name string,
	allowCreate bool,
	requireManagedMode bool,
	policy instanceAuthorityPolicy,
) (int, error) {
	if err := ctx.Err(); err != nil {
		return -1, instanceAtomicError("open instance authority directory "+name, err)
	}
	fd, err := deps.openAt(ctx, parentFD, name, instanceDirectoryFlags, 0)
	if err == nil {
		if verifyErr := verifyInstanceDirectory(ctx, deps, fd, name, policy, requireManagedMode); verifyErr != nil {
			return -1, closeRejectedInstanceFD(ctx, deps, fd, verifyErr)
		}
		return fd, nil
	}
	if !errors.Is(err, unix.ENOENT) || !allowCreate {
		return -1, instanceAtomicError("open instance authority directory "+name, err)
	}
	mkdirErr := deps.mkdirAt(ctx, parentFD, name, instanceManagedDirMode)
	created := mkdirErr == nil
	if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
		return -1, instanceAtomicError("create instance authority directory "+name, mkdirErr)
	}
	fd, err = deps.openAt(ctx, parentFD, name, instanceDirectoryFlags, 0)
	if err != nil {
		return -1, instanceAtomicError("open created instance authority directory "+name, err)
	}
	if !created {
		if verifyErr := verifyInstanceDirectory(ctx, deps, fd, name, policy, requireManagedMode); verifyErr != nil {
			return -1, closeRejectedInstanceFD(ctx, deps, fd, verifyErr)
		}
		return fd, nil
	}
	if err := deps.fchown(ctx, fd, int(policy.expectedUID), int(policy.expectedGID)); err != nil {
		return -1, closeRejectedInstanceFD(
			ctx,
			deps,
			fd,
			instanceAtomicError("set instance authority directory owner "+name, err),
		)
	}
	if err := deps.fchmod(ctx, fd, instanceManagedDirMode); err != nil {
		return -1, closeRejectedInstanceFD(
			ctx,
			deps,
			fd,
			instanceAtomicError("set instance authority directory mode "+name, err),
		)
	}
	if err := verifyInstanceDirectory(ctx, deps, fd, name, policy, true); err != nil {
		return -1, closeRejectedInstanceFD(ctx, deps, fd, err)
	}
	if err := deps.fsync(ctx, fd); err != nil {
		return -1, closeRejectedInstanceFD(
			ctx,
			deps,
			fd,
			instanceAtomicError("sync created instance authority directory "+name, err),
		)
	}
	if err := deps.fsync(ctx, parentFD); err != nil {
		return -1, closeRejectedInstanceFD(
			ctx,
			deps,
			fd,
			instanceAtomicError("sync parent of instance authority directory "+name, err),
		)
	}
	return fd, nil
}

func verifyInstanceDirectory(
	ctx context.Context,
	deps instanceAuthorityDeps,
	fd int,
	name string,
	policy instanceAuthorityPolicy,
	requireManagedMode bool,
) error {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return instanceAtomicError("inspect instance authority directory "+name, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return instanceAtomicError("instance authority path "+name+" is not a directory", nil)
	}
	if stat.Uid != policy.expectedUID || stat.Mode&0022 != 0 {
		return instanceAtomicError("instance authority directory "+name+" has unsafe ownership or mode", nil)
	}
	if requireManagedMode && (stat.Gid != policy.expectedGID || stat.Mode&07777 != instanceManagedDirMode) {
		return instanceAtomicError("managed instance authority directory "+name+" has unexpected owner or mode", nil)
	}
	return nil
}

func (roots *instanceAuthorityRoots) Release(ctx context.Context) error {
	if roots == nil || len(roots.retained) == 0 {
		return nil
	}
	err := closeInstanceFDs(context.WithoutCancel(ctx), roots.deps, roots.retained)
	roots.retained = nil
	if err != nil {
		return instanceAtomicError("release instance authority root descriptors", err)
	}
	return nil
}

func (directories *instanceUIDDirectories) Release(ctx context.Context) error {
	if directories == nil {
		return nil
	}
	cleanupCtx := context.WithoutCancel(ctx)
	var result error
	if directories.runtimeFD >= 0 {
		result = errors.Join(result, directories.deps.close(cleanupCtx, directories.runtimeFD))
		directories.runtimeFD = -1
	}
	if directories.stateFD >= 0 {
		result = errors.Join(result, directories.deps.close(cleanupCtx, directories.stateFD))
		directories.stateFD = -1
	}
	if result != nil {
		return instanceAtomicError("release instance UID directory descriptors", result)
	}
	return nil
}

func closeRejectedInstanceFD(ctx context.Context, deps instanceAuthorityDeps, fd int, cause error) error {
	if err := deps.close(context.WithoutCancel(ctx), fd); err != nil {
		return joinInstanceAtomicError(cause, "close rejected instance authority descriptor", err)
	}
	return cause
}

func closeInstanceFDs(ctx context.Context, deps instanceAuthorityDeps, fds []int) error {
	cleanupCtx := context.WithoutCancel(ctx)
	var result error
	for index := len(fds) - 1; index >= 0; index-- {
		if err := deps.close(cleanupCtx, fds[index]); err != nil {
			result = errors.Join(result, fmt.Errorf("fd %d: %w", fds[index], err))
		}
	}
	return result
}

func joinInstanceAtomicError(primary error, message string, cause error) *errs.DomainError {
	if primary == nil {
		return instanceAtomicError(message+": "+cause.Error(), cause)
	}
	if domainErr := errs.AsDomainError(primary); domainErr != nil {
		domainErr.Message += "; " + message + ": " + cause.Error()
		domainErr.Err = errors.Join(domainErr.Err, cause)
		return domainErr
	}
	return errs.WrapMsg(
		errs.CodeVMAtomicFailed,
		primary.Error()+"; "+message+": "+cause.Error(),
		errors.Join(primary, cause),
		errs.WithClass(errs.ClassInternal),
	)
}

func randomInstanceTempName(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return ".mvm-instance-" + hex.EncodeToString(value[:]) + ".tmp", nil
}

func readInstanceDirectoryNames(ctx context.Context, fd int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	duplicateFD, err := unix.Openat(fd, ".", instanceDirectoryFlags, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(duplicateFD), "instance-authority-directory")
	if file == nil {
		closeErr := unix.Close(duplicateFD)
		return nil, errors.Join(fmt.Errorf("create directory reader"), closeErr)
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
	return names, nil
}

func waitForInstanceLock(ctx context.Context) error {
	timer := time.NewTimer(instanceLockPoll)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func realInstanceAuthorityDeps() instanceAuthorityDeps {
	return instanceAuthorityDeps{
		open: func(_ context.Context, path string, flags int, mode uint32) (int, error) {
			return unix.Open(path, flags, mode)
		},
		openAt: func(_ context.Context, parentFD int, name string, flags int, mode uint32) (int, error) {
			return unix.Openat(parentFD, name, flags, mode)
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
		renameAt: func(_ context.Context, oldFD int, oldName string, newFD int, newName string) error {
			return unix.Renameat(oldFD, oldName, newFD, newName)
		},
		unlinkAt: func(_ context.Context, parentFD int, name string, flags int) error {
			return unix.Unlinkat(parentFD, name, flags)
		},
		flock:        func(_ context.Context, fd, how int) error { return unix.Flock(fd, how) },
		randomName:   randomInstanceTempName,
		readDirNames: readInstanceDirectoryNames,
		waitLock:     waitForInstanceLock,
	}
}

func validateCanonicalUIDName(name string) (uint32, bool) {
	value, err := strconv.ParseUint(name, 10, 32)
	if err != nil || value == 0 || strconv.FormatUint(value, 10) != name {
		return 0, false
	}
	return uint32(value), true
}

func readAllInstanceRecord(ctx context.Context, deps instanceAuthorityDeps, fd int) ([]byte, error) {
	result := make([]byte, 0, instanceIOChunk)
	buffer := make([]byte, instanceIOChunk)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		count, err := deps.read(ctx, fd, buffer)
		if count < 0 || count > len(buffer) {
			return nil, fmt.Errorf("invalid read count %d", count)
		}
		if len(result)+count > maxInstanceRecordBytes {
			return nil, fmt.Errorf("instance authority record exceeds size limit")
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
