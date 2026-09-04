// Package rotating provides a context-aware rotating log writer.
// Layer: Lib leaf utility — imports only the standard library and external dependencies.
package rotating

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	managedFileMode = 0o600
	lockRetryDelay  = 5 * time.Millisecond

	logOpenFlags  = unix.O_WRONLY | unix.O_APPEND | unix.O_CLOEXEC | unix.O_NOFOLLOW
	lockOpenFlags = unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
)

// RotatingFileWriter writes complete records while serializing rotation across writer instances and processes.
//
// THREAD-SAFE: mu serializes the complete descriptor, rotation, write, and close lifecycle within one process.
type RotatingFileWriter struct {
	path        string
	lockPath    string
	maxBytes    int64
	backupCount int
	deps        writerDependencies

	// mu guards both descriptors and the complete write, rotation, and close lifecycle.
	mu       sync.Mutex
	fileFD   int
	lockFD   int
	closed   bool
	closeErr error
}

type writerDependencies struct {
	open   func(context.Context, string, int, uint32) (int, error)
	fchmod func(context.Context, int, uint32) error
	fstat  func(context.Context, int, *unix.Stat_t) error
	lstat  func(context.Context, string, *unix.Stat_t) error
	flock  func(context.Context, int, int) error
	close  func(context.Context, int) error
	rename func(context.Context, string, string) error
	write  func(context.Context, int, []byte) (int, error)
	wait   func(context.Context) error
	euid   func() uint32
}

// NewRotatingFileWriter opens or creates path and its stable sibling lock file.
func NewRotatingFileWriter(path string) (*RotatingFileWriter, error) {
	return newRotatingFileWriter(context.Background(), path, defaultWriterDependencies())
}

func newRotatingFileWriter(ctx context.Context, path string, deps writerDependencies) (*RotatingFileWriter, error) {
	writer := &RotatingFileWriter{
		path:        path,
		lockPath:    path + ".lock",
		maxBytes:    10 * 1024 * 1024,
		backupCount: 3,
		deps:        deps,
		fileFD:      -1,
		lockFD:      -1,
	}

	lockFD, _, err := openManagedFile(ctx, deps, writer.lockPath, lockOpenFlags, true)
	if err != nil {
		return nil, fmt.Errorf("rotating: initialize lock file: %w", err)
	}
	writer.lockFD = lockFD
	if err := writer.acquireLock(ctx); err != nil {
		return nil, writer.abortInitialization(ctx, fmt.Errorf("acquire lock: %w", err))
	}

	fileFD, _, openErr := openManagedFile(ctx, deps, writer.path, logOpenFlags, false)
	if openErr == nil {
		writer.fileFD = fileFD
	}
	unlockErr := writer.releaseLock(ctx)
	if err := errors.Join(openErr, unlockErr); err != nil {
		return nil, writer.abortInitialization(ctx, err)
	}
	return writer, nil
}

// Write writes p using a non-cancellable compatibility context.
func (w *RotatingFileWriter) Write(p []byte) (int, error) {
	return w.WriteContext(context.Background(), p)
}

// WriteContext writes p as one serialized record and observes cancellation while waiting for the cross-process lock.
func (w *RotatingFileWriter) WriteContext(ctx context.Context, p []byte) (n int, returnErr error) {
	if ctx == nil {
		return 0, fmt.Errorf("rotating: write: nil context")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, fmt.Errorf("rotating: write: %w", os.ErrClosed)
	}
	if err := w.acquireLock(ctx); err != nil {
		return 0, fmt.Errorf("rotating: write: acquire lock: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, w.releaseLock(context.WithoutCancel(ctx)))
	}()

	stat, err := w.ensureCurrentFile(ctx)
	if err != nil {
		return 0, fmt.Errorf("rotating: write: %w", err)
	}
	if shouldRotate(stat.Size, int64(len(p)), w.maxBytes) {
		if err := w.rotate(ctx, stat); err != nil {
			return 0, fmt.Errorf("rotating: write: rotate: %w", err)
		}
	}

	n, err = w.deps.write(ctx, w.fileFD, p)
	if err != nil {
		return n, fmt.Errorf("rotating: write record: %w", err)
	}
	if n != len(p) {
		return n, fmt.Errorf("rotating: write record: %w", io.ErrShortWrite)
	}
	return n, nil
}

// Close closes the retained descriptors exactly once and returns the cached first result.
func (w *RotatingFileWriter) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.closeErr
	}
	w.closed = true

	ctx := context.Background()
	var errs []error
	if w.fileFD >= 0 {
		errs = append(errs, wrapCloseError("log file", w.deps.close(ctx, w.fileFD)))
		w.fileFD = -1
	}
	if w.lockFD >= 0 {
		errs = append(errs, wrapCloseError("lock file", w.deps.close(ctx, w.lockFD)))
		w.lockFD = -1
	}
	w.closeErr = errors.Join(errs...)
	return w.closeErr
}

func (w *RotatingFileWriter) acquireLock(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := w.deps.flock(ctx, w.lockFD, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return errors.Join(contextErr, w.releaseLock(context.WithoutCancel(ctx)))
			}
			return nil
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return err
		}
		if err := w.deps.wait(ctx); err != nil {
			return err
		}
	}
}

func (w *RotatingFileWriter) releaseLock(ctx context.Context) error {
	if err := w.deps.flock(ctx, w.lockFD, unix.LOCK_UN); err != nil {
		return fmt.Errorf("rotating: unlock: %w", err)
	}
	return nil
}

func (w *RotatingFileWriter) ensureCurrentFile(ctx context.Context) (unix.Stat_t, error) {
	candidateFD, candidateStat, err := openManagedFile(ctx, w.deps, w.path, logOpenFlags, false)
	if err != nil {
		return unix.Stat_t{}, fmt.Errorf("reopen current log: %w", err)
	}
	if w.fileFD < 0 {
		w.fileFD = candidateFD
		return candidateStat, nil
	}

	var currentStat unix.Stat_t
	if err := w.deps.fstat(ctx, w.fileFD, &currentStat); err != nil {
		closeErr := w.deps.close(context.WithoutCancel(ctx), candidateFD)
		return unix.Stat_t{}, errors.Join(
			fmt.Errorf("stat open log: %w", err),
			wrapCloseError("reopened log file", closeErr),
		)
	}
	if currentStat.Dev == candidateStat.Dev && currentStat.Ino == candidateStat.Ino {
		if err := w.deps.close(context.WithoutCancel(ctx), candidateFD); err != nil {
			return unix.Stat_t{}, fmt.Errorf("close duplicate log file: %w", err)
		}
		return currentStat, nil
	}

	oldFD := w.fileFD
	w.fileFD = -1
	if err := w.deps.close(context.WithoutCancel(ctx), oldFD); err != nil {
		candidateCloseErr := w.deps.close(context.WithoutCancel(ctx), candidateFD)
		return unix.Stat_t{}, errors.Join(
			fmt.Errorf("close stale log file: %w", err),
			wrapCloseError("reopened log file", candidateCloseErr),
		)
	}
	w.fileFD = candidateFD
	return candidateStat, nil
}

func (w *RotatingFileWriter) rotate(ctx context.Context, currentStat unix.Stat_t) error {
	currentFD := w.fileFD
	w.fileFD = -1
	if err := w.deps.close(context.WithoutCancel(ctx), currentFD); err != nil {
		return fmt.Errorf("close current log: %w", err)
	}

	pathStat, exists, err := inspectManagedPath(ctx, w.deps, w.path)
	if err != nil {
		return fmt.Errorf("stat current log: %w", err)
	}
	if !exists || pathStat.Dev != currentStat.Dev || pathStat.Ino != currentStat.Ino {
		return fmt.Errorf("admit current log: path no longer names the open file")
	}

	for index := w.backupCount - 1; index >= 1; index-- {
		oldPath := fmt.Sprintf("%s.%d", w.path, index)
		_, oldExists, err := inspectManagedPath(ctx, w.deps, oldPath)
		if err != nil {
			return fmt.Errorf("admit backup %d: %w", index, err)
		}
		if !oldExists {
			continue
		}
		newPath := fmt.Sprintf("%s.%d", w.path, index+1)
		if _, _, err := inspectManagedPath(ctx, w.deps, newPath); err != nil {
			return fmt.Errorf("admit backup %d: %w", index+1, err)
		}
		if err := w.deps.rename(ctx, oldPath, newPath); err != nil {
			return fmt.Errorf("rename backup %d to %d: %w", index, index+1, err)
		}
	}

	firstBackup := w.path + ".1"
	if _, _, err := inspectManagedPath(ctx, w.deps, firstBackup); err != nil {
		return fmt.Errorf("admit backup 1: %w", err)
	}
	if err := w.deps.rename(ctx, w.path, firstBackup); err != nil {
		return fmt.Errorf("rename current log: %w", err)
	}

	fileFD, _, err := openManagedFile(ctx, w.deps, w.path, logOpenFlags, false)
	if err != nil {
		return fmt.Errorf("reopen rotated log: %w", err)
	}
	w.fileFD = fileFD
	return nil
}

func (w *RotatingFileWriter) abortInitialization(ctx context.Context, primary error) error {
	var cleanupErrors []error
	if w.fileFD >= 0 {
		cleanupErrors = append(
			cleanupErrors,
			wrapCloseError("log file", w.deps.close(context.WithoutCancel(ctx), w.fileFD)),
		)
		w.fileFD = -1
	}
	if w.lockFD >= 0 {
		cleanupErrors = append(
			cleanupErrors,
			wrapCloseError("lock file", w.deps.close(context.WithoutCancel(ctx), w.lockFD)),
		)
		w.lockFD = -1
	}
	return fmt.Errorf("rotating: initialize writer: %w", errors.Join(primary, errors.Join(cleanupErrors...)))
}

func openManagedFile(
	ctx context.Context,
	deps writerDependencies,
	path string,
	baseFlags int,
	requireEmpty bool,
) (int, unix.Stat_t, error) {
	// CRITICAL: The exclusive create distinguishes new files that need mode finalization; every existing-file open
	// rejects a symlink leaf and admits only the expected user's private, one-link regular file.
	fd, err := deps.open(ctx, path, baseFlags|unix.O_CREAT|unix.O_EXCL, managedFileMode)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = deps.open(ctx, path, baseFlags, 0)
	}
	if err != nil {
		return -1, unix.Stat_t{}, fmt.Errorf("open %q: %w", path, err)
	}
	abort := func(primary error) (int, unix.Stat_t, error) {
		closeErr := deps.close(context.WithoutCancel(ctx), fd)
		return -1, unix.Stat_t{}, errors.Join(primary, wrapCloseError(path, closeErr))
	}
	if created {
		if err := deps.fchmod(ctx, fd, managedFileMode); err != nil {
			return abort(fmt.Errorf("set mode on %q: %w", path, err))
		}
	}

	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return abort(fmt.Errorf("stat %q: %w", path, err))
	}
	if err := admitManagedFile(path, stat, deps.euid(), requireEmpty); err != nil {
		return abort(err)
	}
	return fd, stat, nil
}

func inspectManagedPath(ctx context.Context, deps writerDependencies, path string) (unix.Stat_t, bool, error) {
	var stat unix.Stat_t
	if err := deps.lstat(ctx, path, &stat); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return unix.Stat_t{}, false, nil
		}
		return unix.Stat_t{}, false, err
	}
	if err := admitManagedFile(path, stat, deps.euid(), false); err != nil {
		return unix.Stat_t{}, false, err
	}
	return stat, true, nil
}

func admitManagedFile(path string, stat unix.Stat_t, expectedEUID uint32, requireEmpty bool) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("admit %q: not a regular file", path)
	}
	if stat.Uid != expectedEUID {
		return fmt.Errorf("admit %q: unexpected owner", path)
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("admit %q: expected exactly one link", path)
	}
	if stat.Mode&0o7777 != managedFileMode {
		return fmt.Errorf("admit %q: mode is not 0600", path)
	}
	if requireEmpty && stat.Size != 0 {
		return fmt.Errorf("admit %q: lock file is not empty", path)
	}
	return nil
}

func shouldRotate(currentSize, writeSize, maxBytes int64) bool {
	if maxBytes <= 0 || writeSize == 0 {
		return false
	}
	return currentSize > maxBytes-writeSize
}

func wrapCloseError(description string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("close %s: %w", description, err)
}

func defaultWriterDependencies() writerDependencies {
	return writerDependencies{
		open: func(_ context.Context, path string, flags int, mode uint32) (int, error) {
			return unix.Open(path, flags, mode)
		},
		fchmod: func(_ context.Context, fd int, mode uint32) error {
			return unix.Fchmod(fd, mode)
		},
		fstat: func(_ context.Context, fd int, stat *unix.Stat_t) error {
			return unix.Fstat(fd, stat)
		},
		lstat: func(_ context.Context, path string, stat *unix.Stat_t) error {
			return unix.Lstat(path, stat)
		},
		flock: func(_ context.Context, fd, how int) error {
			return unix.Flock(fd, how)
		},
		close: func(_ context.Context, fd int) error {
			return unix.Close(fd)
		},
		rename: func(_ context.Context, oldPath, newPath string) error {
			return unix.Rename(oldPath, newPath)
		},
		write: func(_ context.Context, fd int, p []byte) (int, error) {
			return unix.Write(fd, p)
		},
		wait: func(ctx context.Context) error {
			timer := time.NewTimer(lockRetryDelay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
		euid: func() uint32 {
			return uint32(os.Geteuid())
		},
	}
}
