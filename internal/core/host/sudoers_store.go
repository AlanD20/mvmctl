package host

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

const (
	sudoersFileName       = "mvm"
	sudoersMaxBytes       = 64 * 1024
	sudoersReadFlags      = unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	sudoersResolve        = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS
	sudoersCleanupTimeout = 5 * time.Second
)

type sudoersStorePolicy struct {
	rootPath string
	uid      uint32
	gid      uint32
}

type sudoersStoreDeps struct {
	effectiveUID func() int
	stat         func(context.Context, string) (os.FileInfo, error)
	open         func(context.Context, string, int, uint32) (int, error)
	openAt2      func(context.Context, int, string, *unix.OpenHow) (int, error)
	fstat        func(context.Context, int, *unix.Stat_t) error
	fstatAt      func(context.Context, int, string, *unix.Stat_t, int) error
	fchown       func(context.Context, int, int, int) error
	fchmod       func(context.Context, int, uint32) error
	read         func(context.Context, int, []byte) (int, error)
	write        func(context.Context, int, []byte) (int, error)
	fsync        func(context.Context, int) error
	close        func(context.Context, int) error
	renameAt     func(context.Context, int, string, int, string) error
	unlinkAt     func(context.Context, int, string, int) error
	randomName   func() (string, error)
	run          func(context.Context, []string, system.RunCmdOpts) (*system.RunResult, error)
}

// publishSudoers owns only the compiled policy at /etc/sudoers.d/mvm. Directory
// descriptors survive validation and publication; no user path or temp root is consulted.
func publishSudoers(ctx context.Context, deps sudoersStoreDeps, policy sudoersStorePolicy) (
	changed bool, returnErr error,
) {
	if err := ctx.Err(); err != nil {
		return false, sudoersStoreError("configure sudoers", err)
	}
	if deps.effectiveUID() != 0 {
		return false, errs.New(errs.CodePrivilegeRequired, "configure sudoers requires root")
	}
	fds := make([]int, 0, 5)
	directoryFD := -1
	candidateFD := -1
	candidateName := ""
	var createdCandidate unix.Stat_t
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sudoersCleanupTimeout)
		defer cancel()
		var cleanupErr error
		if candidateName != "" && !changed {
			cleanupErr = errors.Join(
				cleanupErr,
				removeSudoersCandidate(cleanupCtx, deps, directoryFD, candidateName, createdCandidate),
			)
		}
		if candidateFD >= 0 {
			cleanupErr = errors.Join(cleanupErr, deps.close(cleanupCtx, candidateFD))
		}
		for index := len(fds) - 1; index >= 0; index-- {
			cleanupErr = errors.Join(cleanupErr, deps.close(cleanupCtx, fds[index]))
		}
		if cleanupErr != nil {
			if returnErr == nil {
				returnErr = sudoersStoreError("close sudoers publication resources", cleanupErr)
			} else {
				de := errs.AsDomainError(returnErr)
				de.Err = errors.Join(de.Err, cleanupErr)
			}
			de := errs.AsDomainError(returnErr)
			if de.Details == nil {
				de.Details = make(map[string]any)
			}
			de.Details["cleanup_failed"] = true
		}
		if changed && returnErr != nil {
			de := errs.AsDomainError(returnErr)
			if de.Details == nil {
				de.Details = make(map[string]any)
			}
			de.Details["sudoers_replaced"] = true
		}
	}()

	rootFD, err := deps.open(ctx, policy.rootPath, directoryInstallFlags, 0)
	if err != nil {
		return false, sudoersStoreError("open sudoers filesystem root", err)
	}
	fds = append(fds, rootFD)
	if err := inspectSudoersDirectory(ctx, deps, policy, rootFD); err != nil {
		return false, err
	}
	directoryFD = rootFD
	for _, name := range [...]string{"etc", "sudoers.d"} {
		fd, err := deps.openAt2(
			ctx,
			directoryFD,
			name,
			&unix.OpenHow{Flags: directoryInstallFlags, Resolve: sudoersResolve},
		)
		if err != nil {
			return false, sudoersStoreError("open fixed sudoers directory", err)
		}
		fds = append(fds, fd)
		if err := inspectSudoersDirectory(ctx, deps, policy, fd); err != nil {
			return false, err
		}
		directoryFD = fd
	}

	want := GenerateSudoersContent(infra.MVMUnixGroup)
	previousFD, err := deps.openAt2(ctx, directoryFD, sudoersFileName, &unix.OpenHow{
		Flags: sudoersReadFlags, Resolve: sudoersResolve | unix.RESOLVE_NO_XDEV,
	})
	var previous unix.Stat_t
	found := err == nil
	if found {
		fds = append(fds, previousFD)
		previous, err = inspectSudoersFile(ctx, deps, policy, previousFD)
		if err != nil {
			return false, err
		}
		content, err := readSudoersFile(ctx, deps, previousFD, previous)
		if err != nil {
			return false, err
		}
		if err := validateSystemBinaryInstallSudoers(string(content)); err != nil {
			return false, err
		}
		if string(content) == want {
			return false, verifySudoersLeaf(ctx, deps, directoryFD, sudoersFileName, previous, true)
		}
	} else if !errors.Is(err, unix.ENOENT) {
		return false, sudoersStoreError("open existing sudoers policy", err)
	}
	if err := validateSudoersPolicy(ctx, deps, want); err != nil {
		return false, err
	}
	for attempt := 0; attempt < tempNameAttempts; attempt++ {
		name, err := deps.randomName()
		if err != nil {
			return false, sudoersStoreError("create sudoers candidate name", err)
		}
		fd, err := deps.openAt2(ctx, directoryFD, name, &unix.OpenHow{
			Flags: tempInstallFlags, Mode: 0600, Resolve: sudoersResolve | unix.RESOLVE_NO_XDEV,
		})
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return false, sudoersStoreError("create sudoers candidate", err)
		}
		candidateName, candidateFD = name, fd
		break
	}
	if candidateFD < 0 {
		return false, sudoersStoreError("create sudoers candidate", unix.EEXIST)
	}
	if err := deps.fstat(ctx, candidateFD, &createdCandidate); err != nil {
		return false, sudoersStoreError("inspect created sudoers candidate", err)
	}
	if err := deps.fchown(ctx, candidateFD, int(policy.uid), int(policy.gid)); err != nil {
		return false, sudoersStoreError("set sudoers candidate owner", err)
	}
	if err := deps.fchmod(ctx, candidateFD, infra.SudoersPerm); err != nil {
		return false, sudoersStoreError("set sudoers candidate mode", err)
	}
	for written := 0; written < len(want); {
		n, err := deps.write(ctx, candidateFD, []byte(want[written:]))
		if err != nil {
			return false, sudoersStoreError("write sudoers candidate", err)
		}
		if n <= 0 || n > len(want)-written {
			return false, sudoersStoreError("write sudoers candidate", io.ErrShortWrite)
		}
		written += n
	}
	candidate, err := inspectSudoersFile(ctx, deps, policy, candidateFD)
	if err != nil {
		return false, err
	}
	if candidate.Size != int64(len(want)) {
		return false, sudoersStoreError("verify sudoers candidate size", io.ErrShortWrite)
	}
	if err := deps.fsync(ctx, candidateFD); err != nil {
		return false, sudoersStoreError("sync sudoers candidate", err)
	}
	err = deps.close(ctx, candidateFD)
	candidateFD = -1 // close must never be retried: Linux can reuse the descriptor after an error.
	if err != nil {
		return false, sudoersStoreError("close sudoers candidate", err)
	}
	if err := ctx.Err(); err != nil {
		return false, sudoersStoreError("publish sudoers policy", err)
	}
	if err := verifySudoersLeaf(ctx, deps, directoryFD, candidateName, candidate, true); err != nil {
		return false, err
	}
	if err := verifySudoersLeaf(ctx, deps, directoryFD, sudoersFileName, previous, found); err != nil {
		return false, err
	}
	if err := deps.renameAt(ctx, directoryFD, candidateName, directoryFD, sudoersFileName); err != nil {
		return false, sudoersStoreError("atomically publish sudoers policy", err)
	}
	changed = true
	// Once renamed, cancellation cannot skip the durability attempt or undo publication.
	syncCtx, cancelSync := context.WithTimeout(context.WithoutCancel(ctx), sudoersCleanupTimeout)
	defer cancelSync()
	if err := deps.fsync(syncCtx, directoryFD); err != nil {
		return true, errs.WrapMsg(errs.CodePrivilegeSudoers, "sync sudoers policy directory", err,
			errs.WithDetails(map[string]any{"durability_uncertain": true}))
	}
	return true, nil
}

func inspectSudoersDirectory(ctx context.Context, deps sudoersStoreDeps, policy sudoersStorePolicy, fd int) error {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return sudoersStoreError("inspect sudoers directory", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != policy.uid || stat.Gid != policy.gid ||
		stat.Mode&0022 != 0 {
		return sudoersStoreError("verify sudoers directory", errors.New("expected root-owned non-writable directory"))
	}
	return nil
}

func inspectSudoersFile(
	ctx context.Context,
	deps sudoersStoreDeps,
	policy sudoersStorePolicy,
	fd int,
) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return stat, sudoersStoreError("inspect sudoers file", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Uid != policy.uid || stat.Gid != policy.gid ||
		stat.Mode&07777 != infra.SudoersPerm || stat.Size < 0 || stat.Size > sudoersMaxBytes {
		return stat, sudoersStoreError(
			"verify sudoers file",
			errors.New("expected bounded root-owned one-link 0440 regular file"),
		)
	}
	return stat, nil
}

func readSudoersFile(ctx context.Context, deps sudoersStoreDeps, fd int, before unix.Stat_t) ([]byte, error) {
	content := make([]byte, before.Size+1)
	offset := 0
	for offset < len(content) {
		n, err := deps.read(ctx, fd, content[offset:])
		if err != nil {
			return nil, sudoersStoreError("read sudoers file", err)
		}
		if n < 0 || n > len(content)-offset {
			return nil, sudoersStoreError("read sudoers file", io.ErrUnexpectedEOF)
		}
		if n == 0 {
			break
		}
		offset += n
	}
	var after unix.Stat_t
	if err := deps.fstat(ctx, fd, &after); err != nil {
		return nil, sudoersStoreError("reinspect sudoers file", err)
	}
	if int64(offset) != before.Size || !sameSudoersFile(before, after) {
		return nil, sudoersStoreError("read stable sudoers file", errors.New("policy changed during read"))
	}
	return content[:offset], nil
}

func sameSudoersFile(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Uid == b.Uid && a.Gid == b.Gid &&
		a.Nlink == b.Nlink && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}

func removeSudoersCandidate(
	ctx context.Context,
	deps sudoersStoreDeps,
	directoryFD int,
	name string,
	created unix.Stat_t,
) error {
	var current unix.Stat_t
	err := deps.fstatAt(ctx, directoryFD, name, &current, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	// Metadata may be partial after a failed write/chown, but unlink authority is
	// still bound to the created inode. Never delete a replacement at its name.
	if created.Ino == 0 || current.Dev != created.Dev || current.Ino != created.Ino ||
		current.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("sudoers candidate identity changed before cleanup")
	}
	return deps.unlinkAt(ctx, directoryFD, name, 0)
}

func verifySudoersLeaf(
	ctx context.Context,
	deps sudoersStoreDeps,
	directoryFD int,
	name string,
	expected unix.Stat_t,
	found bool,
) error {
	var current unix.Stat_t
	err := deps.fstatAt(ctx, directoryFD, name, &current, unix.AT_SYMLINK_NOFOLLOW)
	if !found && errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return sudoersStoreError("reinspect sudoers publication leaf", err)
	}
	if !found || !sameSudoersFile(expected, current) {
		return sudoersStoreError(
			"verify sudoers publication leaf",
			errors.New("policy identity changed before publication"),
		)
	}
	return nil
}

func validateSudoersPolicy(ctx context.Context, deps sudoersStoreDeps, content string) error {
	if err := validateSystemBinaryInstallSudoers(content); err != nil {
		return err
	}
	result, err := deps.run(ctx, []string{"/usr/sbin/visudo", "-c", "-f", "-"}, system.RunCmdOpts{
		Check: true, Capture: true, Input: content, Timeout: 15 * time.Second, ReplaceEnv: true,
		MaxStdoutBytes: 4096, MaxStderrBytes: 4096,
		Env: map[string]string{"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "HOME": "/root", "LANG": "C", "LC_ALL": "C"},
	})
	if err != nil {
		return sudoersStoreError("validate sudoers policy", err)
	}
	if result == nil || !result.Success() {
		return sudoersStoreError("validate sudoers policy", errors.New("visudo rejected policy"))
	}
	return nil
}

func sudoersStoreError(operation string, cause error) *errs.DomainError {
	return errs.WrapMsg(errs.CodePrivilegeSudoers, operation, cause, errs.WithClass(errs.ClassInternal))
}

func realSudoersStoreDeps() sudoersStoreDeps {
	return sudoersStoreDeps{
		effectiveUID: os.Geteuid,
		stat: func(ctx context.Context, path string) (os.FileInfo, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return os.Stat(path)
		},
		open: func(ctx context.Context, path string, flags int, mode uint32) (int, error) {
			if err := ctx.Err(); err != nil {
				return -1, err
			}
			return unix.Open(path, flags, mode)
		},
		openAt2: func(ctx context.Context, fd int, name string, how *unix.OpenHow) (int, error) {
			if err := ctx.Err(); err != nil {
				return -1, err
			}
			return unix.Openat2(fd, name, how)
		},
		fstat: func(ctx context.Context, fd int, stat *unix.Stat_t) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return unix.Fstat(fd, stat)
		},
		fstatAt: func(ctx context.Context, fd int, name string, stat *unix.Stat_t, flags int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return unix.Fstatat(fd, name, stat, flags)
		},
		fchown: func(ctx context.Context, fd, uid, gid int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return unix.Fchown(fd, uid, gid)
		},
		fchmod: func(ctx context.Context, fd int, mode uint32) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return unix.Fchmod(fd, mode)
		},
		read: func(ctx context.Context, fd int, buf []byte) (int, error) {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			return unix.Read(fd, buf)
		},
		write: func(ctx context.Context, fd int, buf []byte) (int, error) {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			return unix.Write(fd, buf)
		},
		fsync: func(ctx context.Context, fd int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return unix.Fsync(fd)
		},
		close: func(_ context.Context, fd int) error { return unix.Close(fd) },
		renameAt: func(ctx context.Context, from int, old string, to int, name string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return unix.Renameat(from, old, to, name)
		},
		unlinkAt: func(ctx context.Context, fd int, name string, flags int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return unix.Unlinkat(fd, name, flags)
		},
		randomName: func() (string, error) {
			var random [16]byte
			if _, err := rand.Read(random[:]); err != nil {
				return "", err
			}
			return ".mvm-sudoers-" + hex.EncodeToString(random[:]) + ".tmp", nil
		},
		run: system.DefaultRunner.Run,
	}
}
