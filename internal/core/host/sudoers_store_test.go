package host

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/lib/system"
	"mvmctl/pkg/errs"
)

// CONTRACT: The sole passwordless grant reaches only the early privileged dispatcher.
const expectedMarkerOnlySudoers = "# Managed by mvmctl — do not edit manually.\n" +
	"# To remove: sudo /usr/local/bin/mvm host reset --force\n" +
	"%mvm ALL=(root) NOPASSWD: /usr/local/bin/mvm __mvm_privileged_v1 *\n"

// A valid but noncanonical prior policy lets publication and failure tests reach the effect boundary.
const previousMarkerOnlySudoers = "# Previous managed policy\n" +
	"%mvm ALL=(root) NOPASSWD: /usr/local/bin/mvm __mvm_privileged_v1 *\n"

type sudoersFixture struct {
	root   string
	dir    string
	target string
	deps   sudoersStoreDeps
	policy sudoersStorePolicy
}

func newSudoersFixture(t *testing.T) *sudoersFixture {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "etc", "sudoers.d")
	require.NoError(t, os.MkdirAll(dir, 0755))
	f := &sudoersFixture{root: root, dir: dir, target: filepath.Join(dir, "mvm"),
		deps:   realSudoersStoreDeps(),
		policy: sudoersStorePolicy{rootPath: root, uid: uint32(os.Getuid()), gid: uint32(os.Getgid())},
	}
	f.deps.effectiveUID = func() int { return 0 }
	f.deps.run = func(context.Context, []string, system.RunCmdOpts) (*system.RunResult, error) {
		return &system.RunResult{ExitCode: 0}, nil
	}
	opened := make(map[int]bool)
	open, openAt2, closeFD := f.deps.open, f.deps.openAt2, f.deps.close
	f.deps.open = func(ctx context.Context, path string, flags int, mode uint32) (int, error) {
		fd, err := open(ctx, path, flags, mode)
		if err == nil {
			opened[fd] = true
		}
		return fd, err
	}
	f.deps.openAt2 = func(ctx context.Context, dir int, name string, how *unix.OpenHow) (int, error) {
		fd, err := openAt2(ctx, dir, name, how)
		if err == nil {
			opened[fd] = true
		}
		return fd, err
	}
	f.deps.close = func(ctx context.Context, fd int) error {
		assert.True(t, opened[fd], "descriptor must close exactly once")
		delete(opened, fd)
		return closeFD(ctx, fd)
	}
	t.Cleanup(func() {
		assert.Empty(t, opened, "publication must release all descriptors")
		for fd := range opened {
			require.NoError(t, unix.Close(fd))
		}
	})
	return f
}

func (f *sudoersFixture) publish(ctx context.Context) (bool, error) {
	return configureSudoers(ctx, sudoersConfigDeps{
		verifySystemBinary: func() error { return nil }, store: f.deps, policy: f.policy,
	})
}

func (f *sudoersFixture) seed(t *testing.T, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(f.target, []byte(content), 0440))
}

func assertSudoersFiles(t *testing.T, f *sudoersFixture, content string) {
	t.Helper()
	data, err := os.ReadFile(f.target)
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
	entries, err := os.ReadDir(f.dir)
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if diff := cmp.Diff([]string{"mvm"}, names); diff != "" {
		t.Errorf("policy files (-want +got):\n%s", diff)
	}
}

// Snapshot through independent syscalls: rejection must preserve the admitted
// pathname object, including an unsafe file or symlink, without repairing it.
func snapshotSudoersObject(t *testing.T, target string) (unix.Stat_t, []byte) {
	t.Helper()
	var stat unix.Stat_t
	require.NoError(t, unix.Lstat(target, &stat))
	var content []byte
	if kind := stat.Mode & unix.S_IFMT; kind == unix.S_IFREG || kind == unix.S_IFLNK {
		var err error
		content, err = os.ReadFile(target)
		require.NoError(t, err)
	}
	return stat, content
}

func assertSudoersObjectUnchanged(t *testing.T, target string, before unix.Stat_t, content []byte) {
	t.Helper()
	after, afterContent := snapshotSudoersObject(t, target)
	assert.Equal(t, before.Dev, after.Dev)
	assert.Equal(t, before.Ino, after.Ino)
	assert.Equal(t, before.Mode, after.Mode)
	assert.Equal(t, before.Uid, after.Uid)
	assert.Equal(t, before.Gid, after.Gid)
	assert.Equal(t, before.Nlink, after.Nlink)
	assert.Equal(t, before.Size, after.Size)
	if diff := cmp.Diff(content, afterContent); diff != "" {
		t.Errorf("preserved policy content (-want +got):\n%s", diff)
	}
}

func TestSudoersPublisherRejectsUnsafeExistingObjectsEvenWithMatchingContent(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *sudoersFixture)
	}{
		{"writable parent", func(t *testing.T, f *sudoersFixture) { require.NoError(t, os.Chmod(f.dir, 0777)) }},
		{"writable file", func(t *testing.T, f *sudoersFixture) { require.NoError(t, os.Chmod(f.target, 0660)) }},
		{"symlink", func(t *testing.T, f *sudoersFixture) {
			require.NoError(t, os.Rename(f.target, f.target+".original"))
			require.NoError(t, os.Symlink("mvm.original", f.target))
		}},
		{
			"hardlink",
			func(t *testing.T, f *sudoersFixture) { require.NoError(t, os.Link(f.target, f.target+".other")) },
		},
		{"ancestor owner", func(_ *testing.T, f *sudoersFixture) { f.policy.uid++ }},
		{"oversized", func(t *testing.T, f *sudoersFixture) {
			require.NoError(t, os.Chmod(f.target, 0640))
			require.NoError(t, os.Truncate(f.target, 65537))
			require.NoError(t, os.Chmod(f.target, 0440))
		}},
		{"FIFO", func(t *testing.T, f *sudoersFixture) {
			require.NoError(t, os.Remove(f.target))
			require.NoError(t, unix.Mkfifo(f.target, 0440))
		}},
		{"directory", func(t *testing.T, f *sudoersFixture) {
			require.NoError(t, os.Remove(f.target))
			require.NoError(t, os.Mkdir(f.target, 0440))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSudoersFixture(t)
			f.seed(t, expectedMarkerOnlySudoers)
			tt.mutate(t, f)
			before, content := snapshotSudoersObject(t, f.target)
			f.deps.run = func(context.Context, []string, system.RunCmdOpts) (*system.RunResult, error) {
				t.Fatal("unsafe policy must fail before validation")
				return nil, nil
			}
			changed, err := f.publish(context.Background())
			require.Error(t, err)
			assert.False(t, changed)
			assert.NotNil(t, errs.AsDomainError(err))
			assertSudoersObjectUnchanged(t, f.target, before, content)
		})
	}
}

func TestSudoersPublisherRejectsTargetUIDAndGIDWithoutChangingPolicy(t *testing.T) {
	for _, field := range []string{"uid", "gid"} {
		t.Run(field, func(t *testing.T) {
			f := newSudoersFixture(t)
			f.seed(t, expectedMarkerOnlySudoers)
			before, content := snapshotSudoersObject(t, f.target)
			fstat := f.deps.fstat
			injected := false
			f.deps.fstat = func(ctx context.Context, fd int, stat *unix.Stat_t) error {
				if err := fstat(ctx, fd, stat); err != nil {
					return err
				}
				// Match only the policy inode; every directory retains valid metadata.
				if stat.Dev == before.Dev && stat.Ino == before.Ino {
					injected = true
					if field == "uid" {
						stat.Uid++
					} else {
						stat.Gid++
					}
				}
				return nil
			}
			f.deps.run = func(context.Context, []string, system.RunCmdOpts) (*system.RunResult, error) {
				t.Fatal("unsafe target must fail before visudo validation")
				return nil, nil
			}
			changed, err := f.publish(context.Background())
			require.Error(t, err)
			assert.False(t, changed)
			assert.True(t, injected, "valid ancestors must allow inspection to reach the target inode")
			assertSudoersObjectUnchanged(t, f.target, before, content)
			assertSudoersFiles(t, f, expectedMarkerOnlySudoers)
		})
	}
}

func TestSudoersPublisherPublishesExactPolicyAndIsIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		initial string
	}{{name: "absent"}, {name: "prior marker policy", initial: previousMarkerOnlySudoers}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSudoersFixture(t)
			if tc.initial != "" {
				f.seed(t, tc.initial)
			}
			changed, err := f.publish(context.Background())
			require.NoError(t, err)
			assert.True(t, changed)
			assertSudoersFiles(t, f, expectedMarkerOnlySudoers)
			var stat unix.Stat_t
			require.NoError(t, unix.Stat(f.target, &stat))
			assert.Equal(t, uint32(0440), stat.Mode&07777)
			assert.Equal(t, uint64(1), uint64(stat.Nlink))
			assert.Equal(t, f.policy.uid, stat.Uid)
			assert.Equal(t, f.policy.gid, stat.Gid)
			f.deps.run = func(context.Context, []string, system.RunCmdOpts) (*system.RunResult, error) {
				t.Fatal("unchanged valid policy must not execute validation")
				return nil, nil
			}
			changed, err = f.publish(context.Background())
			require.NoError(t, err)
			assert.False(t, changed)
			var after unix.Stat_t
			require.NoError(t, unix.Stat(f.target, &after))
			assert.Equal(t, stat.Ino, after.Ino)
		})
	}
}

func TestSudoersPublisherUsesClosedValidationEnvironment(t *testing.T) {
	f := newSudoersFixture(t)
	t.Setenv("TMPDIR", "/untrusted-temp")
	t.Setenv("PATH", "/untrusted-bin")
	t.Setenv("MVM_CACHE_DIR", "/untrusted-cache")
	f.deps.run = func(ctx context.Context, args []string, opts system.RunCmdOpts) (*system.RunResult, error) {
		if diff := cmp.Diff([]string{"/usr/sbin/visudo", "-c", "-f", "-"}, args); diff != "" {
			t.Errorf("validation argv (-want +got):\n%s", diff)
		}
		assert.Equal(t, expectedMarkerOnlySudoers, opts.Input)
		assert.True(t, opts.ReplaceEnv)
		assert.False(t, opts.Privileged)
		if diff := cmp.Diff(
			map[string]string{"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "HOME": "/root", "LANG": "C", "LC_ALL": "C"},
			opts.Env,
		); diff != "" {
			t.Errorf("environment (-want +got):\n%s", diff)
		}
		assert.Equal(t, 15*time.Second, opts.Timeout)
		assert.Equal(t, 4096, opts.MaxStdoutBytes)
		assert.Equal(t, 4096, opts.MaxStderrBytes)
		return &system.RunResult{ExitCode: 0}, nil
	}
	changed, err := f.publish(context.Background())
	require.NoError(t, err)
	assert.True(t, changed)
	assertSudoersFiles(t, f, expectedMarkerOnlySudoers)
}

func TestSudoersPublisherPreservesPolicyBeforeCommit(t *testing.T) {
	failure := errors.New("injected sudoers failure")
	tests := []struct {
		name  string
		fault func(*sudoersFixture)
	}{
		{"validation execution", func(f *sudoersFixture) {
			f.deps.run = func(context.Context, []string, system.RunCmdOpts) (*system.RunResult, error) { return nil, failure }
		}},
		{"candidate close", func(f *sudoersFixture) {
			closeFD := f.deps.close
			f.deps.close = func(ctx context.Context, fd int) error {
				flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
				if err != nil {
					return err
				}
				err = closeFD(ctx, fd)
				if flags&unix.O_ACCMODE == unix.O_WRONLY {
					return errors.Join(err, failure)
				}
				return err
			}
		}},
		{"validation rejection", func(f *sudoersFixture) {
			f.deps.run = func(context.Context, []string, system.RunCmdOpts) (*system.RunResult, error) {
				return &system.RunResult{ExitCode: 1}, nil
			}
		}},
		{"write", func(f *sudoersFixture) {
			f.deps.write = func(context.Context, int, []byte) (int, error) { return 1, failure }
		}},
		{"zero write", func(f *sudoersFixture) {
			f.deps.write = func(context.Context, int, []byte) (int, error) { return 0, nil }
		}},
		{
			"chmod",
			func(f *sudoersFixture) { f.deps.fchmod = func(context.Context, int, uint32) error { return failure } },
		},
		{
			"chown",
			func(f *sudoersFixture) { f.deps.fchown = func(context.Context, int, int, int) error { return failure } },
		},
		{"file sync", func(f *sudoersFixture) { f.deps.fsync = func(context.Context, int) error { return failure } }},
		{"rename", func(f *sudoersFixture) {
			f.deps.renameAt = func(context.Context, int, string, int, string) error { return failure }
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSudoersFixture(t)
			f.seed(t, previousMarkerOnlySudoers)
			tt.fault(f)
			changed, err := f.publish(context.Background())
			require.Error(t, err)
			assert.False(t, changed)
			assertSudoersFiles(t, f, previousMarkerOnlySudoers)
		})
	}
}

// Rationale: Existing raw-tool grants cannot be silently replaced under a possibly passwordless administrator session.
func TestSudoersPublisherRejectsLegacyPolicyBeforeVisudo(t *testing.T) {
	f := newSudoersFixture(t)
	const legacyPolicy = "%mvm ALL=(root) NOPASSWD: /usr/sbin/ip, /usr/local/bin/mvm *\n"
	f.seed(t, legacyPolicy)
	before, content := snapshotSudoersObject(t, f.target)
	f.deps.run = func(context.Context, []string, system.RunCmdOpts) (*system.RunResult, error) {
		t.Fatal("legacy policy must fail before visudo validation")
		return nil, nil
	}
	changed, err := f.publish(context.Background())
	require.Error(t, err)
	assert.False(t, changed)
	assertSudoersObjectUnchanged(t, f.target, before, content)
}

func TestSudoersPublisherHandlesShortWrites(t *testing.T) {
	f := newSudoersFixture(t)
	write := f.deps.write
	f.deps.write = func(ctx context.Context, fd int, content []byte) (int, error) {
		return write(ctx, fd, content[:min(7, len(content))])
	}
	changed, err := f.publish(context.Background())
	require.NoError(t, err)
	assert.True(t, changed)
	assertSudoersFiles(t, f, expectedMarkerOnlySudoers)
}

func TestSudoersPublisherDetectsTargetReplacementBeforeCommit(t *testing.T) {
	for _, found := range []bool{false, true} {
		t.Run(fmt.Sprint(found), func(t *testing.T) {
			f := newSudoersFixture(t)
			if found {
				f.seed(t, previousMarkerOnlySudoers)
			}
			f.deps.run = func(context.Context, []string, system.RunCmdOpts) (*system.RunResult, error) {
				replacement := filepath.Join(f.dir, ".replacement")
				require.NoError(t, os.WriteFile(replacement, []byte("# another administrator's policy\n"), 0440))
				require.NoError(t, os.Rename(replacement, f.target))
				return &system.RunResult{ExitCode: 0}, nil
			}
			changed, err := f.publish(context.Background())
			require.Error(t, err)
			assert.False(t, changed)
			assertSudoersFiles(t, f, "# another administrator's policy\n")
		})
	}
}

func TestSudoersPublisherDetectsReadMutation(t *testing.T) {
	f := newSudoersFixture(t)
	f.seed(t, expectedMarkerOnlySudoers)
	read := f.deps.read
	mutated := false
	f.deps.read = func(ctx context.Context, fd int, buffer []byte) (int, error) {
		n, err := read(ctx, fd, buffer)
		if !mutated {
			mutated = true
			require.NoError(t, os.Chmod(f.target, 0660))
		}
		return n, err
	}
	changed, err := f.publish(context.Background())
	require.Error(t, err)
	assert.False(t, changed)
	assertSudoersFiles(t, f, expectedMarkerOnlySudoers)
}

func TestSudoersPublisherRejectsFailedPolicyRead(t *testing.T) {
	f := newSudoersFixture(t)
	f.seed(t, previousMarkerOnlySudoers)
	failure := errors.New("read failure")
	f.deps.read = func(context.Context, int, []byte) (int, error) { return 0, failure }
	changed, err := f.publish(context.Background())
	require.ErrorIs(t, err, failure)
	assert.False(t, changed)
	assertSudoersFiles(t, f, previousMarkerOnlySudoers)
}

func TestSudoersPublisherDetectsReplacementOfMatchingPolicy(t *testing.T) {
	f := newSudoersFixture(t)
	f.seed(t, expectedMarkerOnlySudoers)
	read := f.deps.read
	replaced := false
	f.deps.read = func(ctx context.Context, fd int, buffer []byte) (int, error) {
		n, err := read(ctx, fd, buffer)
		if !replaced {
			replaced = true
			replacement := filepath.Join(f.dir, ".replacement")
			require.NoError(t, os.WriteFile(replacement, []byte("# replaced during inspection\n"), 0440))
			require.NoError(t, os.Rename(replacement, f.target))
		}
		return n, err
	}
	changed, err := f.publish(context.Background())
	require.Error(t, err)
	assert.False(t, changed)
	assertSudoersFiles(t, f, "# replaced during inspection\n")
}

func TestSudoersPublisherNeverDeletesReplacedCandidateDuringCleanup(t *testing.T) {
	f := newSudoersFixture(t)
	f.seed(t, previousMarkerOnlySudoers)
	failure := errors.New("write failure after replacement")
	candidate := ""
	f.deps.write = func(context.Context, int, []byte) (int, error) {
		entries, err := os.ReadDir(f.dir)
		require.NoError(t, err)
		for _, entry := range entries {
			if entry.Name() != "mvm" {
				candidate = filepath.Join(f.dir, entry.Name())
			}
		}
		require.NotEmpty(t, candidate)
		require.NoError(t, os.Remove(candidate))
		require.NoError(t, os.WriteFile(candidate, []byte("replacement belongs to another writer"), 0600))
		return 0, failure
	}
	changed, err := f.publish(context.Background())
	require.ErrorIs(t, err, failure)
	assert.False(t, changed)
	de := errs.AsDomainError(err)
	require.NotNil(t, de)
	assert.Equal(t, true, de.Details["cleanup_failed"])
	data, err := os.ReadFile(candidate)
	require.NoError(t, err)
	assert.Equal(t, "replacement belongs to another writer", string(data))
	data, err = os.ReadFile(f.target)
	require.NoError(t, err)
	assert.Equal(t, previousMarkerOnlySudoers, string(data))
}

func TestSudoersPublisherReportsCloseFailureAfterCommit(t *testing.T) {
	f := newSudoersFixture(t)
	closeFD := f.deps.close
	failure := errors.New("directory close failure")
	f.deps.close = func(ctx context.Context, fd int) error {
		var stat unix.Stat_t
		require.NoError(t, unix.Fstat(fd, &stat))
		err := closeFD(ctx, fd)
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			return errors.Join(err, failure)
		}
		return err
	}
	changed, err := f.publish(context.Background())
	require.ErrorIs(t, err, failure)
	assert.True(t, changed)
	de := errs.AsDomainError(err)
	require.NotNil(t, de)
	assert.Equal(t, true, de.Details["sudoers_replaced"])
	assert.Equal(t, true, de.Details["cleanup_failed"])
	assert.NotContains(t, de.Details, "durability_uncertain")
	assertSudoersFiles(t, f, expectedMarkerOnlySudoers)
}

func TestSudoersPublisherPreservesPrimaryFailureWhenCleanupFails(t *testing.T) {
	f := newSudoersFixture(t)
	f.seed(t, previousMarkerOnlySudoers)
	primary, cleanup := errors.New("write failure"), errors.New("unlink failure")
	f.deps.write = func(context.Context, int, []byte) (int, error) { return 0, primary }
	f.deps.unlinkAt = func(context.Context, int, string, int) error { return cleanup }
	changed, err := f.publish(context.Background())
	require.ErrorIs(t, err, primary)
	require.ErrorIs(t, err, cleanup)
	assert.False(t, changed)
	de := errs.AsDomainError(err)
	require.NotNil(t, de)
	assert.Equal(t, true, de.Details["cleanup_failed"])
	data, err := os.ReadFile(f.target)
	require.NoError(t, err)
	assert.Equal(t, previousMarkerOnlySudoers, string(data))
	entries, err := os.ReadDir(f.dir)
	require.NoError(t, err)
	assert.Len(t, entries, 2, "failed cleanup retains the harmless hidden candidate for diagnosis")
}

func TestSudoersPublisherRejectsCancellationBeforeEffects(t *testing.T) {
	f := newSudoersFixture(t)
	f.seed(t, previousMarkerOnlySudoers)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	changed, err := f.publish(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, changed)
	assertSudoersFiles(t, f, previousMarkerOnlySudoers)
}

func TestSudoersPublisherReportsPostCommitDurabilityFailure(t *testing.T) {
	f := newSudoersFixture(t)
	f.seed(t, previousMarkerOnlySudoers)
	failure := errors.New("directory sync failure")
	original := f.deps.fsync
	f.deps.fsync = func(ctx context.Context, fd int) error {
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			return failure
		}
		return original(ctx, fd)
	}
	changed, err := f.publish(context.Background())
	require.ErrorIs(t, err, failure)
	assert.True(t, changed)
	de := errs.AsDomainError(err)
	require.NotNil(t, de)
	assert.Equal(t, true, de.Details["sudoers_replaced"])
	assert.Equal(t, true, de.Details["durability_uncertain"])
	assertSudoersFiles(t, f, expectedMarkerOnlySudoers)
}

func TestSudoersPublisherCancellationCleansWithBoundedIndependentContext(t *testing.T) {
	f := newSudoersFixture(t)
	f.seed(t, previousMarkerOnlySudoers)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	originalWrite := f.deps.write
	f.deps.write = func(ctx context.Context, fd int, bytes []byte) (int, error) {
		n, err := originalWrite(ctx, fd, bytes)
		cancel()
		return n, err
	}
	originalUnlink := f.deps.unlinkAt
	f.deps.unlinkAt = func(ctx context.Context, fd int, name string, flags int) error {
		assert.NoError(t, ctx.Err())
		deadline, ok := ctx.Deadline()
		assert.True(t, ok)
		assert.WithinDuration(t, time.Now().Add(5*time.Second), deadline, time.Second)
		return originalUnlink(ctx, fd, name, flags)
	}
	changed, err := f.publish(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, changed)
	assertSudoersFiles(t, f, previousMarkerOnlySudoers)
}
