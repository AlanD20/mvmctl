package infra

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestResolveDefaultUserDirFreshSudoHomeSupportsUnprivilegedFollowUp(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	uid := uint32(os.Geteuid())
	gid := uint32(os.Getegid())
	require.NotZero(t, uid, "the ownership regression requires an unprivileged test process")

	tests := map[string]struct {
		kind     defaultUserDirKind
		baseName string
	}{
		"cache":  {kind: defaultUserCacheDir, baseName: ".cache"},
		"config": {kind: defaultUserConfigDir, baseName: ".config"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			deps := testDefaultUserDirDeps(home, uid, gid)
			realFchown := deps.fchown
			fchowned := make([]string, 0, 2)
			deps.fchown = func(ctx context.Context, fd int, ownerUID, ownerGID int) error {
				fchowned = append(fchowned, descriptorPath(t, fd))
				return realFchown(ctx, fd, ownerUID, ownerGID)
			}

			path, err := resolveDefaultUserDir(context.Background(), tc.kind, deps)
			require.NoError(t, err)
			assert.Equal(t, filepath.Join(home, tc.baseName, ProjectName), path)
			assert.Equal(t, []string{filepath.Join(home, tc.baseName), path}, fchowned)
			assertDirectoryOwner(t, filepath.Join(home, tc.baseName), uid, gid)
			assertDirectoryOwner(t, path, uid, gid)

			// This write runs as the original unprivileged test user, reproducing
			// the first command after the sudo host-init process exits.
			require.NoError(t, os.WriteFile(filepath.Join(path, "follow-up"), []byte("ok"), 0600))
		})
	}
}

func TestResolveDefaultUserDirRejectsWritableExistingDirectory(t *testing.T) {
	t.Parallel()

	uid := uint32(os.Geteuid())
	gid := uint32(os.Getegid())
	require.NotZero(t, uid)
	tests := map[string]struct {
		prepare func(t *testing.T, home string)
	}{
		"home": {
			prepare: func(t *testing.T, home string) {
				require.NoError(t, os.Chmod(home, 0777))
			},
		},
		"base": {
			prepare: func(t *testing.T, home string) {
				base := filepath.Join(home, ".cache")
				require.NoError(t, os.Mkdir(base, 0700))
				require.NoError(t, os.Chmod(base, 0777))
			},
		},
		"project": {
			prepare: func(t *testing.T, home string) {
				project := filepath.Join(home, ".cache", ProjectName)
				require.NoError(t, os.MkdirAll(project, 0700))
				require.NoError(t, os.Chmod(project, 0777))
			},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			tc.prepare(t, home)

			_, err := resolveDefaultUserDir(
				context.Background(),
				defaultUserCacheDir,
				testDefaultUserDirDeps(home, uid, gid),
			)
			require.Error(t, err)
			assert.ErrorContains(t, err, "unsafe ownership or access mode")
		})
	}
}

func TestResolveDefaultUserDirAllowsReadOnlyExistingDirectoryAccess(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	uid := uint32(os.Geteuid())
	gid := uint32(os.Getegid())
	require.NotZero(t, uid)
	project := filepath.Join(home, ".cache", ProjectName)
	require.NoError(t, os.MkdirAll(project, 0700))
	for _, path := range []string{home, filepath.Join(home, ".cache"), project} {
		require.NoError(t, os.Chmod(path, 0755))
	}
	deps := testDefaultUserDirDeps(home, uid, gid)
	deps.fchown = func(context.Context, int, int, int) error {
		t.Fatal("existing safe directories must not be chowned")
		return nil
	}

	path, err := resolveDefaultUserDir(context.Background(), defaultUserCacheDir, deps)
	require.NoError(t, err)
	assert.Equal(t, project, path)
}

func TestResolveDefaultUserDirRejectsUnsafeEEXISTRaceWithoutChown(t *testing.T) {
	t.Parallel()

	uid := uint32(os.Geteuid())
	gid := uint32(os.Getegid())
	require.NotZero(t, uid)
	tests := map[string]func(t *testing.T, path string){
		"writable_directory": func(t *testing.T, path string) {
			require.NoError(t, os.Mkdir(path, 0700))
			require.NoError(t, os.Chmod(path, 0777))
		},
		"symlink": func(t *testing.T, path string) {
			require.NoError(t, os.Symlink(t.TempDir(), path))
		},
	}
	for name, substitute := range tests {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			deps := testDefaultUserDirDeps(home, uid, gid)
			realOpenAt := deps.openAt
			baseOpenCount := 0
			deps.openAt = func(ctx context.Context, parentFD int, entry string, flags int, mode uint32) (int, error) {
				if entry == ".cache" {
					baseOpenCount++
					if baseOpenCount == 1 {
						return -1, unix.ENOENT
					}
				}
				return realOpenAt(ctx, parentFD, entry, flags, mode)
			}
			deps.mkdirAt = func(context.Context, int, string, uint32) error {
				substitute(t, filepath.Join(home, ".cache"))
				return unix.EEXIST
			}
			deps.fchown = func(context.Context, int, int, int) error {
				t.Fatal("an EEXIST entry must be verified before ownership changes")
				return nil
			}

			_, err := resolveDefaultUserDir(context.Background(), defaultUserCacheDir, deps)
			require.Error(t, err)
			require.NotNil(t, errs.AsDomainError(err))
			assert.Equal(t, 2, baseOpenCount)
		})
	}
}

func TestResolveDefaultUserDirStopsAfterIncompleteBaseOwnership(t *testing.T) {
	t.Parallel()

	uid := uint32(os.Geteuid())
	gid := uint32(os.Getegid())
	require.NotZero(t, uid)
	tests := map[string]struct {
		configure func(deps *defaultUserDirDeps, failure error)
		message   string
	}{
		"fchmod": {
			configure: func(deps *defaultUserDirDeps, failure error) {
				deps.fchmod = func(context.Context, int, uint32) error { return failure }
			},
			message: "set invoking user directory mode .cache",
		},
		"fchown": {
			configure: func(deps *defaultUserDirDeps, failure error) {
				deps.fchown = func(context.Context, int, int, int) error { return failure }
			},
			message: "set invoking user directory owner .cache",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			deps := testDefaultUserDirDeps(home, uid, gid)
			realMkdirAt := deps.mkdirAt
			created := make([]string, 0, 1)
			deps.mkdirAt = func(ctx context.Context, parentFD int, entry string, mode uint32) error {
				created = append(created, entry)
				return realMkdirAt(ctx, parentFD, entry, mode)
			}
			failure := errors.New(name + " failed")
			tc.configure(&deps, failure)

			_, err := resolveDefaultUserDir(context.Background(), defaultUserCacheDir, deps)
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeConfigError, domainErr.Code)
			assert.ErrorContains(t, err, tc.message)
			assert.ErrorIs(t, err, failure)
			assert.Equal(t, []string{".cache"}, created)
			_, projectErr := os.Lstat(filepath.Join(home, ".cache", ProjectName))
			assert.ErrorIs(t, projectErr, os.ErrNotExist)
		})
	}
}

func TestResolveDefaultUserDirSurfacesSyncAndCloseFailures(t *testing.T) {
	t.Parallel()

	uid := uint32(os.Geteuid())
	gid := uint32(os.Getegid())
	require.NotZero(t, uid)
	tests := map[string]struct {
		configure     func(t *testing.T, deps *defaultUserDirDeps, primary, closeFailure error)
		wantPrimary   bool
		wantClose     bool
		errorContains string
	}{
		"fsync": {
			configure: func(_ *testing.T, deps *defaultUserDirDeps, primary, _ error) {
				deps.fsync = func(context.Context, int) error { return primary }
			},
			wantPrimary:   true,
			errorContains: "sync invoking user directory .cache",
		},
		"close": {
			configure: func(t *testing.T, deps *defaultUserDirDeps, _, closeFailure error) {
				realClose := deps.close
				deps.close = func(ctx context.Context, fd int) error {
					path := descriptorPath(t, fd)
					err := realClose(ctx, fd)
					if filepath.Base(path) == ProjectName {
						return closeFailure
					}
					return err
				}
			},
			wantClose:     true,
			errorContains: "close invoking user project directory",
		},
		"fsync_and_close": {
			configure: func(t *testing.T, deps *defaultUserDirDeps, primary, closeFailure error) {
				deps.fsync = func(context.Context, int) error { return primary }
				realClose := deps.close
				deps.close = func(ctx context.Context, fd int) error {
					path := descriptorPath(t, fd)
					err := realClose(ctx, fd)
					if filepath.Base(path) == ".cache" {
						return closeFailure
					}
					return err
				}
			},
			wantPrimary:   true,
			wantClose:     true,
			errorContains: "sync invoking user directory .cache",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			deps := testDefaultUserDirDeps(home, uid, gid)
			primary := errors.New("sync failed")
			closeFailure := errors.New("close failed")
			tc.configure(t, &deps, primary, closeFailure)

			_, err := resolveDefaultUserDir(context.Background(), defaultUserCacheDir, deps)
			require.Error(t, err)
			domainErr := errs.AsDomainError(err)
			require.NotNil(t, domainErr)
			assert.Equal(t, errs.CodeConfigError, domainErr.Code)
			assert.ErrorContains(t, err, tc.errorContains)
			if tc.wantPrimary {
				assert.ErrorIs(t, err, primary)
			}
			if tc.wantClose {
				assert.ErrorIs(t, err, closeFailure)
			}
		})
	}
}

func TestResolveUserDirRejectsOverrideUnderSudoBeforeFilesystemMutation(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	uid := uint32(os.Geteuid())
	gid := uint32(os.Getegid())
	require.NotZero(t, uid)
	override := filepath.Join(home, "caller-selected", "cache")
	deps := testDefaultUserDirDeps(home, uid, gid)
	deps.mkdirAll = func(context.Context, string, os.FileMode) error {
		t.Fatal("sudo override must fail before filesystem mutation")
		return nil
	}

	_, err := resolveUserDir(context.Background(), defaultUserCacheDir, override, deps)
	require.Error(t, err)
	assert.ErrorContains(t, err, "override is not allowed under sudo")
	_, statErr := os.Lstat(filepath.Dir(override))
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestResolveUserDirPreservesUnprivilegedOverride(t *testing.T) {
	t.Parallel()

	override := filepath.Join(t.TempDir(), "caller-selected", "config")
	deps := realDefaultUserDirDeps()
	deps.effectiveUID = func() int { return 1000 }

	path, err := resolveUserDir(context.Background(), defaultUserConfigDir, override, deps)
	require.NoError(t, err)
	absolute, err := filepath.Abs(override)
	require.NoError(t, err)
	assert.Equal(t, absolute, path)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestResolveDefaultUserDirDoesNotChownPreExistingBaseOrContents(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	uid := uint32(os.Geteuid())
	gid := uint32(os.Getegid())
	require.NotZero(t, uid)
	base := filepath.Join(home, ".cache")
	require.NoError(t, os.Mkdir(base, 0700))
	sentinel := filepath.Join(base, "existing-user-data")
	require.NoError(t, os.WriteFile(sentinel, []byte("untouched"), 0600))
	deps := testDefaultUserDirDeps(home, uid, gid)
	realFchown := deps.fchown
	fchowned := make([]string, 0, 1)
	deps.fchown = func(ctx context.Context, fd int, ownerUID, ownerGID int) error {
		fchowned = append(fchowned, descriptorPath(t, fd))
		return realFchown(ctx, fd, ownerUID, ownerGID)
	}

	path, err := resolveDefaultUserDir(context.Background(), defaultUserCacheDir, deps)
	require.NoError(t, err)
	assert.Equal(t, []string{path}, fchowned)
	content, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	assert.Equal(t, "untouched", string(content))
}

func TestResolveDefaultUserDirDoesNotChownPreExistingProjectContents(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	uid := uint32(os.Geteuid())
	gid := uint32(os.Getegid())
	require.NotZero(t, uid)
	project := filepath.Join(home, ".config", ProjectName)
	require.NoError(t, os.MkdirAll(project, 0700))
	sentinel := filepath.Join(project, "existing-user-data")
	require.NoError(t, os.WriteFile(sentinel, []byte("untouched"), 0600))
	deps := testDefaultUserDirDeps(home, uid, gid)
	deps.fchown = func(context.Context, int, int, int) error {
		t.Fatal("pre-existing project directory must not be chowned")
		return nil
	}

	path, err := resolveDefaultUserDir(context.Background(), defaultUserConfigDir, deps)
	require.NoError(t, err)
	assert.Equal(t, project, path)
	content, err := os.ReadFile(sentinel)
	require.NoError(t, err)
	assert.Equal(t, "untouched", string(content))
}

func TestResolveSudoUserIdentityFailsClosed(t *testing.T) {
	t.Parallel()

	valid := map[string]string{
		"SUDO_UID":  "1000",
		"SUDO_GID":  "1001",
		"SUDO_USER": "runner",
	}
	tests := map[string]struct {
		environment map[string]string
		lookup      func(string) (*user.User, error)
		wantSudo    bool
		wantErr     string
	}{
		"valid": {
			environment: valid,
			lookup: func(string) (*user.User, error) {
				return &user.User{Uid: "1000", Gid: "1001", Username: "runner", HomeDir: "/home/runner"}, nil
			},
			wantSudo: true,
		},
		"partial_environment": {
			environment: map[string]string{"SUDO_USER": "runner"},
			wantErr:     "complete sudo identity",
		},
		"malformed_uid": {
			environment: map[string]string{"SUDO_UID": "user", "SUDO_GID": "1001", "SUDO_USER": "runner"},
			wantErr:     "invalid SUDO_UID",
		},
		"root_uid": {
			environment: map[string]string{"SUDO_UID": "0", "SUDO_GID": "1001", "SUDO_USER": "runner"},
			wantErr:     "non-root",
		},
		"lookup_failure": {
			environment: valid,
			lookup: func(string) (*user.User, error) {
				return nil, errors.New("NSS failed")
			},
			wantErr: "look up sudo user",
		},
		"username_mismatch": {
			environment: valid,
			lookup: func(string) (*user.User, error) {
				return &user.User{Uid: "1000", Gid: "1001", Username: "other", HomeDir: "/home/other"}, nil
			},
			wantErr: "does not match SUDO_USER",
		},
		"group_mismatch": {
			environment: valid,
			lookup: func(string) (*user.User, error) {
				return &user.User{Uid: "1000", Gid: "1002", Username: "runner", HomeDir: "/home/runner"}, nil
			},
			wantErr: "does not match SUDO_GID",
		},
		"unsafe_home": {
			environment: valid,
			lookup: func(string) (*user.User, error) {
				return &user.User{Uid: "1000", Gid: "1001", Username: "runner", HomeDir: "/"}, nil
			},
			wantErr: "unsafe home directory",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			deps := defaultUserDirDeps{
				effectiveUID: func() int { return 0 },
				lookupEnv: func(name string) (string, bool) {
					value, ok := tc.environment[name]
					return value, ok
				},
				lookupUserID: tc.lookup,
			}
			identity, sudo, err := resolveSudoUserIdentity(deps)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.ErrorContains(t, err, tc.wantErr)
				assert.False(t, sudo)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantSudo, sudo)
			assert.Equal(t, uint32(1000), identity.uid)
			assert.Equal(t, "/home/runner", identity.home)
		})
	}
}

func TestResolveSudoUserIdentityIgnoresEnvironmentWhenUnprivileged(t *testing.T) {
	t.Parallel()

	deps := defaultUserDirDeps{
		effectiveUID: func() int { return 1000 },
		lookupEnv: func(string) (string, bool) {
			return "hostile", true
		},
		lookupUserID: func(string) (*user.User, error) {
			t.Fatal("unprivileged resolution must not consume sudo identity")
			return nil, nil
		},
	}
	_, sudo, err := resolveSudoUserIdentity(deps)
	require.NoError(t, err)
	assert.False(t, sudo)
}

func testDefaultUserDirDeps(home string, uid, gid uint32) defaultUserDirDeps {
	deps := realDefaultUserDirDeps()
	deps.effectiveUID = func() int { return 0 }
	deps.lookupEnv = func(name string) (string, bool) {
		values := map[string]string{
			"SUDO_UID":  strconv.FormatUint(uint64(uid), 10),
			"SUDO_GID":  strconv.FormatUint(uint64(gid), 10),
			"SUDO_USER": "runner",
		}
		value, ok := values[name]
		return value, ok
	}
	deps.lookupUserID = func(id string) (*user.User, error) {
		if id != strconv.FormatUint(uint64(uid), 10) {
			return nil, errors.New("unexpected UID lookup")
		}
		return &user.User{
			Uid:      strconv.FormatUint(uint64(uid), 10),
			Gid:      strconv.FormatUint(uint64(gid), 10),
			Username: "runner",
			HomeDir:  home,
		}, nil
	}
	return deps
}

func assertDirectoryOwner(t *testing.T, path string, uid, gid uint32) {
	t.Helper()

	var stat unix.Stat_t
	require.NoError(t, unix.Lstat(path, &stat))
	assert.Equal(t, uid, stat.Uid)
	assert.Equal(t, gid, stat.Gid)
	assert.Equal(t, uint32(0700), stat.Mode&07777)
}

func descriptorPath(t *testing.T, fd int) string {
	t.Helper()

	path, err := os.Readlink("/proc/self/fd/" + strconv.Itoa(fd))
	require.NoError(t, err)
	return path
}
