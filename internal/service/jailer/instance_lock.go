package jailer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

type authorityLock struct {
	deps instanceAuthorityDeps
	fd   int
}

func (roots *instanceAuthorityRoots) acquireIndexLock(ctx context.Context) (*authorityLock, error) {
	return acquireAuthorityLock(ctx, roots.deps, roots.policy, roots.runtimeFD, "index.lock", "global index")
}

func (roots *instanceAuthorityRoots) acquireReleaseLock(
	ctx context.Context,
	release releaseIdentity,
) (*authorityLock, error) {
	if err := validateReleaseIdentityValue(release); err != nil {
		return nil, instanceValidationError(err.Error())
	}
	return acquireAuthorityLock(
		ctx,
		roots.deps,
		roots.policy,
		roots.releasesFD,
		releaseLockName(release),
		"release",
	)
}

func (directories *instanceUIDDirectories) acquireVMLock(
	ctx context.Context,
	vmID string,
) (*authorityLock, error) {
	if !vmIDPattern.MatchString(vmID) {
		return nil, instanceValidationError("vm ID is invalid")
	}
	return acquireAuthorityLock(
		ctx,
		directories.deps,
		directories.policy,
		directories.runtimeFD,
		vmID+".lock",
		"VM",
	)
}

func acquireAuthorityLock(
	ctx context.Context,
	deps instanceAuthorityDeps,
	policy instanceAuthorityPolicy,
	parentFD int,
	name string,
	description string,
) (*authorityLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, instanceLockContextError(description, err)
	}
	fd, err := deps.openAt(ctx, parentFD, name, instanceLockCreate, instanceStateFileMode)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = deps.openAt(ctx, parentFD, name, instanceLockFlags, 0)
	}
	if err != nil {
		return nil, instanceLockError("open "+description+" lock", err)
	}
	abort := func(cause error) (*authorityLock, error) {
		if closeErr := deps.close(context.WithoutCancel(ctx), fd); closeErr != nil {
			cause = errors.Join(cause, closeErr)
		}
		return nil, instanceLockError("prepare "+description+" lock", cause)
	}
	if created {
		if err := deps.fchown(ctx, fd, int(policy.expectedUID), int(policy.expectedGID)); err != nil {
			return abort(err)
		}
		if err := deps.fchmod(ctx, fd, instanceStateFileMode); err != nil {
			return abort(err)
		}
	}
	if err := verifyInstanceRegularFile(ctx, deps, fd, description+" lock", policy); err != nil {
		return abort(err)
	}
	if created {
		if err := deps.fsync(ctx, fd); err != nil {
			return abort(err)
		}
		if err := deps.fsync(ctx, parentFD); err != nil {
			return abort(err)
		}
	}
	for {
		err := deps.flock(ctx, fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &authorityLock{deps: deps, fd: fd}, nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return abort(err)
		}
		if err := deps.waitLock(ctx); err != nil {
			if closeErr := deps.close(context.WithoutCancel(ctx), fd); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
			return nil, instanceLockContextError(description, err)
		}
	}
}

func (lock *authorityLock) Release(ctx context.Context) error {
	if lock == nil || lock.fd < 0 {
		return nil
	}
	cleanupCtx := context.WithoutCancel(ctx)
	unlockErr := lock.deps.flock(cleanupCtx, lock.fd, unix.LOCK_UN)
	closeErr := lock.deps.close(cleanupCtx, lock.fd)
	lock.fd = -1
	if err := errors.Join(unlockErr, closeErr); err != nil {
		return instanceLockError("release authority lock", err)
	}
	return nil
}

func verifyInstanceRegularFile(
	ctx context.Context,
	deps instanceAuthorityDeps,
	fd int,
	description string,
	policy instanceAuthorityPolicy,
) error {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return instanceAtomicError("inspect "+description, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return instanceAtomicError(description+" is not a regular file", nil)
	}
	if stat.Uid != policy.expectedUID || stat.Gid != policy.expectedGID ||
		stat.Mode&07777 != instanceStateFileMode {
		return instanceAtomicError(description+" has unexpected owner or mode", nil)
	}
	return nil
}

func releaseLockName(release releaseIdentity) string {
	canonical := release.version + "\x00" + release.architecture + "\x00" +
		release.firecrackerSHA256 + "\x00" + release.jailerSHA256
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:]) + ".lock"
}

func instanceLockError(message string, cause error) *errs.DomainError {
	return errs.WrapMsg(errs.CodeProcessError, message+": "+cause.Error(), cause)
}

func instanceLockContextError(description string, cause error) *errs.DomainError {
	return errs.WrapMsg(
		errs.CodeProcessError,
		fmt.Sprintf("wait for %s lock: %v", description, cause),
		cause,
		errs.WithClass(errs.ClassRetryable),
	)
}
