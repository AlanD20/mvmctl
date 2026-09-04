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
	realWrite := first.deps.write
	first.deps.write = func(ctx context.Context, fd int, p []byte) (int, error) {
		close(writeStarted)
		<-releaseWrite
		return realWrite(ctx, fd, p)
	}
	type writeResult struct {
		n   int
		err error
	}
	firstResult := make(chan writeResult, 1)
	go func() {
		n, err := first.WriteContext(context.Background(), []byte("held-record\n"))
		firstResult <- writeResult{n: n, err: err}
	}()
	<-writeStarted

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	written, err := second.WriteContext(ctx, []byte("must-not-appear\n"))
	require.Error(t, err)
	assert.Zero(t, written)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	close(releaseWrite)
	result := <-firstResult
	require.NoError(t, result.err)
	assert.Equal(t, len("held-record\n"), result.n)
	assertFileContent(t, path, []byte("held-record\n"))
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
			writer, err := NewRotatingFileWriter(path)
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
		path := filepath.Join(t.TempDir(), "audit.log")
		writer := newTestWriter(t, path)
		writer.deps.fstat = func(context.Context, int, *unix.Stat_t) error { return injected }

		n, err := writer.WriteContext(t.Context(), []byte("record\n"))
		assert.Zero(t, n)
		require.Error(t, err)
		assert.ErrorIs(t, err, injected)
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

	t.Run("unlock failure", func(t *testing.T) {
		injected := errors.New("injected unlock failure")
		path := filepath.Join(t.TempDir(), "audit.log")
		writer := newTestWriter(t, path)
		realFlock := writer.deps.flock
		writer.deps.flock = func(ctx context.Context, fd, how int) error {
			if how == unix.LOCK_UN {
				return injected
			}
			return realFlock(ctx, fd, how)
		}

		n, err := writer.WriteContext(t.Context(), []byte("record\n"))
		assert.Equal(t, len("record\n"), n)
		require.Error(t, err)
		assert.ErrorIs(t, err, injected)
	})
}

func TestRotatingFileWriterCloseIsCachedAndRejectsLaterWrites(t *testing.T) {
	// Rationale: retrying a failed close can target a reused descriptor, while accepting writes would hide record loss.
	t.Parallel()

	injected := errors.New("injected close failure")
	path := filepath.Join(t.TempDir(), "audit.log")
	writer, err := NewRotatingFileWriter(path)
	require.NoError(t, err)
	realClose := writer.deps.close
	closeCalls := 0
	writer.deps.close = func(ctx context.Context, fd int) error {
		closeCalls++
		return errors.Join(realClose(ctx, fd), injected)
	}

	firstErr := writer.Close()
	require.Error(t, firstErr)
	assert.ErrorIs(t, firstErr, injected)
	secondErr := writer.Close()
	assert.Same(t, firstErr, secondErr)
	assert.Equal(t, 2, closeCalls, "each retained descriptor must be closed only once")

	written, writeErr := writer.WriteContext(t.Context(), []byte("after-close\n"))
	assert.Zero(t, written)
	require.Error(t, writeErr)
	assert.ErrorIs(t, writeErr, os.ErrClosed)
	_, statErr := os.Stat(path + ".lock")
	assert.NoError(t, statErr, "Close must leave the stable lock path in place")
}

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

	writer, err := NewRotatingFileWriter(path)
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
