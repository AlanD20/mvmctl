package hostpolicy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

const markerPolicyFixture = "%mvm ALL=(root) NOPASSWD: /usr/local/bin/mvm __mvm_privileged_v1 *\n"

func TestObserverAdmitsOnlyStableManagedPolicy(t *testing.T) {
	tests := map[string]struct {
		mutate     func(*testing.T, string, *observerDeps, *observerPolicy)
		wantErr    string
		wantExists bool
	}{
		"unsafe directory": {mutate: func(t *testing.T, root string, _ *observerDeps, _ *observerPolicy) {
			require.NoError(t, os.Chmod(filepath.Join(root, "etc"), 0777))
		}, wantErr: "unsafe managed host policy directory"},
		"wrong owner": {mutate: func(_ *testing.T, _ string, _ *observerDeps, p *observerPolicy) {
			p.uid++
		}, wantErr: "ownership"},
		"symlink": {mutate: func(t *testing.T, root string, _ *observerDeps, _ *observerPolicy) {
			path := filepath.Join(root, "etc/sudoers.d/mvm")
			require.NoError(t, os.Rename(path, path+".original"))
			require.NoError(t, os.Symlink("mvm.original", path))
		}, wantErr: "open managed host policy"},
		"hardlink": {mutate: func(t *testing.T, root string, _ *observerDeps, _ *observerPolicy) {
			path := filepath.Join(root, "etc/sudoers.d/mvm")
			require.NoError(t, os.Link(path, path+".alias"))
		}, wantErr: "metadata"},
		"wrong mode": {mutate: func(t *testing.T, root string, _ *observerDeps, _ *observerPolicy) {
			require.NoError(t, os.Chmod(filepath.Join(root, "etc/sudoers.d/mvm"), 0640))
		}, wantErr: "metadata"},
		"wrong file owner": {mutate: func(_ *testing.T, _ string, deps *observerDeps, _ *observerPolicy) {
			fstat := deps.fstat
			deps.fstat = func(fd int, stat *unix.Stat_t) error {
				err := fstat(fd, stat)
				if stat.Mode&unix.S_IFMT == unix.S_IFREG {
					stat.Uid++
				}
				return err
			}
		}, wantErr: "metadata"},
		"FIFO": {mutate: func(t *testing.T, root string, _ *observerDeps, _ *observerPolicy) {
			path := filepath.Join(root, "etc/sudoers.d/mvm")
			require.NoError(t, os.Remove(path))
			require.NoError(t, unix.Mkfifo(path, 0440))
		}, wantErr: "metadata"},
		"oversized": {mutate: func(t *testing.T, root string, _ *observerDeps, _ *observerPolicy) {
			replacePolicyFixture(t, root, strings.Repeat("#", policyMaxBytes+1))
		}, wantErr: "metadata"},
		"raw grant": {mutate: func(t *testing.T, root string, _ *observerDeps, _ *observerPolicy) {
			replacePolicyFixture(t, root, "%mvm ALL=(root) NOPASSWD: /usr/bin/ip *\n")
		}, wantErr: "marker-only"},
		"read denied": {mutate: func(_ *testing.T, _ string, deps *observerDeps, _ *observerPolicy) {
			deps.read = func(int, []byte) (int, error) { return 0, unix.EACCES }
		}, wantErr: "read managed host policy"},
		"replacement during read": {mutate: func(t *testing.T, root string, deps *observerDeps, _ *observerPolicy) {
			read := deps.read
			replaced := false
			deps.read = func(fd int, data []byte) (int, error) {
				n, err := read(fd, data)
				if !replaced {
					replaced = true
					path := filepath.Join(root, "etc/sudoers.d/mvm")
					require.NoError(t, os.WriteFile(path+".replacement", []byte(markerPolicyFixture), 0440))
					require.NoError(t, os.Rename(path+".replacement", path))
				}
				return n, err
			}
		}, wantErr: "changed during read"},
		"directory replacement during read": {mutate: func(t *testing.T, root string,
			deps *observerDeps, _ *observerPolicy,
		) {
			read := deps.read
			replaced := false
			deps.read = func(fd int, data []byte) (int, error) {
				n, err := read(fd, data)
				if !replaced {
					replaced = true
					path := filepath.Join(root, "etc/sudoers.d")
					require.NoError(t, os.Rename(path, path+".previous"))
					require.NoError(t, os.Mkdir(path, 0755))
				}
				return n, err
			}
		}, wantErr: "directory changed during read"},
		"close fails after absence": {mutate: func(t *testing.T, root string, deps *observerDeps, _ *observerPolicy) {
			require.NoError(t, os.Remove(filepath.Join(root, "etc/sudoers.d/mvm")))
			deps.close = func(fd int) error { return errors.Join(unix.Close(fd), unix.EIO) }
		}, wantErr: "close managed host policy descriptors"},
		"absent": {mutate: func(t *testing.T, root string, _ *observerDeps, _ *observerPolicy) {
			require.NoError(t, os.Remove(filepath.Join(root, "etc/sudoers.d/mvm")))
		}},
		"valid": {wantExists: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(root, "etc/sudoers.d"), 0755))
			require.NoError(t, os.WriteFile(filepath.Join(root, "etc/sudoers.d/mvm"),
				[]byte(markerPolicyFixture), 0440))
			deps := observerDeps{open: unix.Open, openat: unix.Openat, fstat: unix.Fstat,
				fstatat: unix.Fstatat, read: unix.Read, close: unix.Close}
			policy := observerPolicy{root: root, uid: uint32(os.Getuid()), gid: uint32(os.Getgid())}
			if tc.mutate != nil {
				tc.mutate(t, root, &deps, &policy)
			}
			result, err := observePolicy(t.Context(), deps, policy)
			if tc.wantErr != "" {
				assert.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			if diff := cmp.Diff(ObserveResult{Exists: &tc.wantExists}, result); diff != "" {
				t.Errorf("policy observation (-want +got):\n%s", diff)
			}
		})
	}
}

func replacePolicyFixture(t *testing.T, root, content string) {
	t.Helper()
	path := filepath.Join(root, "etc/sudoers.d/mvm")
	require.NoError(t, os.Chmod(path, 0600))
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	require.NoError(t, os.Chmod(path, 0440))
}
