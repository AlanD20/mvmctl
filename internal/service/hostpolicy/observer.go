package hostpolicy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	policygrammar "mvmctl/internal/lib/hostpolicy"
)

const policyMaxBytes = 64 * 1024

type observerDeps struct {
	open    func(string, int, uint32) (int, error)
	openat  func(int, string, int, uint32) (int, error)
	fstat   func(int, *unix.Stat_t) error
	fstatat func(int, string, *unix.Stat_t, int) error
	read    func(int, []byte) (int, error)
	close   func(int) error
}

type observerPolicy struct {
	root string
	uid  uint32
	gid  uint32
}

// Observer reads the compiled policy locally for already-root administrator callers.
// It never creates or repairs files or directories and accepts no alternate path.
type Observer struct{}

func NewObserver() *Observer { return &Observer{} }

// Observe requires root and proves fixed-path metadata, policy bytes and retained bindings.
func (observer *Observer) Observe(ctx context.Context) (ObserveResult, error) {
	if observer == nil || ctx == nil || os.Geteuid() != 0 {
		return ObserveResult{}, policyError("managed host policy observation requires root and context", nil)
	}
	result, err := observePolicy(ctx, observerDeps{
		open: unix.Open, openat: unix.Openat, fstat: unix.Fstat,
		fstatat: unix.Fstatat, read: unix.Read, close: unix.Close,
	}, observerPolicy{root: "/"})
	if err != nil {
		slog.Error("could not observe managed host policy", "error", err)
	}
	return result, err
}

func observePolicy(ctx context.Context, deps observerDeps, policy observerPolicy) (
	result ObserveResult, returnErr error,
) {
	if err := ctx.Err(); err != nil {
		return ObserveResult{}, policyError("observe managed host policy", err)
	}
	var fds []int
	var directories []unix.Stat_t
	defer func() {
		var cleanup error
		for index := len(fds) - 1; index >= 0; index-- {
			cleanup = errors.Join(cleanup, deps.close(fds[index]))
		}
		if cleanup != nil {
			returnErr = policyError("close managed host policy descriptors", errors.Join(returnErr, cleanup))
			result = ObserveResult{}
		}
	}()
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	fd, err := deps.open(policy.root, flags, 0)
	if err != nil {
		return ObserveResult{}, policyError("open managed host policy root", err)
	}
	fds = append(fds, fd)
	names := [...]string{"etc", "sudoers.d"}
	for index := 0; ; index++ {
		var stat unix.Stat_t
		if err := deps.fstat(fd, &stat); err != nil {
			return ObserveResult{}, policyError("inspect managed host policy directory", err)
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != policy.uid || stat.Gid != policy.gid ||
			stat.Mode&0022 != 0 {
			return ObserveResult{}, policyError("unsafe managed host policy directory ownership or mode", nil)
		}
		directories = append(directories, stat)
		if index == len(names) {
			break
		}
		if err := ctx.Err(); err != nil {
			return ObserveResult{}, policyError("observe managed host policy", err)
		}
		child, err := deps.openat(fd, names[index], flags, 0)
		if errors.Is(err, unix.ENOENT) {
			return confirmedAbsence(ctx, deps, fds, directories, names[:index])
		}
		if err != nil {
			return ObserveResult{}, policyError("open managed host policy directory", err)
		}
		fd = child
		fds = append(fds, fd)
	}
	file, err := deps.openat(fd, "mvm", unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, unix.ENOENT) {
		return confirmedAbsence(ctx, deps, fds, directories, names[:])
	}
	if err != nil {
		return ObserveResult{}, policyError("open managed host policy", err)
	}
	fds = append(fds, file)
	var before unix.Stat_t
	if err := deps.fstat(file, &before); err != nil {
		return ObserveResult{}, policyError("inspect managed host policy", err)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&07777 != infra.SudoersPerm ||
		before.Uid != policy.uid || before.Gid != policy.gid || before.Nlink != 1 ||
		before.Size < 0 || before.Size > policyMaxBytes {
		return ObserveResult{}, policyError("unsafe managed host policy metadata", nil)
	}
	data := make([]byte, before.Size+1)
	offset := 0
	for offset < len(data) {
		if err := ctx.Err(); err != nil {
			return ObserveResult{}, policyError("read managed host policy", err)
		}
		n, err := deps.read(file, data[offset:])
		if err != nil {
			return ObserveResult{}, policyError("read managed host policy", err)
		}
		if n < 0 || n > len(data)-offset {
			return ObserveResult{}, policyError("read managed host policy", io.ErrUnexpectedEOF)
		}
		if n == 0 {
			break
		}
		offset += n
	}
	var after, binding unix.Stat_t
	if err := deps.fstat(file, &after); err != nil {
		return ObserveResult{}, policyError("reinspect managed host policy", err)
	}
	if err := deps.fstatat(fd, "mvm", &binding, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return ObserveResult{}, policyError("recheck managed host policy binding", err)
	}
	if int64(offset) != before.Size || !samePolicyFile(before, after) || !samePolicyFile(after, binding) {
		return ObserveResult{}, policyError("managed host policy changed during read", nil)
	}
	if line, _ := policygrammar.InvalidMarkerPolicyLine(string(data[:offset])); line != 0 {
		return ObserveResult{}, policyError("managed host policy is not the marker-only dispatcher grant", nil)
	}
	if err := recheckDirectories(ctx, deps, fds[:len(directories)], directories, names[:]); err != nil {
		return ObserveResult{}, err
	}
	exists := true
	return ObserveResult{Exists: &exists}, nil
}

func confirmedAbsence(ctx context.Context, deps observerDeps, fds []int, stats []unix.Stat_t,
	names []string,
) (ObserveResult, error) {
	if err := recheckDirectories(ctx, deps, fds, stats, names); err != nil {
		return ObserveResult{}, err
	}
	exists := false
	return ObserveResult{Exists: &exists}, nil
}

func recheckDirectories(ctx context.Context, deps observerDeps, fds []int, stats []unix.Stat_t,
	names []string,
) error {
	for index, before := range stats {
		if err := ctx.Err(); err != nil {
			return policyError("recheck managed host policy directories", err)
		}
		var after unix.Stat_t
		var err error
		if index == 0 {
			err = deps.fstat(fds[index], &after)
		} else {
			err = deps.fstatat(fds[index-1], names[index-1], &after, unix.AT_SYMLINK_NOFOLLOW)
		}
		if err != nil {
			return policyError("recheck managed host policy directory binding", err)
		}
		if before.Dev != after.Dev || before.Ino != after.Ino || before.Mode != after.Mode ||
			before.Uid != after.Uid || before.Gid != after.Gid {
			return policyError("managed host policy directory changed during read", nil)
		}
	}
	return nil
}

func samePolicyFile(a, b unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Mode == b.Mode && a.Uid == b.Uid && a.Gid == b.Gid &&
		a.Nlink == b.Nlink && a.Size == b.Size && a.Mtim == b.Mtim && a.Ctim == b.Ctim
}
