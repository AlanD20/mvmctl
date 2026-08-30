package system

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"sync"

	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const pinnedExecutableHashBufferBytes = 64 * 1024

// PinnedExecutableIdentity is immutable evidence about the exact system executable retained by a PinnedExecutable.
type PinnedExecutableIdentity struct {
	Device    uint64
	Inode     uint64
	SizeBytes uint64
	Mode      uint32
	UID       uint32
	GID       uint32
	Links     uint64
	SHA256    string
}

// PinnedExecutable retains the exact canonical and running executable admitted by the privileged receiver.
//
// The capability deliberately exposes identity rather than descriptors or paths. Copies share one lifecycle.
type PinnedExecutable struct {
	state *pinnedExecutableState
}

type pinnedExecutableState struct {
	mu          sync.Mutex
	deps        pinnedExecutableDeps
	retained    []int
	canonicalFD int
	identity    PinnedExecutableIdentity
	released    bool
	releaseErr  error
}

type pinnedExecutableDeps struct {
	executable func(context.Context) (string, error)
	open       func(context.Context, string, int, uint32) (int, error)
	openAt     func(context.Context, int, string, int, uint32) (int, error)
	fstat      func(context.Context, int, *unix.Stat_t) error
	pread      func(context.Context, int, []byte, int64) (int, error)
	close      func(context.Context, int) error
}

// PinSystemExecutable admits and retains the canonical root-owned system executable and the running process image.
func PinSystemExecutable(ctx context.Context) (*PinnedExecutable, error) {
	return pinSystemExecutable(ctx, realPinnedExecutableDeps())
}

func pinSystemExecutable(
	ctx context.Context,
	deps pinnedExecutableDeps,
) (_ *PinnedExecutable, returnErr error) {
	if ctx == nil {
		return nil, pinnedExecutableAuthorityError("pin system executable: context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return nil, pinnedExecutableAuthorityError("pin system executable: context ended", err)
	}
	executable, err := deps.executable(ctx)
	if err != nil {
		return nil, pinnedExecutableAuthorityError("resolve running executable", err)
	}
	if executable != infra.SystemBinaryPath {
		return nil, pinnedExecutableAuthorityError(
			fmt.Sprintf("privileged protocol must run from %s", infra.SystemBinaryPath),
			errors.New("running executable path is not canonical"),
		)
	}

	capability := &PinnedExecutable{state: &pinnedExecutableState{
		deps: deps, canonicalFD: -1,
	}}
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanupErr := capability.Release(context.WithoutCancel(ctx))
		returnErr = appendPinnedExecutableCleanupError(returnErr, cleanupErr)
	}()

	directoryFlags := unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	rootFD, err := deps.open(ctx, "/", directoryFlags, 0)
	if err != nil {
		return nil, pinnedExecutableAuthorityError("open trusted executable root", err)
	}
	capability.state.retained = append(capability.state.retained, rootFD)
	if err := verifyPinnedExecutableDirectory(ctx, deps, rootFD, "/"); err != nil {
		return nil, err
	}

	parts := strings.Split(strings.TrimPrefix(infra.SystemBinaryPath, "/"), "/")
	if len(parts) < 2 {
		return nil, pinnedExecutableAuthorityError(
			"canonical system executable path is invalid",
			errors.New("system executable path has no parent"),
		)
	}
	parentFD := rootFD
	for _, component := range parts[:len(parts)-1] {
		fd, openErr := deps.openAt(ctx, parentFD, component, directoryFlags, 0)
		if openErr != nil {
			return nil, pinnedExecutableAuthorityError(
				fmt.Sprintf("open trusted executable path component %s", component),
				openErr,
			)
		}
		capability.state.retained = append(capability.state.retained, fd)
		if err := verifyPinnedExecutableDirectory(ctx, deps, fd, component); err != nil {
			return nil, err
		}
		parentFD = fd
	}

	// O_NOFOLLOW plus descriptor-relative traversal pins the inode which is hashed and retained.
	// Source: https://man7.org/linux/man-pages/man2/openat.2.html
	canonicalFD, err := deps.openAt(
		ctx,
		parentFD,
		parts[len(parts)-1],
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, pinnedExecutableAuthorityError("open canonical system executable", err)
	}
	capability.state.retained = append(capability.state.retained, canonicalFD)
	capability.state.canonicalFD = canonicalFD

	canonicalStat, err := inspectPinnedExecutable(ctx, deps, canonicalFD)
	if err != nil {
		return nil, err
	}

	// /proc/self/exe is a procfs magic link. Opening it without O_NOFOLLOW retains the running inode for comparison.
	// Source: https://man7.org/linux/man-pages/man5/proc_pid_exe.5.html
	runningFD, err := deps.open(ctx, "/proc/self/exe", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, pinnedExecutableAuthorityError("open running process image", err)
	}
	capability.state.retained = append(capability.state.retained, runningFD)
	var runningStat unix.Stat_t
	if err := deps.fstat(ctx, runningFD, &runningStat); err != nil {
		return nil, pinnedExecutableAuthorityError("inspect running process image descriptor", err)
	}
	if canonicalStat.Dev != runningStat.Dev || canonicalStat.Ino != runningStat.Ino {
		return nil, pinnedExecutableAuthorityError(
			"canonical system executable does not match the running process image",
			errors.New("canonical and running executable descriptors identify different inodes"),
		)
	}

	identity, err := hashPinnedExecutable(ctx, deps, canonicalFD, canonicalStat)
	if err != nil {
		return nil, err
	}
	var runningFinal unix.Stat_t
	if err := deps.fstat(ctx, runningFD, &runningFinal); err != nil {
		return nil, pinnedExecutableAuthorityError("reinspect running process image descriptor", err)
	}
	if !samePinnedExecutableStat(canonicalStat, runningFinal) {
		return nil, pinnedExecutableAuthorityError(
			"running process image identity changed during admission",
			errors.New("running executable metadata is not stable"),
		)
	}

	capability.state.identity = identity
	committed = true
	return capability, nil
}

// Identity returns the immutable admitted identity only while the capability remains live.
func (capability *PinnedExecutable) Identity(ctx context.Context) (PinnedExecutableIdentity, error) {
	if capability == nil || capability.state == nil {
		return PinnedExecutableIdentity{}, pinnedExecutableAuthorityError(
			"pinned system executable is not active",
			errors.New("missing pinned executable state"),
		)
	}
	state := capability.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := validatePinnedExecutableContext(ctx); err != nil {
		return PinnedExecutableIdentity{}, err
	}
	if state.released || state.canonicalFD < 0 {
		return PinnedExecutableIdentity{}, pinnedExecutableAuthorityError(
			"pinned system executable is not active",
			errors.New("pinned executable was released"),
		)
	}
	return state.identity, nil
}

// Reinspect proves that the retained executable still has the admitted metadata and digest.
func (capability *PinnedExecutable) Reinspect(ctx context.Context) (PinnedExecutableIdentity, error) {
	if capability == nil || capability.state == nil {
		return PinnedExecutableIdentity{}, pinnedExecutableAuthorityError(
			"pinned system executable is not active",
			errors.New("missing pinned executable state"),
		)
	}
	state := capability.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if err := validatePinnedExecutableContext(ctx); err != nil {
		return PinnedExecutableIdentity{}, err
	}
	if state.released || state.canonicalFD < 0 {
		return PinnedExecutableIdentity{}, pinnedExecutableAuthorityError(
			"pinned system executable is not active",
			errors.New("pinned executable was released"),
		)
	}
	stat, err := inspectPinnedExecutable(ctx, state.deps, state.canonicalFD)
	if err != nil {
		return PinnedExecutableIdentity{}, err
	}
	identity, err := hashPinnedExecutable(ctx, state.deps, state.canonicalFD, stat)
	if err != nil {
		return PinnedExecutableIdentity{}, err
	}
	if identity != state.identity {
		return PinnedExecutableIdentity{}, pinnedExecutableAuthorityError(
			"pinned system executable identity changed",
			errors.New("retained executable metadata or digest does not match admission"),
		)
	}
	return identity, nil
}

// Release closes every retained descriptor once across copies and concurrent callers and caches the first result.
func (capability *PinnedExecutable) Release(ctx context.Context) error {
	if capability == nil || capability.state == nil {
		return nil
	}
	state := capability.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return state.releaseErr
	}
	cleanupCtx := context.Background()
	if ctx != nil {
		cleanupCtx = context.WithoutCancel(ctx)
	}
	var cleanupErr error
	for index := len(state.retained) - 1; index >= 0; index-- {
		cleanupErr = errors.Join(cleanupErr, state.deps.close(cleanupCtx, state.retained[index]))
	}
	state.retained = nil
	state.canonicalFD = -1
	state.identity = PinnedExecutableIdentity{}
	state.released = true
	if cleanupErr != nil {
		state.releaseErr = errs.WrapMsg(
			errs.CodeInternal,
			"close pinned system executable descriptors",
			cleanupErr,
			errs.WithClass(errs.ClassInternal),
			errs.WithEntity(infra.SystemBinaryPath),
			errs.WithDetails(map[string]any{"pinned_executable_cleanup_failed": true}),
		)
	}
	return state.releaseErr
}

func verifyPinnedExecutableDirectory(
	ctx context.Context,
	deps pinnedExecutableDeps,
	fd int,
	component string,
) error {
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return pinnedExecutableAuthorityError(
			fmt.Sprintf("inspect trusted executable descriptor %s", component),
			err,
		)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return pinnedExecutableAuthorityError(
			fmt.Sprintf("trusted executable path %s must be a directory", component),
			errors.New("trusted executable component is not a directory"),
		)
	}
	if stat.Uid != 0 {
		return pinnedExecutableAuthorityError(
			fmt.Sprintf("trusted executable path %s must be owned by root", component),
			errors.New("trusted executable component has a non-root owner"),
		)
	}
	if stat.Mode&0022 != 0 {
		return pinnedExecutableAuthorityError(
			fmt.Sprintf("trusted executable path %s must not be group/world writable", component),
			errors.New("trusted executable component is writable by an untrusted identity"),
		)
	}
	return nil
}

func inspectPinnedExecutable(
	ctx context.Context,
	deps pinnedExecutableDeps,
	fd int,
) (unix.Stat_t, error) {
	if err := validatePinnedExecutableContext(ctx); err != nil {
		return unix.Stat_t{}, err
	}
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return unix.Stat_t{}, pinnedExecutableAuthorityError(
			"inspect canonical system executable descriptor",
			err,
		)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return unix.Stat_t{}, pinnedExecutableAuthorityError(
			fmt.Sprintf("system executable %s must be a regular file", infra.SystemBinaryPath),
			errors.New("canonical executable is not a regular file"),
		)
	}
	if stat.Uid != 0 || stat.Gid != 0 {
		return unix.Stat_t{}, pinnedExecutableAuthorityError(
			fmt.Sprintf("system executable %s must be owned by root:root", infra.SystemBinaryPath),
			errors.New("canonical executable owner is not root:root"),
		)
	}
	if stat.Mode&07777 != infra.ExecutablePerm {
		return unix.Stat_t{}, pinnedExecutableAuthorityError(
			fmt.Sprintf("system executable %s must have mode 0755", infra.SystemBinaryPath),
			errors.New("canonical executable mode is not exact"),
		)
	}
	return stat, nil
}

func hashPinnedExecutable(
	ctx context.Context,
	deps pinnedExecutableDeps,
	fd int,
	before unix.Stat_t,
) (PinnedExecutableIdentity, error) {
	digest := sha256.New()
	buffer := make([]byte, pinnedExecutableHashBufferBytes)
	var offset int64
	for {
		if err := validatePinnedExecutableContext(ctx); err != nil {
			return PinnedExecutableIdentity{}, err
		}
		count, err := deps.pread(ctx, fd, buffer, offset)
		if count < 0 || count > len(buffer) {
			return PinnedExecutableIdentity{}, pinnedExecutableAuthorityError(
				"hash canonical system executable",
				errors.New("executable read returned an invalid byte count"),
			)
		}
		if count > 0 {
			if _, writeErr := digest.Write(buffer[:count]); writeErr != nil {
				return PinnedExecutableIdentity{}, pinnedExecutableAuthorityError(
					"hash canonical system executable",
					writeErr,
				)
			}
			offset += int64(count)
			if offset > before.Size {
				return PinnedExecutableIdentity{}, pinnedExecutableAuthorityError(
					"hash canonical system executable",
					errors.New("executable grew while it was being hashed"),
				)
			}
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return PinnedExecutableIdentity{}, pinnedExecutableAuthorityError(
				"hash canonical system executable",
				err,
			)
		}
		if count == 0 || errors.Is(err, io.EOF) {
			break
		}
	}
	if offset != before.Size {
		return PinnedExecutableIdentity{}, pinnedExecutableAuthorityError(
			"hash canonical system executable",
			errors.New("executable size changed while it was being hashed"),
		)
	}
	after, err := inspectPinnedExecutable(ctx, deps, fd)
	if err != nil {
		return PinnedExecutableIdentity{}, err
	}
	if !samePinnedExecutableStat(before, after) {
		return PinnedExecutableIdentity{}, pinnedExecutableAuthorityError(
			"canonical system executable changed while it was being hashed",
			errors.New("executable metadata is not stable"),
		)
	}
	return pinnedExecutableIdentityFromStat(after, fmt.Sprintf("%x", digest.Sum(nil))), nil
}

func pinnedExecutableIdentityFromStat(stat unix.Stat_t, digest string) PinnedExecutableIdentity {
	return PinnedExecutableIdentity{
		Device:    stat.Dev,
		Inode:     stat.Ino,
		SizeBytes: uint64(stat.Size),
		Mode:      stat.Mode,
		UID:       stat.Uid,
		GID:       stat.Gid,
		Links:     stat.Nlink,
		SHA256:    digest,
	}
}

func samePinnedExecutableStat(first unix.Stat_t, second unix.Stat_t) bool {
	return first.Dev == second.Dev &&
		first.Ino == second.Ino &&
		first.Size == second.Size &&
		first.Mode == second.Mode &&
		first.Uid == second.Uid &&
		first.Gid == second.Gid &&
		first.Nlink == second.Nlink &&
		first.Mtim == second.Mtim &&
		first.Ctim == second.Ctim
}

func validatePinnedExecutableContext(ctx context.Context) error {
	if ctx == nil {
		return pinnedExecutableAuthorityError("pinned system executable context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return pinnedExecutableAuthorityError("pinned system executable context ended", err)
	}
	return nil
}

func pinnedExecutableAuthorityError(message string, cause error) *errs.DomainError {
	if cause == nil {
		cause = errors.New(message)
	}
	return errs.WrapMsg(
		errs.CodePrivilegeRequired,
		message,
		cause,
		errs.WithClass(errs.ClassNeedsInteraction),
		errs.WithEntity(infra.SystemBinaryPath),
	)
}

func appendPinnedExecutableCleanupError(primary error, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	if primary == nil {
		return cleanup
	}
	domain := errs.AsDomainError(primary)
	if domain == nil {
		return errs.WrapMsg(
			errs.CodeInternal,
			primary.Error(),
			errors.Join(primary, cleanup),
			errs.WithClass(errs.ClassInternal),
			errs.WithEntity(infra.SystemBinaryPath),
			errs.WithDetails(map[string]any{"pinned_executable_cleanup_failed": true}),
		)
	}
	result := *domain
	result.Details = maps.Clone(domain.Details)
	if result.Details == nil {
		result.Details = make(map[string]any)
	}
	result.Details["pinned_executable_cleanup_failed"] = true
	result.Err = errors.Join(domain.Err, cleanup)
	return &result
}

func realPinnedExecutableDeps() pinnedExecutableDeps {
	return pinnedExecutableDeps{
		executable: func(ctx context.Context) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			return os.Executable()
		},
		open: func(ctx context.Context, name string, flags int, mode uint32) (int, error) {
			if err := ctx.Err(); err != nil {
				return -1, err
			}
			return unix.Open(name, flags, mode)
		},
		openAt: func(ctx context.Context, directoryFD int, name string, flags int, mode uint32) (int, error) {
			if err := ctx.Err(); err != nil {
				return -1, err
			}
			return unix.Openat(directoryFD, name, flags, mode)
		},
		fstat: func(ctx context.Context, fd int, stat *unix.Stat_t) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return unix.Fstat(fd, stat)
		},
		pread: func(ctx context.Context, fd int, target []byte, offset int64) (int, error) {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			return unix.Pread(fd, target, offset)
		},
		close: func(_ context.Context, fd int) error {
			return unix.Close(fd)
		},
	}
}
