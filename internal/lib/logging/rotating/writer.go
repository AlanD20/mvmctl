// Package rotating provides a context-aware rotating log writer.
// Layer: Lib leaf utility — imports only the standard library and external dependencies.
package rotating

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const (
	managedFileMode = 0o600
	lockRetryDelay  = 5 * time.Millisecond

	logOpenFlags  = unix.O_WRONLY | unix.O_APPEND | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	lockOpenFlags = unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
)

// RotatingFileWriter writes complete records while serializing rotation across writer instances and processes.
//
// THREAD-SAFE: gate serializes the complete descriptor, rotation, write, and close lifecycle within one process.
type RotatingFileWriter struct {
	path        string
	lockPath    string
	maxBytes    int64
	backupCount int
	deps        writerDependencies

	// gate guards both descriptors and the complete write, rotation, and close lifecycle.
	gate      chan struct{}
	fileFD    int
	lockFD    int
	closed    bool
	closeErr  error
	poisoned  bool
	poisonErr error
}

type writerDependencies struct {
	open   func(context.Context, string, int, uint32) (int, error)
	fchmod func(context.Context, int, uint32) error
	fstat  func(context.Context, int, *unix.Stat_t) error
	lstat  func(context.Context, string, *unix.Stat_t) error
	flock  func(context.Context, int, int) error
	close  func(context.Context, int) error
	rename func(context.Context, string, string) error
	unlink func(context.Context, string) error
	write  func(context.Context, int, []byte) (int, error)
	wait   func(context.Context) error
	euid   func() uint32
}

// NewRotatingFileWriter opens or creates path and its stable sibling lock file.
func NewRotatingFileWriter(ctx context.Context, path string) (*RotatingFileWriter, error) {
	if ctx == nil {
		return nil, fmt.Errorf("rotating: initialize writer: nil context")
	}
	return newRotatingFileWriter(ctx, path, defaultWriterDependencies())
}

func newRotatingFileWriter(ctx context.Context, path string, deps writerDependencies) (*RotatingFileWriter, error) {
	writer := &RotatingFileWriter{
		path:        path,
		lockPath:    path + ".lock",
		maxBytes:    10 * 1024 * 1024,
		backupCount: 3,
		deps:        deps,
		gate:        make(chan struct{}, 1),
		fileFD:      -1,
		lockFD:      -1,
	}
	writer.gate <- struct{}{}

	lockFD, _, err := openManagedFile(ctx, deps, writer.lockPath, lockOpenFlags, true)
	if err != nil {
		return nil, fmt.Errorf("rotating: initialize lock file: %w", err)
	}
	writer.lockFD = lockFD
	if err := writer.acquireLock(ctx); err != nil {
		return nil, writer.abortInitialization(ctx, fmt.Errorf("acquire lock: %w", err))
	}
	if err := ctx.Err(); err != nil {
		unlockErr := writer.releaseLock(context.WithoutCancel(ctx))
		return nil, writer.abortInitialization(
			ctx,
			errors.Join(fmt.Errorf("context after acquiring lock: %w", err), unlockErr),
		)
	}

	stageErr := ensureRotationStagesAbsent(ctx, deps, writer.path, writer.backupCount)
	var fileFD int
	var openErr error
	if stageErr == nil {
		fileFD, _, openErr = openManagedFile(ctx, deps, writer.path, logOpenFlags, false)
	} else {
		openErr = stageErr
	}
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
	if err := w.acquireLifecycle(ctx); err != nil {
		return 0, fmt.Errorf("rotating: write: acquire lifecycle: %w", err)
	}
	defer w.releaseLifecycle()
	if w.closed {
		return 0, fmt.Errorf("rotating: write: %w", os.ErrClosed)
	}
	if w.poisoned {
		return 0, w.poisonErr
	}
	if err := w.acquireLock(ctx); err != nil {
		return 0, fmt.Errorf("rotating: write: acquire lock: %w", err)
	}
	defer func() {
		if unlockErr := w.releaseLock(context.WithoutCancel(ctx)); unlockErr != nil {
			returnErr = errors.Join(returnErr, w.poison(context.WithoutCancel(ctx), unlockErr))
		}
	}()
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("rotating: write: context after acquiring lock: %w", err)
	}

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
	if n < 0 || n > len(p) {
		invalidCountErr := fmt.Errorf("rotating: write record: invalid byte count %d for %d-byte input", n, len(p))
		return 0, errors.Join(invalidCountErr, err)
	}
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
	if err := w.acquireLifecycle(context.Background()); err != nil {
		return err
	}
	defer w.releaseLifecycle()
	if w.closed {
		return w.closeErr
	}
	w.closed = true
	if w.poisoned {
		w.closeErr = w.poisonErr
		return w.closeErr
	}

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

func (w *RotatingFileWriter) acquireLifecycle(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.gate:
		if err := ctx.Err(); err != nil {
			w.releaseLifecycle()
			return err
		}
		return nil
	}
}

func (w *RotatingFileWriter) releaseLifecycle() {
	w.gate <- struct{}{}
}

func (w *RotatingFileWriter) acquireLock(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := w.deps.flock(ctx, w.lockFD, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
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
	for {
		if err := w.deps.flock(ctx, w.lockFD, unix.LOCK_UN); err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return fmt.Errorf("rotating: unlock: %w", err)
		}
		return nil
	}
}

func (w *RotatingFileWriter) poison(ctx context.Context, cause error) error {
	if w.poisoned {
		return w.poisonErr
	}
	w.poisoned = true
	var poisonErrors []error
	poisonErrors = append(poisonErrors, cause)
	if w.fileFD >= 0 {
		poisonErrors = append(poisonErrors, wrapCloseError("log file", w.deps.close(ctx, w.fileFD)))
		w.fileFD = -1
	}
	if w.lockFD >= 0 {
		poisonErrors = append(poisonErrors, wrapCloseError("lock file", w.deps.close(ctx, w.lockFD)))
		w.lockFD = -1
	}
	w.poisonErr = fmt.Errorf("rotating: writer poisoned after unlock failure: %w", errors.Join(poisonErrors...))
	return w.poisonErr
}

func (w *RotatingFileWriter) ensureCurrentFile(ctx context.Context) (unix.Stat_t, error) {
	if err := ensureRotationStagesAbsent(ctx, w.deps, w.path, w.backupCount); err != nil {
		return unix.Stat_t{}, err
	}
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
	transaction, err := prepareRotation(ctx, w, currentStat)
	if err != nil {
		return err
	}
	currentFD := w.fileFD
	w.fileFD = -1
	if err := w.deps.close(context.WithoutCancel(ctx), currentFD); err != nil {
		return fmt.Errorf("close current log: %w", err)
	}

	rollback := func(primary error) error {
		return errors.Join(primary, transaction.rollback(context.WithoutCancel(ctx)))
	}
	for index := range transaction.entries {
		entry := &transaction.entries[index]
		if !entry.exists {
			continue
		}
		if err := transaction.move(ctx, index, entry.stage); err != nil {
			return rollback(fmt.Errorf("stage rotation entry %d: %w", index, err))
		}
	}

	for index := 0; index < w.backupCount; index++ {
		entry := &transaction.entries[index]
		if !entry.exists {
			continue
		}
		destination := transaction.entries[index+1].original
		if err := transaction.move(ctx, index, destination); err != nil {
			return rollback(fmt.Errorf("publish rotation entry %d: %w", index, err))
		}
	}

	fileFD, fileStat, err := openManagedFile(ctx, w.deps, w.path, logOpenFlags, false)
	if err != nil {
		return rollback(fmt.Errorf("reopen rotated log: %w", err))
	}
	w.fileFD = fileFD
	transaction.newCurrentFD = fileFD
	transaction.newCurrentStat = fileStat

	oldest := &transaction.entries[len(transaction.entries)-1]
	if oldest.exists {
		if err := transaction.removeOldest(ctx, oldest); err != nil {
			return rollback(fmt.Errorf("remove oldest backup: %w", err))
		}
	}
	transaction.committed = true
	return nil
}

type rotationEntry struct {
	original string
	stage    string
	location string
	stat     unix.Stat_t
	exists   bool
}

type rotationTransaction struct {
	writer         *RotatingFileWriter
	entries        []rotationEntry
	newCurrentFD   int
	newCurrentStat unix.Stat_t
	committed      bool
}

func prepareRotation(
	ctx context.Context,
	writer *RotatingFileWriter,
	currentStat unix.Stat_t,
) (*rotationTransaction, error) {
	transaction := &rotationTransaction{writer: writer, newCurrentFD: -1}
	for index := 0; index <= writer.backupCount; index++ {
		original := writer.path
		if index > 0 {
			original = fmt.Sprintf("%s.%d", writer.path, index)
		}
		stat, exists, err := inspectManagedPath(ctx, writer.deps, original)
		if err != nil {
			return nil, fmt.Errorf("inspect rotation entry %d: %w", index, err)
		}
		if index == 0 && (!exists || !sameFileIdentity(stat, currentStat)) {
			return nil, fmt.Errorf("admit current log: path no longer names the open file")
		}
		stage := rotationStagePath(writer.path, index)
		if _, stageExists, err := inspectManagedPath(ctx, writer.deps, stage); err != nil {
			return nil, fmt.Errorf("inspect rotation stage %d: %w", index, err)
		} else if stageExists {
			return nil, fmt.Errorf("admit rotation stage %d: unfinished rotation state exists", index)
		}
		transaction.entries = append(transaction.entries, rotationEntry{
			original: original,
			stage:    stage,
			location: original,
			stat:     stat,
			exists:   exists,
		})
	}
	return transaction, nil
}

func (transaction *rotationTransaction) move(ctx context.Context, index int, destination string) error {
	entry := &transaction.entries[index]
	if !entry.exists {
		return nil
	}
	source := entry.location
	if err := verifyExpectedPath(ctx, transaction.writer.deps, source, entry.stat); err != nil {
		return err
	}
	if _, exists, err := inspectManagedPath(ctx, transaction.writer.deps, destination); err != nil {
		return fmt.Errorf("inspect rename destination %q: %w", destination, err)
	} else if exists {
		return fmt.Errorf("admit rename destination %q: path already exists", destination)
	}

	renameErr := transaction.writer.deps.rename(ctx, source, destination)
	locatedAt, verifyErr := locateExpectedPath(ctx, transaction.writer.deps, entry.stat, source, destination)
	if locatedAt != "" {
		entry.location = locatedAt
	}
	if renameErr != nil {
		renameErr = fmt.Errorf("rename %q to %q: %w", source, destination, renameErr)
	}
	return errors.Join(renameErr, verifyErr)
}

func (transaction *rotationTransaction) removeOldest(ctx context.Context, entry *rotationEntry) error {
	if err := verifyExpectedPath(ctx, transaction.writer.deps, entry.location, entry.stat); err != nil {
		return err
	}
	evidencePath := rotationPruneEvidencePath(transaction.writer.path)
	evidenceStat, err := createRotationEvidence(ctx, transaction.writer.deps, evidencePath)
	if err != nil {
		return fmt.Errorf("create prune evidence: %w", err)
	}

	unlinkErr := transaction.writer.deps.unlink(ctx, entry.location)
	remainingStat, exists, inspectErr := inspectManagedPath(ctx, transaction.writer.deps, entry.location)
	if inspectErr != nil {
		return errors.Join(unlinkErr, fmt.Errorf("inspect oldest backup after unlink: %w", inspectErr))
	}
	if exists {
		var outcomeErr error
		if sameFileIdentity(remainingStat, entry.stat) {
			outcomeErr = unlinkErr
			if outcomeErr == nil {
				outcomeErr = fmt.Errorf("unlink oldest backup reported success but inode remains")
			}
		} else {
			outcomeErr = errors.Join(unlinkErr, fmt.Errorf("oldest backup inode changed during unlink"))
			// An unexpected inode at the stage path is itself fail-closed evidence. Retain the explicit evidence too.
			return outcomeErr
		}
		cleanupErr := unlinkExpectedPath(
			context.WithoutCancel(ctx),
			transaction.writer.deps,
			evidencePath,
			evidenceStat,
		)
		return errors.Join(outcomeErr, wrapCleanupError("prune evidence", cleanupErr))
	}

	entry.exists = false
	entry.location = ""
	transaction.committed = true
	cleanupErr := unlinkExpectedPath(
		context.WithoutCancel(ctx),
		transaction.writer.deps,
		evidencePath,
		evidenceStat,
	)
	return errors.Join(unlinkErr, wrapCleanupError("prune evidence", cleanupErr))
}

func (transaction *rotationTransaction) rollback(ctx context.Context) error {
	if transaction.committed {
		return nil
	}
	var rollbackErrors []error
	if transaction.newCurrentFD >= 0 {
		rollbackErrors = append(
			rollbackErrors,
			wrapCloseError("new current log", transaction.writer.deps.close(ctx, transaction.newCurrentFD)),
		)
		transaction.writer.fileFD = -1
		transaction.newCurrentFD = -1
		if err := unlinkExpectedPath(
			ctx,
			transaction.writer.deps,
			transaction.writer.path,
			transaction.newCurrentStat,
		); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("remove new current log: %w", err))
		}
	}

	for index := range transaction.entries {
		entry := &transaction.entries[index]
		if !entry.exists || entry.location == entry.stage {
			continue
		}
		if err := transaction.move(ctx, index, entry.stage); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restage rotation entry %d: %w", index, err))
		}
	}
	for index := len(transaction.entries) - 1; index >= 0; index-- {
		entry := &transaction.entries[index]
		if !entry.exists || entry.location != entry.stage {
			continue
		}
		if err := transaction.move(ctx, index, entry.original); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore rotation entry %d: %w", index, err))
		}
	}
	if err := errors.Join(rollbackErrors...); err != nil {
		return fmt.Errorf("rollback rotation: %w", err)
	}
	return nil
}

func ensureRotationStagesAbsent(
	ctx context.Context,
	deps writerDependencies,
	path string,
	backupCount int,
) error {
	for index := 0; index <= backupCount; index++ {
		stage := rotationStagePath(path, index)
		if _, exists, err := inspectManagedPath(ctx, deps, stage); err != nil {
			return fmt.Errorf("inspect rotation stage %d: %w", index, err)
		} else if exists {
			return fmt.Errorf("admit rotation stage %d: unfinished rotation state exists", index)
		}
	}
	evidencePath := rotationPruneEvidencePath(path)
	if _, exists, err := inspectManagedPath(ctx, deps, evidencePath); err != nil {
		return fmt.Errorf("inspect rotation prune evidence: %w", err)
	} else if exists {
		return fmt.Errorf("admit rotation prune evidence: unfinished rotation state exists")
	}
	return nil
}

func rotationStagePath(path string, index int) string {
	return fmt.Sprintf("%s.rotate-stage-%d", path, index)
}

func rotationPruneEvidencePath(path string) string {
	return path + ".rotate-prune"
}

func createRotationEvidence(
	ctx context.Context,
	deps writerDependencies,
	path string,
) (unix.Stat_t, error) {
	fd, err := deps.open(ctx, path, logOpenFlags|unix.O_CREAT|unix.O_EXCL, managedFileMode)
	if err != nil {
		return unix.Stat_t{}, fmt.Errorf("create %q: %w", path, err)
	}
	abort := func(primary error) (unix.Stat_t, error) {
		closeErr := deps.close(context.WithoutCancel(ctx), fd)
		return unix.Stat_t{}, errors.Join(primary, wrapCloseError(path, closeErr))
	}
	if err := deps.fchmod(ctx, fd, managedFileMode); err != nil {
		return abort(fmt.Errorf("set mode on %q: %w", path, err))
	}
	var stat unix.Stat_t
	if err := deps.fstat(ctx, fd, &stat); err != nil {
		return abort(fmt.Errorf("stat %q: %w", path, err))
	}
	if err := admitManagedFile(path, stat, deps.euid(), true); err != nil {
		return abort(err)
	}
	if err := deps.close(context.WithoutCancel(ctx), fd); err != nil {
		return unix.Stat_t{}, wrapCloseError(path, err)
	}
	return stat, nil
}

func verifyExpectedPath(ctx context.Context, deps writerDependencies, path string, expected unix.Stat_t) error {
	stat, exists, err := inspectManagedPath(ctx, deps, path)
	if err != nil {
		return fmt.Errorf("inspect %q before rename: %w", path, err)
	}
	if !exists || !sameFileIdentity(stat, expected) {
		return fmt.Errorf("admit %q before rename: inode changed", path)
	}
	return nil
}

func locateExpectedPath(
	ctx context.Context,
	deps writerDependencies,
	expected unix.Stat_t,
	source string,
	destination string,
) (string, error) {
	sourceStat, sourceExists, sourceErr := inspectManagedPath(ctx, deps, source)
	destinationStat, destinationExists, destinationErr := inspectManagedPath(ctx, deps, destination)
	if err := errors.Join(sourceErr, destinationErr); err != nil {
		return "", fmt.Errorf("verify rename result: %w", err)
	}
	sourceMatches := sourceExists && sameFileIdentity(sourceStat, expected)
	destinationMatches := destinationExists && sameFileIdentity(destinationStat, expected)
	switch {
	case sourceMatches && !destinationExists:
		return source, nil
	case destinationMatches && !sourceExists:
		return destination, nil
	default:
		return "", fmt.Errorf("verify rename result: expected inode has ambiguous location")
	}
}

func unlinkExpectedPath(ctx context.Context, deps writerDependencies, path string, expected unix.Stat_t) error {
	if err := verifyExpectedPath(ctx, deps, path, expected); err != nil {
		return err
	}
	unlinkErr := deps.unlink(ctx, path)
	stat, exists, inspectErr := inspectManagedPath(ctx, deps, path)
	if inspectErr != nil {
		return errors.Join(unlinkErr, inspectErr)
	}
	if !exists {
		return unlinkErr
	}
	if sameFileIdentity(stat, expected) {
		return errors.Join(unlinkErr, fmt.Errorf("unlinked path still exists"))
	}
	return errors.Join(unlinkErr, fmt.Errorf("unlinked path names an unexpected inode"))
}

func sameFileIdentity(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino
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
	if maxBytes <= 0 || currentSize == 0 || writeSize == 0 {
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

func wrapCleanupError(description string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("clean up %s: %w", description, err)
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
			return unix.Renameat2(
				unix.AT_FDCWD,
				oldPath,
				unix.AT_FDCWD,
				newPath,
				unix.RENAME_NOREPLACE,
			)
		},
		unlink: func(_ context.Context, path string) error {
			return unix.Unlink(path)
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
