package infra

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

const defaultUserDirectoryFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW

type defaultUserDirKind uint8

const (
	defaultUserCacheDir defaultUserDirKind = iota
	defaultUserConfigDir
)

type sudoUserIdentity struct {
	uid      uint32
	gid      uint32
	username string
	home     string
}

type defaultUserDirDeps struct {
	effectiveUID func() int
	lookupEnv    func(string) (string, bool)
	lookupUserID func(string) (*user.User, error)
	userHomeDir  func() (string, error)
	open         func(context.Context, string, int, uint32) (int, error)
	openAt       func(context.Context, int, string, int, uint32) (int, error)
	mkdirAt      func(context.Context, int, string, uint32) error
	fstat        func(context.Context, int, *unix.Stat_t) error
	fchmod       func(context.Context, int, uint32) error
	fchown       func(context.Context, int, int, int) error
	fsync        func(context.Context, int) error
	close        func(context.Context, int) error
	mkdirAll     func(context.Context, string, os.FileMode) error
}

func resolveDefaultUserDir(
	ctx context.Context,
	kind defaultUserDirKind,
	deps defaultUserDirDeps,
) (_ string, returnErr error) {
	baseName, err := defaultUserBaseName(kind)
	if err != nil {
		return "", err
	}
	identity, sudo, err := resolveSudoUserIdentity(deps)
	if err != nil {
		return "", err
	}
	if !sudo {
		home, homeErr := deps.userHomeDir()
		if homeErr != nil {
			return "", defaultUserDirError("resolve current user home directory", homeErr)
		}
		path := filepath.Join(home, baseName, ProjectName)
		if mkdirErr := deps.mkdirAll(ctx, path, CacheDirPerm); mkdirErr != nil {
			return "", defaultUserDirError("create default user directory", mkdirErr)
		}
		return path, nil
	}

	homeFD, err := deps.open(ctx, identity.home, defaultUserDirectoryFlags, 0)
	if err != nil {
		return "", defaultUserDirError("open invoking user home directory", err)
	}
	defer func() {
		if closeErr := deps.close(context.WithoutCancel(ctx), homeFD); closeErr != nil {
			returnErr = joinDefaultUserDirError(returnErr, "close invoking user home directory", closeErr)
		}
	}()
	if err := verifySudoUserDirectory(ctx, deps, homeFD, identity, false); err != nil {
		return "", err
	}

	baseFD, err := openSudoUserDirectory(ctx, deps, homeFD, baseName, identity)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := deps.close(context.WithoutCancel(ctx), baseFD); closeErr != nil {
			returnErr = joinDefaultUserDirError(returnErr, "close invoking user base directory", closeErr)
		}
	}()
	projectFD, err := openSudoUserDirectory(ctx, deps, baseFD, ProjectName, identity)
	if err != nil {
		return "", err
	}
	if err := deps.close(context.WithoutCancel(ctx), projectFD); err != nil {
		return "", defaultUserDirError("close invoking user project directory", err)
	}
	return filepath.Join(identity.home, baseName, ProjectName), nil
}

func resolveUserDir(
	ctx context.Context,
	kind defaultUserDirKind,
	override string,
	deps defaultUserDirDeps,
) (string, error) {
	if override == "" {
		return resolveDefaultUserDir(ctx, kind, deps)
	}
	_, sudo, err := resolveSudoUserIdentity(deps)
	if err != nil {
		return "", err
	}
	if sudo {
		return "", errs.New(errs.CodeConfigError, "user directory override is not allowed under sudo")
	}
	path, err := filepath.Abs(override)
	if err != nil {
		return "", defaultUserDirError("resolve user directory override", err)
	}
	if err := deps.mkdirAll(ctx, path, CacheDirPerm); err != nil {
		return "", defaultUserDirError("create user directory override", err)
	}
	return path, nil
}

func defaultUserBaseName(kind defaultUserDirKind) (string, error) {
	switch kind {
	case defaultUserCacheDir:
		return ".cache", nil
	case defaultUserConfigDir:
		return ".config", nil
	default:
		return "", errs.New(errs.CodeConfigError, "unknown default user directory kind")
	}
}

func openSudoUserDirectory(
	ctx context.Context,
	deps defaultUserDirDeps,
	parentFD int,
	name string,
	identity sudoUserIdentity,
) (_ int, returnErr error) {
	if err := ctx.Err(); err != nil {
		return -1, defaultUserDirError("create invoking user directory", err)
	}
	fd, err := deps.openAt(ctx, parentFD, name, defaultUserDirectoryFlags, 0)
	if err == nil {
		if verifyErr := verifySudoUserDirectory(ctx, deps, fd, identity, false); verifyErr != nil {
			if closeErr := deps.close(context.WithoutCancel(ctx), fd); closeErr != nil {
				verifyErr = joinDefaultUserDirError(verifyErr, "close rejected invoking user directory", closeErr)
			}
			return -1, verifyErr
		}
		return fd, nil
	}
	if !errors.Is(err, unix.ENOENT) {
		return -1, defaultUserDirError("open invoking user directory "+name, err)
	}
	mkdirErr := deps.mkdirAt(ctx, parentFD, name, uint32(CacheDirPerm))
	created := mkdirErr == nil
	if mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
		return -1, defaultUserDirError("create invoking user directory "+name, mkdirErr)
	}
	fd, err = deps.openAt(ctx, parentFD, name, defaultUserDirectoryFlags, 0)
	if err != nil {
		return -1, defaultUserDirError("open created invoking user directory "+name, err)
	}
	defer func() {
		if returnErr == nil {
			return
		}
		if closeErr := deps.close(context.WithoutCancel(ctx), fd); closeErr != nil {
			returnErr = joinDefaultUserDirError(returnErr, "close incomplete invoking user directory", closeErr)
		}
	}()
	if !created {
		if err := verifySudoUserDirectory(ctx, deps, fd, identity, false); err != nil {
			return -1, err
		}
		return fd, nil
	}
	if err := deps.fchmod(ctx, fd, uint32(CacheDirPerm)); err != nil {
		return -1, defaultUserDirError("set invoking user directory mode "+name, err)
	}
	if err := deps.fchown(ctx, fd, int(identity.uid), int(identity.gid)); err != nil {
		return -1, defaultUserDirError("set invoking user directory owner "+name, err)
	}
	if err := verifySudoUserDirectory(ctx, deps, fd, identity, true); err != nil {
		return -1, err
	}
	if err := deps.fsync(ctx, fd); err != nil {
		return -1, defaultUserDirError("sync invoking user directory "+name, err)
	}
	if err := deps.fsync(ctx, parentFD); err != nil {
		return -1, defaultUserDirError("sync parent of invoking user directory "+name, err)
	}
	return fd, nil
}

func verifySudoUserDirectory(
	ctx context.Context,
	deps defaultUserDirDeps,
	fd int,
	identity sudoUserIdentity,
	requireExactMode bool,
) error {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return defaultUserDirError("inspect invoking user directory", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return errs.New(errs.CodeConfigError, "invoking user directory is not a directory")
	}
	if stat.Uid != identity.uid || stat.Mode&0700 != 0700 || stat.Mode&0022 != 0 {
		return errs.New(errs.CodeConfigError, "invoking user directory has unsafe ownership or access mode")
	}
	if requireExactMode && (stat.Gid != identity.gid || stat.Mode&07777 != uint32(CacheDirPerm)) {
		return errs.New(errs.CodeConfigError, "created invoking user directory has unexpected owner or mode")
	}
	return nil
}

func resolveSudoUserIdentity(deps defaultUserDirDeps) (sudoUserIdentity, bool, error) {
	if deps.effectiveUID() != 0 {
		return sudoUserIdentity{}, false, nil
	}
	uidValue, hasUID := deps.lookupEnv("SUDO_UID")
	gidValue, hasGID := deps.lookupEnv("SUDO_GID")
	username, hasUser := deps.lookupEnv("SUDO_USER")
	if !hasUID && !hasGID && !hasUser {
		return sudoUserIdentity{}, false, nil
	}
	if !hasUID || !hasGID || !hasUser || uidValue == "" || gidValue == "" || username == "" {
		return sudoUserIdentity{}, false, errs.New(
			errs.CodeConfigError,
			"complete sudo identity is required for user directory ownership",
		)
	}
	uid, err := parseSudoUserIdentityNumber("SUDO_UID", uidValue)
	if err != nil {
		return sudoUserIdentity{}, false, err
	}
	gid, err := parseSudoUserIdentityNumber("SUDO_GID", gidValue)
	if err != nil {
		return sudoUserIdentity{}, false, err
	}
	invokingUser, err := deps.lookupUserID(strconv.FormatUint(uint64(uid), 10))
	if err != nil {
		return sudoUserIdentity{}, false, defaultUserDirError("look up sudo user by UID", err)
	}
	lookupUID, uidErr := strconv.ParseUint(invokingUser.Uid, 10, 32)
	if uidErr != nil || uint32(lookupUID) != uid {
		return sudoUserIdentity{}, false, errs.New(
			errs.CodeConfigError,
			"sudo user lookup returned an inconsistent UID",
		)
	}
	lookupGID, gidErr := strconv.ParseUint(invokingUser.Gid, 10, 32)
	if gidErr != nil || uint32(lookupGID) != gid {
		return sudoUserIdentity{}, false, errs.New(errs.CodeConfigError, "sudo user lookup does not match SUDO_GID")
	}
	if invokingUser.Username != username {
		return sudoUserIdentity{}, false, errs.New(errs.CodeConfigError, "sudo user lookup does not match SUDO_USER")
	}
	if !filepath.IsAbs(invokingUser.HomeDir) || filepath.Clean(invokingUser.HomeDir) != invokingUser.HomeDir ||
		invokingUser.HomeDir == "/" || strings.ContainsRune(invokingUser.HomeDir, '\x00') {
		return sudoUserIdentity{}, false, errs.New(
			errs.CodeConfigError,
			"sudo user lookup returned an unsafe home directory",
		)
	}
	return sudoUserIdentity{
		uid:      uid,
		gid:      gid,
		username: invokingUser.Username,
		home:     invokingUser.HomeDir,
	}, true, nil
}

func parseSudoUserIdentityNumber(name, value string) (uint32, error) {
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil || parsed == 0 || parsed == math.MaxUint32 || strconv.FormatUint(parsed, 10) != value {
		return 0, errs.New(errs.CodeConfigError, fmt.Sprintf("invalid %s: expected a canonical non-root ID", name))
	}
	return uint32(parsed), nil
}

func realDefaultUserDirDeps() defaultUserDirDeps {
	return defaultUserDirDeps{
		effectiveUID: os.Geteuid,
		lookupEnv:    os.LookupEnv,
		lookupUserID: user.LookupId,
		userHomeDir:  os.UserHomeDir,
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
		fchmod: func(_ context.Context, fd int, mode uint32) error { return unix.Fchmod(fd, mode) },
		fchown: func(_ context.Context, fd, uid, gid int) error { return unix.Fchown(fd, uid, gid) },
		fsync:  func(_ context.Context, fd int) error { return unix.Fsync(fd) },
		close:  func(_ context.Context, fd int) error { return unix.Close(fd) },
		mkdirAll: func(_ context.Context, path string, mode os.FileMode) error {
			return os.MkdirAll(path, mode)
		},
	}
}

func defaultUserDirError(message string, cause error) *errs.DomainError {
	return errs.WrapMsg(errs.CodeConfigError, message, cause)
}

func joinDefaultUserDirError(primary error, message string, cause error) *errs.DomainError {
	if primary == nil {
		return defaultUserDirError(message, cause)
	}
	if domainErr := errs.AsDomainError(primary); domainErr != nil {
		domainErr.Message += "; " + message + ": " + cause.Error()
		domainErr.Err = errors.Join(domainErr.Err, cause)
		return domainErr
	}
	return defaultUserDirError(primary.Error()+"; "+message, errors.Join(primary, cause))
}
