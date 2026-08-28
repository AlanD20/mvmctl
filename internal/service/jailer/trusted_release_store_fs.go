package jailer

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const (
	trustedReleaseStoreDirectoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	trustedReleaseStoreDirectoryMode  = uint32(0700)
)

type trustedReleaseStoreDeps struct {
	open   func(context.Context, string, int, uint32) (int, error)
	openAt func(context.Context, int, string, int, uint32) (int, error)
	fstat  func(context.Context, int, *unix.Stat_t) error
	read   func(context.Context, int, []byte) (int, error)
	pread  func(context.Context, int, []byte, int64) (int, error)
	close  func(context.Context, int) error
}

type trustedReleaseStorePolicy struct {
	rootPath    string
	expectedUID uint32
	expectedGID uint32
}

type trustedReleaseStore struct {
	deps       trustedReleaseStoreDeps
	policy     trustedReleaseStorePolicy
	binariesFD int
	retained   []int
}

type trustedReleaseDirectory struct {
	deps     trustedReleaseStoreDeps
	policy   trustedReleaseStorePolicy
	slot     releaseSlot
	slotFD   int
	retained []int
}

func productionTrustedReleaseStorePolicy() trustedReleaseStorePolicy {
	return trustedReleaseStorePolicy{rootPath: "/", expectedUID: 0, expectedGID: 0}
}

// CRITICAL: Every component is opened once relative to its retained parent. No caller path or reconstructed release
// path participates in trusted-store authorization.
func openTrustedReleaseStoreForRead(
	ctx context.Context,
	deps trustedReleaseStoreDeps,
	policy trustedReleaseStorePolicy,
) (_ *trustedReleaseStore, returnErr error) {
	if err := ctx.Err(); err != nil {
		return nil, trustedReleaseStoreError("open trusted release store", err)
	}
	if !path.IsAbs(policy.rootPath) || path.Clean(policy.rootPath) != policy.rootPath {
		return nil, trustedReleaseStoreError("trusted release store root policy is not canonical", nil)
	}
	components, err := trustedReleaseStorePathComponents()
	if err != nil {
		return nil, err
	}

	retained := make([]int, 0, len(components)+1)
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = appendTrustedReleaseStoreError(
			returnErr,
			"close rejected trusted release store",
			closeTrustedReleaseStoreFDs(ctx, deps, retained),
		)
	}()

	rootFD, err := deps.open(ctx, policy.rootPath, trustedReleaseStoreDirectoryFlags, 0)
	if err != nil {
		return nil, classifyTrustedReleaseStoreOpenError("filesystem root", err, false)
	}
	retained = append(retained, rootFD)
	if err := verifyTrustedReleaseStoreDirectory(ctx, deps, rootFD, "filesystem root", policy, false); err != nil {
		return nil, err
	}

	parentFD := rootFD
	for index, name := range components {
		if err := ctx.Err(); err != nil {
			return nil, trustedReleaseStoreError("open trusted release store component", err)
		}
		fd, openErr := deps.openAt(ctx, parentFD, name, trustedReleaseStoreDirectoryFlags, 0)
		if openErr != nil {
			return nil, classifyTrustedReleaseStoreOpenError(name, openErr, index >= 2)
		}
		retained = append(retained, fd)
		if err := verifyTrustedReleaseStoreDirectory(ctx, deps, fd, name, policy, index >= 2); err != nil {
			return nil, err
		}
		parentFD = fd
	}

	store := &trustedReleaseStore{
		deps:       deps,
		policy:     policy,
		binariesFD: parentFD,
		retained:   retained,
	}
	retained = nil
	return store, nil
}

func (store *trustedReleaseStore) openInstalledSlot(
	ctx context.Context,
	slot releaseSlot,
) (_ *trustedReleaseDirectory, returnErr error) {
	if store == nil || store.binariesFD < 0 || len(store.retained) == 0 {
		return nil, trustedReleaseStoreError("trusted release store is not active", nil)
	}
	if err := validateReleaseSlotValue(slot); err != nil {
		return nil, instanceValidationError(err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, trustedReleaseStoreError("open installed trusted release", err)
	}

	retained := make([]int, 0, 2)
	defer func() {
		if returnErr == nil {
			return
		}
		returnErr = appendTrustedReleaseStoreError(
			returnErr,
			"close rejected installed trusted release",
			closeTrustedReleaseStoreFDs(ctx, store.deps, retained),
		)
	}()

	architectureFD, err := store.deps.openAt(
		ctx,
		store.binariesFD,
		slot.architecture,
		trustedReleaseStoreDirectoryFlags,
		0,
	)
	if err != nil {
		return nil, classifyTrustedReleaseStoreOpenError("release architecture", err, true)
	}
	retained = append(retained, architectureFD)
	if err := verifyTrustedReleaseStoreDirectory(
		ctx,
		store.deps,
		architectureFD,
		"release architecture",
		store.policy,
		true,
	); err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, trustedReleaseStoreError("open installed trusted release version", err)
	}
	slotFD, err := store.deps.openAt(
		ctx,
		architectureFD,
		slot.version,
		trustedReleaseStoreDirectoryFlags,
		0,
	)
	if err != nil {
		return nil, classifyTrustedReleaseStoreOpenError("release version", err, true)
	}
	retained = append(retained, slotFD)
	if err := verifyTrustedReleaseStoreDirectory(
		ctx,
		store.deps,
		slotFD,
		"release version",
		store.policy,
		true,
	); err != nil {
		return nil, err
	}

	directory := &trustedReleaseDirectory{
		deps:     store.deps,
		policy:   store.policy,
		slot:     slot,
		slotFD:   slotFD,
		retained: retained,
	}
	retained = nil
	return directory, nil
}

func trustedReleaseStorePathComponents() ([]string, error) {
	if !strings.HasPrefix(infra.TrustedBinaryRoot, "/") ||
		path.Clean(infra.TrustedBinaryRoot) != infra.TrustedBinaryRoot {
		return nil, trustedReleaseStoreError("trusted release store root constant is not canonical", nil)
	}
	components := strings.Split(strings.TrimPrefix(infra.TrustedBinaryRoot, "/"), "/")
	if len(components) != 4 {
		return nil, trustedReleaseStoreError("trusted release store root constant has unexpected depth", nil)
	}
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			return nil, trustedReleaseStoreError("trusted release store root constant has an unsafe component", nil)
		}
	}
	return components, nil
}

func verifyTrustedReleaseStoreDirectory(
	ctx context.Context,
	deps trustedReleaseStoreDeps,
	fd int,
	description string,
	policy trustedReleaseStorePolicy,
	requireManagedMode bool,
) error {
	if err := ctx.Err(); err != nil {
		return trustedReleaseStoreError("inspect trusted release directory "+description, err)
	}
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return trustedReleaseStoreError("inspect trusted release directory "+description, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return trustedReleaseStoreUntrusted("trusted release path "+description+" is not a directory", nil)
	}
	if stat.Uid != policy.expectedUID {
		return trustedReleaseStoreUntrusted("trusted release directory "+description+" has an unsafe owner", nil)
	}
	if requireManagedMode {
		if stat.Gid != policy.expectedGID || stat.Mode&07777 != trustedReleaseStoreDirectoryMode {
			return trustedReleaseStoreUntrusted(
				"managed trusted release directory "+description+" has unexpected owner or mode",
				nil,
			)
		}
		return nil
	}
	if stat.Mode&0022 != 0 {
		return trustedReleaseStoreUntrusted(
			"trusted release directory "+description+" is group or world writable",
			nil,
		)
	}
	return nil
}

func classifyTrustedReleaseStoreOpenError(description string, cause error, missingIsNotFound bool) error {
	if errors.Is(cause, unix.ENOENT) && missingIsNotFound {
		return errs.NotFound(errs.CodeBinaryNotFound, "trusted release is not installed")
	}
	if errors.Is(cause, unix.ELOOP) || errors.Is(cause, unix.ENOTDIR) {
		return trustedReleaseStoreUntrusted("trusted release path "+description+" is unsafe", cause)
	}
	return trustedReleaseStoreError("open trusted release path "+description, cause)
}

func (store *trustedReleaseStore) Release(ctx context.Context) error {
	if store == nil || len(store.retained) == 0 {
		return nil
	}
	err := closeTrustedReleaseStoreFDs(ctx, store.deps, store.retained)
	store.retained = nil
	store.binariesFD = -1
	if err != nil {
		return trustedReleaseStoreError("release trusted release store descriptors", err)
	}
	return nil
}

func (directory *trustedReleaseDirectory) Release(ctx context.Context) error {
	if directory == nil || len(directory.retained) == 0 {
		return nil
	}
	err := closeTrustedReleaseStoreFDs(ctx, directory.deps, directory.retained)
	directory.retained = nil
	directory.slotFD = -1
	if err != nil {
		return trustedReleaseStoreError("release installed trusted release descriptors", err)
	}
	return nil
}

func closeTrustedReleaseStoreFDs(ctx context.Context, deps trustedReleaseStoreDeps, fds []int) error {
	cleanupCtx := context.WithoutCancel(ctx)
	var result error
	for index := len(fds) - 1; index >= 0; index-- {
		if err := deps.close(cleanupCtx, fds[index]); err != nil {
			result = errors.Join(result, fmt.Errorf("fd %d: %w", fds[index], err))
		}
	}
	return result
}

func trustedReleaseStoreError(message string, cause error) *errs.DomainError {
	if cause == nil {
		return errs.New(errs.CodeBinaryTrustedInstallFailed, message, errs.WithClass(errs.ClassInternal))
	}
	return errs.WrapMsg(
		errs.CodeBinaryTrustedInstallFailed,
		message,
		cause,
		errs.WithClass(errs.ClassInternal),
	)
}

func trustedReleaseStoreUntrusted(message string, cause error) *errs.DomainError {
	if cause == nil {
		return errs.New(errs.CodeBinaryUntrusted, message)
	}
	return errs.WrapMsg(errs.CodeBinaryUntrusted, message, cause, errs.WithClass(errs.ClassValidation))
}

func appendTrustedReleaseStoreError(primary error, message string, cause error) error {
	if cause == nil {
		return primary
	}
	if primary == nil {
		return trustedReleaseStoreError(message, cause)
	}
	if domainErr := errs.AsDomainError(primary); domainErr != nil {
		domainErr.Message += "; " + message + ": " + cause.Error()
		domainErr.Err = errors.Join(domainErr.Err, cause)
		return domainErr
	}
	return trustedReleaseStoreError(message, errors.Join(primary, cause))
}

func realTrustedReleaseStoreDeps() trustedReleaseStoreDeps {
	return trustedReleaseStoreDeps{
		open: func(_ context.Context, name string, flags int, mode uint32) (int, error) {
			return unix.Open(name, flags, mode)
		},
		openAt: func(_ context.Context, parentFD int, name string, flags int, mode uint32) (int, error) {
			return unix.Openat(parentFD, name, flags, mode)
		},
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error {
			return unix.Fstat(fd, stat)
		},
		read: func(_ context.Context, fd int, value []byte) (int, error) {
			return unix.Read(fd, value)
		},
		pread: func(_ context.Context, fd int, value []byte, offset int64) (int, error) {
			return unix.Pread(fd, value, offset)
		},
		close: func(_ context.Context, fd int) error {
			return unix.Close(fd)
		},
	}
}
