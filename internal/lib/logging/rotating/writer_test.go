package rotating

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestRotatingFileWriterWriteContextPersistsOneCompleteRecord(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	writer := newTestWriter(t, path)

	record := []byte("request=first result=allowed\n")
	written, err := writer.WriteContext(t.Context(), record)
	require.NoError(t, err)
	assert.Equal(t, len(record), written)
	assertFileContent(t, path, record)
}

func TestRotatingFileWriterWriteCompatibilityPersistsRecord(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	writer := newTestWriter(t, path)

	record := []byte("compatibility-write\n")
	written, err := writer.Write(record)
	require.NoError(t, err)
	assert.Equal(t, len(record), written)
	assertFileContent(t, path, record)
}

func TestNewRotatingFileWriterContextCancelsWhileStableLockIsHeld(t *testing.T) {
	// Rationale: constructor admission performs filesystem effects and must not wait forever behind another process.
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	first := newTestWriter(t, path)
	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseWrite) }) })
	realWrite := first.deps.write
	first.deps.write = func(ctx context.Context, fd int, p []byte) (int, error) {
		close(writeStarted)
		if err := waitForRelease(releaseWrite); err != nil {
			return 0, err
		}
		return realWrite(ctx, fd, p)
	}
	firstResult := make(chan writeResult, 1)
	go func() {
		n, err := first.WriteContext(context.Background(), []byte("held-record\n"))
		firstResult <- writeResult{n: n, err: err}
	}()
	awaitSignal(t, writeStarted)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	constructorResult := make(chan writerResult, 1)
	go func() {
		writer, err := NewRotatingFileWriter(ctx, path)
		constructorResult <- writerResult{writer: writer, err: err}
	}()
	result := awaitWriterResult(t, constructorResult)
	assert.Nil(t, result.writer)
	require.Error(t, result.err)
	assert.ErrorIs(t, result.err, context.DeadlineExceeded)

	releaseOnce.Do(func() { close(releaseWrite) })
	require.NoError(t, awaitResult(t, firstResult).err)
}

func TestNewRotatingFileWriterRechecksCancellationAfterAcquiringStableLock(t *testing.T) {
	// Rationale: cancellation can race a successful nonblocking flock. Constructor effects after the lock must not
	// begin, and an unlock failure must still detach the retained lock descriptor.
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	ctx, cancel := context.WithCancel(t.Context())
	deps := defaultWriterDependencies()
	realOpen := deps.open
	openedLog := false
	lockFD := -1
	deps.open = func(ctx context.Context, openedPath string, flags int, mode uint32) (int, error) {
		fd, err := realOpen(ctx, openedPath, flags, mode)
		if openedPath == path && err == nil {
			openedLog = true
		}
		if openedPath == path+".lock" && err == nil {
			lockFD = fd
		}
		return fd, err
	}
	realFlock := deps.flock
	unlockFailure := errors.New("injected canceled-constructor unlock failure")
	deps.flock = func(ctx context.Context, fd, how int) error {
		if how == unix.LOCK_UN {
			return unlockFailure
		}
		err := realFlock(ctx, fd, how)
		if err == nil {
			cancel()
		}
		return err
	}
	realClose := deps.close
	var closedFDs []int
	deps.close = func(ctx context.Context, fd int) error {
		closedFDs = append(closedFDs, fd)
		return realClose(ctx, fd)
	}

	writer, err := newRotatingFileWriter(ctx, path, deps)
	assert.Nil(t, writer)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, err, unlockFailure)
	assert.False(t, openedLog)
	assert.Equal(t, []int{lockFD}, closedFDs)
}

func TestNewRotatingFileWriterRejectsFIFOsWithoutBlocking(t *testing.T) {
	// Rationale: a blocking open of an attacker-supplied FIFO can hang logging initialization before type admission.
	t.Parallel()

	for _, leaf := range []string{"log", "lock"} {
		t.Run(leaf, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.log")
			fifoPath := path
			if leaf == "lock" {
				fifoPath += ".lock"
			}
			require.NoError(t, unix.Mkfifo(fifoPath, managedFileMode))

			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			result := make(chan writerResult, 1)
			go func() {
				writer, err := NewRotatingFileWriter(ctx, path)
				result <- writerResult{writer: writer, err: err}
			}()
			got := awaitWriterResult(t, result)
			assert.Nil(t, got.writer)
			require.Error(t, got.err)
			assert.NotErrorIs(t, got.err, context.DeadlineExceeded, "FIFO admission must reject without waiting")
		})
	}
}

func TestRotatingFileWriterIndependentWritersSerializeRotationAndRefreshStaleInodes(t *testing.T) {
	// Rationale: every writer retains its own descriptor, so cross-writer rotation must force the other writer to
	// discover the replacement inode before it computes size or appends a record.
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	first := newTestWriter(t, path)
	second := newTestWriter(t, path)
	first.maxBytes = 8
	second.maxBytes = 8
	first.backupCount = 2
	second.backupCount = 2

	_, err := first.WriteContext(t.Context(), []byte("aaaa\n"))
	require.NoError(t, err)
	_, err = second.WriteContext(t.Context(), []byte("bbbb\n"))
	require.NoError(t, err)
	_, err = first.WriteContext(t.Context(), []byte("cccc\n"))
	require.NoError(t, err)

	assertFileContent(t, path, []byte("cccc\n"))
	assertFileContent(t, path+".1", []byte("bbbb\n"))
	assertFileContent(t, path+".2", []byte("aaaa\n"))
}

func TestRotatingFileWriterCancellationWhileAnotherWriterHoldsStableLock(t *testing.T) {
	// Rationale: a process that stalls while writing must not make another caller ignore its cancellation deadline.
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	first := newTestWriter(t, path)
	second := newTestWriter(t, path)

	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseWrite) }) })
	realWrite := first.deps.write
	first.deps.write = func(ctx context.Context, fd int, p []byte) (int, error) {
		close(writeStarted)
		if err := waitForRelease(releaseWrite); err != nil {
			return 0, err
		}
		return realWrite(ctx, fd, p)
	}
	firstResult := make(chan writeResult, 1)
	go func() {
		n, err := first.WriteContext(context.Background(), []byte("held-record\n"))
		firstResult <- writeResult{n: n, err: err}
	}()
	awaitSignal(t, writeStarted)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	written, err := second.WriteContext(ctx, []byte("must-not-appear\n"))
	require.Error(t, err)
	assert.Zero(t, written)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	releaseOnce.Do(func() { close(releaseWrite) })
	result := awaitResult(t, firstResult)
	require.NoError(t, result.err)
	assert.Equal(t, len("held-record\n"), result.n)
	assertFileContent(t, path, []byte("held-record\n"))
}

func TestRotatingFileWriterCancellationWhileSameWriterIsBusy(t *testing.T) {
	// Rationale: local lifecycle serialization must not hide cancellation behind an operation using the same writer.
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	writer := newTestWriter(t, path)
	writeStarted := make(chan struct{})
	releaseWrite := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseWrite) }) })
	realWrite := writer.deps.write
	writer.deps.write = func(ctx context.Context, fd int, p []byte) (int, error) {
		close(writeStarted)
		if err := waitForRelease(releaseWrite); err != nil {
			return 0, err
		}
		return realWrite(ctx, fd, p)
	}
	firstResult := make(chan writeResult, 1)
	go func() {
		n, err := writer.WriteContext(context.Background(), []byte("held-record\n"))
		firstResult <- writeResult{n: n, err: err}
	}()
	awaitSignal(t, writeStarted)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	secondResult := make(chan writeResult, 1)
	go func() {
		n, err := writer.WriteContext(ctx, []byte("must-not-appear\n"))
		secondResult <- writeResult{n: n, err: err}
	}()
	result := awaitResult(t, secondResult)
	require.Error(t, result.err)
	assert.Zero(t, result.n)
	assert.ErrorIs(t, result.err, context.DeadlineExceeded)

	releaseOnce.Do(func() { close(releaseWrite) })
	require.NoError(t, awaitResult(t, firstResult).err)
}

func TestRotatingFileWriterRechecksCancellationAfterAcquiringStableLock(t *testing.T) {
	// Rationale: a successful flock racing cancellation must install unlock ownership, then stop before reopening or
	// writing the current log.
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	writer := newTestWriter(t, path)
	ctx, cancel := context.WithCancel(t.Context())
	realFlock := writer.deps.flock
	unlockCalls := 0
	writer.deps.flock = func(ctx context.Context, fd, how int) error {
		if how == unix.LOCK_UN {
			unlockCalls++
			return realFlock(ctx, fd, how)
		}
		err := realFlock(ctx, fd, how)
		if err == nil {
			cancel()
		}
		return err
	}
	writeCalls := 0
	writer.deps.write = func(context.Context, int, []byte) (int, error) {
		writeCalls++
		return 0, nil
	}

	n, err := writer.WriteContext(ctx, []byte("must-not-appear\n"))
	assert.Zero(t, n)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, writeCalls)
	assert.Equal(t, 1, unlockCalls)
	assertFileContent(t, path, []byte{})
}

func TestRotatingFileWriterRefreshesExternallyReplacedCurrentPath(t *testing.T) {
	// Rationale: external rotation changes the path inode without changing the retained descriptor.
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "audit.log")
	detachedPath := filepath.Join(directory, "detached.log")
	writer := newTestWriter(t, path)

	_, err := writer.WriteContext(t.Context(), []byte("old-inode\n"))
	require.NoError(t, err)
	require.NoError(t, os.Rename(path, detachedPath))
	require.NoError(t, os.WriteFile(path, []byte("replacement\n"), managedFileMode))
	require.NoError(t, os.Chmod(path, managedFileMode))

	_, err = writer.WriteContext(t.Context(), []byte("new-inode\n"))
	require.NoError(t, err)
	assertFileContent(t, detachedPath, []byte("old-inode\n"))
	assertFileContent(t, path, []byte("replacement\nnew-inode\n"))
}

func TestRotatingFileWriterFailedRotationPreservesHistoryAndAllowsFreshRetry(t *testing.T) {
	// Rationale: a partial backup shift can overwrite the oldest retained record and make every retry compound the
	// damage. Each rename and reopen boundary must restore the exact pre-rotation namespace before returning an error.
	tests := []struct {
		name       string
		failRename int
		failReopen bool
		failRemove bool
	}{
		{name: "stage current", failRename: 1},
		{name: "stage backup one", failRename: 2},
		{name: "stage backup two", failRename: 3},
		{name: "stage backup three", failRename: 4},
		{name: "publish current", failRename: 5},
		{name: "publish backup one", failRename: 6},
		{name: "publish backup two", failRename: 7},
		{name: "reopen current", failReopen: true},
		{name: "remove oldest", failRemove: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.log")
			seedRotationHistory(t, path)
			writer := newTestWriter(t, path)
			writer.maxBytes = 1
			injected := errors.New("injected rotation boundary failure")

			if test.failRename > 0 {
				realRename := writer.deps.rename
				renameCalls := 0
				writer.deps.rename = func(ctx context.Context, oldPath, newPath string) error {
					renameCalls++
					if renameCalls == test.failRename {
						return injected
					}
					return realRename(ctx, oldPath, newPath)
				}
			}
			if test.failReopen {
				realOpen := writer.deps.open
				createAttempts := 0
				writer.deps.open = func(ctx context.Context, openedPath string, flags int, mode uint32) (int, error) {
					if openedPath == path && flags&unix.O_EXCL != 0 {
						createAttempts++
						if createAttempts == 2 {
							return -1, injected
						}
					}
					return realOpen(ctx, openedPath, flags, mode)
				}
			}
			if test.failRemove {
				realUnlink := writer.deps.unlink
				failed := false
				writer.deps.unlink = func(ctx context.Context, removedPath string) error {
					if !failed {
						failed = true
						return injected
					}
					return realUnlink(ctx, removedPath)
				}
			}

			n, err := writer.WriteContext(t.Context(), []byte("new-record\n"))
			assert.Zero(t, n)
			require.Error(t, err)
			assert.ErrorIs(t, err, injected)
			require.NoError(t, writer.Close())
			assertRotationHistory(t, path, originalRotationHistory())

			retry, err := NewRotatingFileWriter(t.Context(), path)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, retry.Close()) })
			retry.maxBytes = 1
			_, err = retry.WriteContext(t.Context(), []byte("retry-record\n"))
			require.NoError(t, err)
			assertRotationHistory(t, path, map[string]string{
				"current": "retry-record\n",
				".1":      "current\n",
				".2":      "backup-one\n",
				".3":      "backup-two\n",
			})
		})
	}
}

func TestRotatingFileWriterRollbackFailureLeavesDiscoverableFailClosedState(t *testing.T) {
	// Rationale: if rollback itself fails, its retained stage must prevent a new writer from compounding history loss.
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	seedRotationHistory(t, path)
	writer := newTestWriter(t, path)
	writer.maxBytes = 1
	primaryFailure := errors.New("injected publish failure")
	rollbackFailure := errors.New("injected rollback failure")
	realRename := writer.deps.rename
	renameCalls := 0
	writer.deps.rename = func(ctx context.Context, oldPath, newPath string) error {
		renameCalls++
		switch renameCalls {
		case 5:
			return primaryFailure
		case 6:
			return rollbackFailure
		default:
			return realRename(ctx, oldPath, newPath)
		}
	}

	_, err := writer.WriteContext(t.Context(), []byte("new-record\n"))
	require.Error(t, err)
	assert.ErrorIs(t, err, primaryFailure)
	assert.ErrorIs(t, err, rollbackFailure)
	require.NoError(t, writer.Close())
	assertFileContent(t, rotationStagePath(path, 3), []byte("backup-three\n"))

	reopened, reopenErr := NewRotatingFileWriter(t.Context(), path)
	assert.Nil(t, reopened)
	require.Error(t, reopenErr)
	assert.Contains(t, reopenErr.Error(), "unfinished rotation state")
}

func TestRotatingFileWriterEffectfulUnlinkErrorCommitsPrunedRotation(t *testing.T) {
	// Rationale: unlink can report an injected error after removing the oldest stage. A verified-absent inode means
	// the rotation committed and a retry must write the empty current file instead of rotating it again.
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	seedRotationHistory(t, path)
	writer := newTestWriter(t, path)
	writer.maxBytes = 1
	injected := errors.New("injected effectful unlink failure")
	realUnlink := writer.deps.unlink
	effectApplied := false
	writer.deps.unlink = func(ctx context.Context, removedPath string) error {
		if !effectApplied && removedPath == rotationStagePath(path, writer.backupCount) {
			effectApplied = true
			require.NoError(t, realUnlink(ctx, removedPath))
			return injected
		}
		return realUnlink(ctx, removedPath)
	}

	n, err := writer.WriteContext(t.Context(), []byte("new-record\n"))
	assert.Zero(t, n)
	require.Error(t, err)
	assert.ErrorIs(t, err, injected)
	assertRotationHistory(t, path, map[string]string{
		"current": "",
		".1":      "current\n",
		".2":      "backup-one\n",
		".3":      "backup-two\n",
	})
	require.NoError(t, writer.Close())

	retry, err := NewRotatingFileWriter(t.Context(), path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, retry.Close()) })
	retry.maxBytes = 1
	_, err = retry.WriteContext(t.Context(), []byte("retry-record\n"))
	require.NoError(t, err)
	assertRotationHistory(t, path, map[string]string{
		"current": "retry-record\n",
		".1":      "current\n",
		".2":      "backup-one\n",
		".3":      "backup-two\n",
	})
}

func TestRotatingFileWriterPostUnlinkInspectionFailureLeavesFailClosedEvidence(t *testing.T) {
	// Rationale: if the oldest inode cannot be observed after unlink, persistent transaction evidence must keep a fresh
	// writer from interpreting an indeterminate namespace as a safe new rotation starting point.
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	seedRotationHistory(t, path)
	writer := newTestWriter(t, path)
	writer.maxBytes = 1
	inspectionFailure := errors.New("injected post-unlink inspection failure")
	oldestStage := rotationStagePath(path, writer.backupCount)
	realUnlink := writer.deps.unlink
	unlinkedOldest := false
	writer.deps.unlink = func(ctx context.Context, removedPath string) error {
		err := realUnlink(ctx, removedPath)
		if removedPath == oldestStage && err == nil {
			unlinkedOldest = true
		}
		return err
	}
	realLstat := writer.deps.lstat
	failedInspection := false
	writer.deps.lstat = func(ctx context.Context, inspectedPath string, stat *unix.Stat_t) error {
		if unlinkedOldest && !failedInspection && inspectedPath == oldestStage {
			failedInspection = true
			return inspectionFailure
		}
		return realLstat(ctx, inspectedPath, stat)
	}

	n, err := writer.WriteContext(t.Context(), []byte("new-record\n"))
	assert.Zero(t, n)
	require.Error(t, err)
	assert.ErrorIs(t, err, inspectionFailure)
	assertRotationHistory(t, path, map[string]string{
		"current": "current\n",
		".1":      "backup-one\n",
		".2":      "backup-two\n",
		".3":      "<absent>",
	})
	assertFileContent(t, path+".rotate-prune", []byte{})
	require.NoError(t, writer.Close())

	reopened, reopenErr := NewRotatingFileWriter(t.Context(), path)
	if reopened != nil {
		t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	}
	assert.Nil(t, reopened)
	require.Error(t, reopenErr)
	assert.Contains(t, reopenErr.Error(), "unfinished rotation state")
}

func TestRotatingFileWriterDetectsReplacementBeforeBoundRename(t *testing.T) {
	// Rationale: a cooperative lock cannot prevent an external replacement, so every rename must recheck the exact
	// inode it intends to move and reject the replacement rather than publish it as retained history.
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "audit.log")
	detached := filepath.Join(directory, "detached-current")
	seedRotationHistory(t, path)
	writer := newTestWriter(t, path)
	writer.maxBytes = 1
	realLstat := writer.deps.lstat
	currentStats := 0
	writer.deps.lstat = func(ctx context.Context, inspectedPath string, stat *unix.Stat_t) error {
		if inspectedPath == path {
			currentStats++
			if currentStats == 2 {
				require.NoError(t, os.Rename(path, detached))
				require.NoError(t, os.WriteFile(path, []byte("replacement\n"), managedFileMode))
			}
		}
		return realLstat(ctx, inspectedPath, stat)
	}

	n, err := writer.WriteContext(t.Context(), []byte("new-record\n"))
	assert.Zero(t, n)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inode changed")
	assertFileContent(t, path, []byte("replacement\n"))
	assertFileContent(t, detached, []byte("current\n"))
	assertFileContent(t, path+".1", []byte("backup-one\n"))
	assertFileContent(t, path+".2", []byte("backup-two\n"))
	assertFileContent(t, path+".3", []byte("backup-three\n"))
}

func TestRotatingFileWriterDoesNotReplaceDestinationInsertedBeforeRename(t *testing.T) {
	// Rationale: destination admission and rename are separate operations, so a newly inserted destination must make
	// the transaction fail closed instead of being overwritten by a replacing rename.
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	seedRotationHistory(t, path)
	writer := newTestWriter(t, path)
	writer.maxBytes = 1
	stagePath := rotationStagePath(path, 0)
	insertedContent := []byte("externally-inserted\n")
	realRename := writer.deps.rename
	inserted := false
	writer.deps.rename = func(ctx context.Context, oldPath, newPath string) error {
		if !inserted && oldPath == path && newPath == stagePath {
			inserted = true
			require.NoError(t, os.WriteFile(stagePath, insertedContent, managedFileMode))
		}
		return realRename(ctx, oldPath, newPath)
	}

	n, err := writer.WriteContext(t.Context(), []byte("new-record\n"))
	assert.Zero(t, n)
	require.Error(t, err)
	assert.ErrorIs(t, err, unix.EEXIST)
	assertFileContent(t, stagePath, insertedContent)
	assertRotationHistory(t, path, originalRotationHistory())

	reopened, reopenErr := NewRotatingFileWriter(t.Context(), path)
	assert.Nil(t, reopened)
	require.Error(t, reopenErr)
	assert.Contains(t, reopenErr.Error(), "unfinished rotation state")
}

func TestRotatingFileWriterCreatesSecureLogAndPersistentLockFiles(t *testing.T) {
	// Rationale: private audit material and the synchronization inode must not inherit a permissive umask or vanish
	// during ordinary shutdown.
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	writer := newTestWriter(t, path)
	_, err := writer.WriteContext(t.Context(), []byte("record\n"))
	require.NoError(t, err)

	assertFileMode(t, path, managedFileMode)
	assertFileMode(t, path+".lock", managedFileMode)
	assertCloseOnExec(t, writer.fileFD)
	assertCloseOnExec(t, writer.lockFD)

	require.NoError(t, writer.Close())
	_, err = os.Stat(path + ".lock")
	assert.NoError(t, err, "Close must not unlink the stable lock file")
}

func TestNewRotatingFileWriterRejectsUnsafeManagedFiles(t *testing.T) {
	// Rationale: following or admitting an ambiguous managed path can redirect or disclose privileged audit output.
	t.Parallel()

	tests := map[string]struct {
		prepare func(t *testing.T, path string)
		want    string
	}{
		"log symlink": {
			prepare: func(t *testing.T, path string) {
				target := filepath.Join(filepath.Dir(path), "target")
				require.NoError(t, os.WriteFile(target, []byte("target"), managedFileMode))
				require.NoError(t, os.Symlink(target, path))
			},
			want: "open",
		},
		"lock symlink": {
			prepare: func(t *testing.T, path string) {
				target := filepath.Join(filepath.Dir(path), "target-lock")
				require.NoError(t, os.WriteFile(target, nil, managedFileMode))
				require.NoError(t, os.Symlink(target, path+".lock"))
			},
			want: "open",
		},
		"log directory": {
			prepare: func(t *testing.T, path string) {
				require.NoError(t, os.Mkdir(path, managedFileMode))
			},
			want: "open",
		},
		"lock directory": {
			prepare: func(t *testing.T, path string) {
				require.NoError(t, os.Mkdir(path+".lock", managedFileMode))
			},
			want: "open",
		},
		"log hard link": {
			prepare: func(t *testing.T, path string) {
				require.NoError(t, os.WriteFile(path, nil, managedFileMode))
				require.NoError(t, os.Link(path, path+".alias"))
			},
			want: "exactly one link",
		},
		"log broad mode": {
			prepare: func(t *testing.T, path string) {
				require.NoError(t, os.WriteFile(path, nil, 0o644))
				require.NoError(t, os.Chmod(path, 0o644))
			},
			want: "mode is not 0600",
		},
		"nonempty lock": {
			prepare: func(t *testing.T, path string) {
				require.NoError(t, os.WriteFile(path+".lock", []byte("not-a-lock"), managedFileMode))
			},
			want: "lock file is not empty",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.log")
			test.prepare(t, path)
			writer, err := NewRotatingFileWriter(t.Context(), path)
			if writer != nil {
				t.Cleanup(func() { assert.NoError(t, writer.Close()) })
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.want)
		})
	}
}

func TestNewRotatingFileWriterUsesNoFollowAndCloseOnExecForEveryOpen(t *testing.T) {
	// Rationale: flags are part of every open operation's security contract and cannot be inferred from final mode.
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	deps := defaultWriterDependencies()
	realOpen := deps.open
	var mu sync.Mutex
	var observedFlags []int
	deps.open = func(ctx context.Context, path string, flags int, mode uint32) (int, error) {
		mu.Lock()
		observedFlags = append(observedFlags, flags)
		mu.Unlock()
		return realOpen(ctx, path, flags, mode)
	}
	writer, err := newRotatingFileWriter(t.Context(), path, deps)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writer.Close()) })
	_, err = writer.WriteContext(t.Context(), []byte("record\n"))
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, observedFlags)
	for _, flags := range observedFlags {
		assert.NotZero(t, flags&unix.O_NOFOLLOW)
		assert.NotZero(t, flags&unix.O_CLOEXEC)
		assert.NotZero(t, flags&unix.O_NONBLOCK)
	}
}

func TestNewRotatingFileWriterRejectsUnexpectedOwner(t *testing.T) {
	// Rationale: mode and file type alone do not stop a different user from supplying the managed inode.
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	deps := defaultWriterDependencies()
	deps.euid = func() uint32 { return uint32(os.Geteuid()) + 1 }

	writer, err := newRotatingFileWriter(t.Context(), path, deps)
	assert.Nil(t, writer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected owner")
}

func TestRotatingFileWriterPropagatesFilesystemFailures(t *testing.T) {
	// Rationale: an audit writer must expose partial or uncertain filesystem outcomes instead of reporting success.
	t.Parallel()

	t.Run("constructor open failure", func(t *testing.T) {
		injected := errors.New("injected open failure")
		deps := defaultWriterDependencies()
		deps.open = func(context.Context, string, int, uint32) (int, error) { return -1, injected }

		writer, err := newRotatingFileWriter(t.Context(), filepath.Join(t.TempDir(), "audit.log"), deps)
		assert.Nil(t, writer)
		require.Error(t, err)
		assert.ErrorIs(t, err, injected)
	})

	t.Run("constructor stat and cleanup close failures", func(t *testing.T) {
		statFailure := errors.New("injected stat failure")
		closeFailure := errors.New("injected close failure")
		deps := defaultWriterDependencies()
		deps.fstat = func(context.Context, int, *unix.Stat_t) error { return statFailure }
		realClose := deps.close
		deps.close = func(ctx context.Context, fd int) error {
			return errors.Join(realClose(ctx, fd), closeFailure)
		}

		writer, err := newRotatingFileWriter(t.Context(), filepath.Join(t.TempDir(), "audit.log"), deps)
		assert.Nil(t, writer)
		require.Error(t, err)
		assert.ErrorIs(t, err, statFailure)
		assert.ErrorIs(t, err, closeFailure)
	})

	t.Run("write reopen failure", func(t *testing.T) {
		injected := errors.New("injected reopen failure")
		path := filepath.Join(t.TempDir(), "audit.log")
		writer := newTestWriter(t, path)
		realOpen := writer.deps.open
		writer.deps.open = func(ctx context.Context, openedPath string, flags int, mode uint32) (int, error) {
			if openedPath == path {
				return -1, injected
			}
			return realOpen(ctx, openedPath, flags, mode)
		}

		n, err := writer.WriteContext(t.Context(), []byte("record\n"))
		assert.Zero(t, n)
		require.Error(t, err)
		assert.ErrorIs(t, err, injected)
	})

	t.Run("write stat failure", func(t *testing.T) {
		injected := errors.New("injected write stat failure")
		closeFailure := errors.New("injected candidate close failure")
		path := filepath.Join(t.TempDir(), "audit.log")
		writer := newTestWriter(t, path)
		realFstat := writer.deps.fstat
		fstatCalls := 0
		candidateFD := -1
		writer.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
			fstatCalls++
			if fstatCalls == 1 {
				candidateFD = fd
			}
			if fstatCalls == 2 {
				return injected
			}
			return realFstat(ctx, fd, stat)
		}
		realClose := writer.deps.close
		writer.deps.close = func(ctx context.Context, fd int) error {
			closeErr := realClose(ctx, fd)
			if fd == candidateFD {
				return errors.Join(closeErr, closeFailure)
			}
			return closeErr
		}

		n, err := writer.WriteContext(t.Context(), []byte("record\n"))
		assert.Zero(t, n)
		require.Error(t, err)
		assert.ErrorIs(t, err, injected)
		assert.ErrorIs(t, err, closeFailure)
	})

	t.Run("rotation path stat failure", func(t *testing.T) {
		injected := errors.New("injected path stat failure")
		path := filepath.Join(t.TempDir(), "audit.log")
		writer := newTestWriter(t, path)
		writer.maxBytes = 1
		writer.deps.lstat = func(context.Context, string, *unix.Stat_t) error { return injected }

		n, err := writer.WriteContext(t.Context(), []byte("record\n"))
		assert.Zero(t, n)
		require.Error(t, err)
		assert.ErrorIs(t, err, injected)
	})

	t.Run("rotation close failure", func(t *testing.T) {
		injected := errors.New("injected rotation close failure")
		path := filepath.Join(t.TempDir(), "audit.log")
		writer := newTestWriter(t, path)
		_, err := writer.WriteContext(t.Context(), []byte("seed\n"))
		require.NoError(t, err)
		writer.maxBytes = 1
		realClose := writer.deps.close
		initialFileFD := writer.fileFD
		failed := false
		writer.deps.close = func(ctx context.Context, fd int) error {
			if fd == initialFileFD && !failed {
				failed = true
				return errors.Join(realClose(ctx, fd), injected)
			}
			return realClose(ctx, fd)
		}

		n, err := writer.WriteContext(t.Context(), []byte("record\n"))
		assert.Zero(t, n)
		require.Error(t, err)
		assert.ErrorIs(t, err, injected)
	})

	t.Run("rotation rename failure", func(t *testing.T) {
		injected := errors.New("injected rename failure")
		path := filepath.Join(t.TempDir(), "audit.log")
		writer := newTestWriter(t, path)
		_, err := writer.WriteContext(t.Context(), []byte("seed\n"))
		require.NoError(t, err)
		writer.maxBytes = 1
		writer.deps.rename = func(context.Context, string, string) error { return injected }

		n, err := writer.WriteContext(t.Context(), []byte("record\n"))
		assert.Zero(t, n)
		require.Error(t, err)
		assert.ErrorIs(t, err, injected)
	})

	t.Run("rotation reopen failure", func(t *testing.T) {
		injected := errors.New("injected rotation reopen failure")
		path := filepath.Join(t.TempDir(), "audit.log")
		writer := newTestWriter(t, path)
		_, err := writer.WriteContext(t.Context(), []byte("seed\n"))
		require.NoError(t, err)
		writer.maxBytes = 1
		realOpen := writer.deps.open
		createAttempts := 0
		writer.deps.open = func(ctx context.Context, openedPath string, flags int, mode uint32) (int, error) {
			if openedPath == path && flags&unix.O_EXCL != 0 {
				createAttempts++
				if createAttempts == 2 {
					return -1, injected
				}
			}
			return realOpen(ctx, openedPath, flags, mode)
		}

		n, err := writer.WriteContext(t.Context(), []byte("record\n"))
		assert.Zero(t, n)
		require.Error(t, err)
		assert.ErrorIs(t, err, injected)
	})

	t.Run("write failure", func(t *testing.T) {
		injected := errors.New("injected write failure")
		path := filepath.Join(t.TempDir(), "audit.log")
		writer := newTestWriter(t, path)
		writer.deps.write = func(context.Context, int, []byte) (int, error) { return 0, injected }

		n, err := writer.WriteContext(t.Context(), []byte("record\n"))
		assert.Zero(t, n)
		require.Error(t, err)
		assert.ErrorIs(t, err, injected)
	})

	t.Run("short write", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "audit.log")
		writer := newTestWriter(t, path)
		writer.deps.write = func(context.Context, int, []byte) (int, error) { return 2, nil }

		n, err := writer.WriteContext(t.Context(), []byte("record\n"))
		assert.Equal(t, 2, n)
		require.Error(t, err)
		assert.ErrorIs(t, err, io.ErrShortWrite)
	})

}

func TestRotatingFileWriterUnlockFailurePoisonsAndDetachesExactDescriptors(t *testing.T) {
	// Rationale: after an uncertain unlock, retaining either descriptor permits later writes under ambiguous locking
	// authority and retrying a close could target a reused descriptor.
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	writer, err := NewRotatingFileWriter(t.Context(), path)
	require.NoError(t, err)
	originalFileFD := writer.fileFD
	originalLockFD := writer.lockFD
	unlockFailure := errors.New("injected unlock failure")
	fileCloseFailure := errors.New("injected file close failure")
	lockCloseFailure := errors.New("injected lock close failure")
	realFlock := writer.deps.flock
	writer.deps.flock = func(ctx context.Context, fd, how int) error {
		if how == unix.LOCK_UN {
			return unlockFailure
		}
		return realFlock(ctx, fd, how)
	}
	realClose := writer.deps.close
	var closedFDs []int
	writer.deps.close = func(ctx context.Context, fd int) error {
		closeErr := realClose(ctx, fd)
		switch fd {
		case originalFileFD:
			closedFDs = append(closedFDs, fd)
			return errors.Join(closeErr, fileCloseFailure)
		case originalLockFD:
			closedFDs = append(closedFDs, fd)
			return errors.Join(closeErr, lockCloseFailure)
		default:
			return closeErr
		}
	}

	written, writeErr := writer.WriteContext(t.Context(), []byte("record\n"))
	assert.Equal(t, len("record\n"), written)
	require.Error(t, writeErr)
	assert.ErrorIs(t, writeErr, unlockFailure)
	assert.ErrorIs(t, writeErr, fileCloseFailure)
	assert.ErrorIs(t, writeErr, lockCloseFailure)
	if diff := cmp.Diff([]int{originalFileFD, originalLockFD}, closedFDs); diff != "" {
		t.Errorf("detached descriptor targets mismatch (-want +got):\n%s", diff)
	}

	written, retryErr := writer.WriteContext(t.Context(), []byte("must-not-appear\n"))
	assert.Zero(t, written)
	require.Error(t, retryErr)
	assert.ErrorIs(t, retryErr, unlockFailure)
	assert.Error(t, writer.Close())
	if diff := cmp.Diff([]int{originalFileFD, originalLockFD}, closedFDs); diff != "" {
		t.Errorf("close retried detached descriptor (-want +got):\n%s", diff)
	}
	_, statErr := os.Stat(path + ".lock")
	assert.NoError(t, statErr)
}

func TestRotatingFileWriterUnlockRetriesEINTR(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	writer := newTestWriter(t, path)
	realFlock := writer.deps.flock
	unlockCalls := 0
	writer.deps.flock = func(ctx context.Context, fd, how int) error {
		if how == unix.LOCK_UN {
			unlockCalls++
			if unlockCalls == 1 {
				return unix.EINTR
			}
		}
		return realFlock(ctx, fd, how)
	}

	_, err := writer.WriteContext(t.Context(), []byte("record\n"))
	require.NoError(t, err)
	assert.Equal(t, 2, unlockCalls)
}

func TestRotatingFileWriterNormalizesInvalidWriteCounts(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		writeResult int
		writeErr    error
		wantErr     error
	}{
		"negative count with error": {
			writeResult: -1,
			writeErr:    errInjectedNegativeWrite,
			wantErr:     errInjectedNegativeWrite,
		},
		"count larger than input": {
			writeResult: len("record\n") + 1,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.log")
			writer := newTestWriter(t, path)
			writer.deps.write = func(context.Context, int, []byte) (int, error) {
				return test.writeResult, test.writeErr
			}

			n, err := writer.WriteContext(t.Context(), []byte("record\n"))
			assert.Zero(t, n)
			require.Error(t, err)
			if test.wantErr != nil {
				assert.ErrorIs(t, err, test.wantErr)
			}
		})
	}
}

func TestRotatingFileWriterCloseIsCachedAndRejectsLaterWrites(t *testing.T) {
	// Rationale: retrying a failed close can target a reused descriptor, while accepting writes would hide record loss.
	t.Parallel()

	injected := errors.New("injected close failure")
	path := filepath.Join(t.TempDir(), "audit.log")
	writer, err := NewRotatingFileWriter(t.Context(), path)
	require.NoError(t, err)
	realClose := writer.deps.close
	originalFileFD := writer.fileFD
	originalLockFD := writer.lockFD
	var closedFDs []int
	writer.deps.close = func(ctx context.Context, fd int) error {
		closedFDs = append(closedFDs, fd)
		return errors.Join(realClose(ctx, fd), injected)
	}

	firstErr := writer.Close()
	require.Error(t, firstErr)
	assert.ErrorIs(t, firstErr, injected)
	secondErr := writer.Close()
	assert.Same(t, firstErr, secondErr)
	if diff := cmp.Diff([]int{originalFileFD, originalLockFD}, closedFDs); diff != "" {
		t.Errorf("closed descriptor targets mismatch (-want +got):\n%s", diff)
	}

	written, writeErr := writer.WriteContext(t.Context(), []byte("after-close\n"))
	assert.Zero(t, written)
	require.Error(t, writeErr)
	assert.ErrorIs(t, writeErr, os.ErrClosed)
	_, statErr := os.Stat(path + ".lock")
	assert.NoError(t, statErr, "Close must leave the stable lock path in place")
}

var errInjectedNegativeWrite = errors.New("injected negative write failure")

var errTestWaitTimeout = errors.New("timed out waiting for test release")

func TestRotatingFileWriterRotatedFilesRemainPrivate(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "audit.log")
	writer := newTestWriter(t, path)
	writer.maxBytes = 4
	writer.backupCount = 2
	_, err := writer.WriteContext(t.Context(), []byte("one\n"))
	require.NoError(t, err)
	_, err = writer.WriteContext(t.Context(), []byte("two\n"))
	require.NoError(t, err)

	assertFileMode(t, path, managedFileMode)
	assertFileMode(t, path+".1", managedFileMode)
	assertFileMode(t, path+".lock", managedFileMode)
}

func newTestWriter(t *testing.T, path string) *RotatingFileWriter {
	t.Helper()

	writer, err := NewRotatingFileWriter(t.Context(), path)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return writer
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("file content mismatch (-want +got):\n%s", diff)
	}
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, want, info.Mode().Perm())
}

func assertCloseOnExec(t *testing.T, fd int) {
	t.Helper()

	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	require.NoError(t, err)
	assert.NotZero(t, flags&unix.FD_CLOEXEC)
}

func seedRotationHistory(t *testing.T, path string) {
	t.Helper()

	for suffix, content := range originalRotationHistory() {
		filePath := path
		if suffix != "current" {
			filePath += suffix
		}
		require.NoError(t, os.WriteFile(filePath, []byte(content), managedFileMode))
		require.NoError(t, os.Chmod(filePath, managedFileMode))
	}
}

func originalRotationHistory() map[string]string {
	return map[string]string{
		"current": "current\n",
		".1":      "backup-one\n",
		".2":      "backup-two\n",
		".3":      "backup-three\n",
	}
}

func assertRotationHistory(t *testing.T, path string, want map[string]string) {
	t.Helper()

	got := make(map[string]string, len(want))
	for _, suffix := range []string{"current", ".1", ".2", ".3"} {
		filePath := path
		if suffix != "current" {
			filePath += suffix
		}
		content, err := os.ReadFile(filePath)
		if errors.Is(err, os.ErrNotExist) {
			got[suffix] = "<absent>"
			continue
		}
		require.NoError(t, err)
		got[suffix] = string(content)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("rotation history mismatch (-want +got):\n%s", diff)
	}
}

type writeResult struct {
	n   int
	err error
}

type writerResult struct {
	writer *RotatingFileWriter
	err    error
}

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()

	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for signal")
	}
}

func awaitResult(t *testing.T, result <-chan writeResult) writeResult {
	t.Helper()

	select {
	case value := <-result:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for write result")
		return writeResult{}
	}
}

func awaitWriterResult(t *testing.T, result <-chan writerResult) writerResult {
	t.Helper()

	select {
	case value := <-result:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for writer result")
		return writerResult{}
	}
}

func waitForRelease(release <-chan struct{}) error {
	select {
	case <-release:
		return nil
	case <-time.After(2 * time.Second):
		return errTestWaitTimeout
	}
}
