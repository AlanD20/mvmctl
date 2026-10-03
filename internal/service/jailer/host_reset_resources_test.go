package jailer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/pkg/errs"
)

func TestHostResetJailResourcesAdmitsOnlyEmptyPrivateParents(t *testing.T) {
	if os.Geteuid() != 0 || os.Getegid() != 0 {
		t.Skip("exact root-owned reset resources require the disposable root-test runner")
	}
	// Rationale: administrator initialization creates private 0700 Jailer parents, which survive
	// stopped-VM cleanup. Reset must admit that empty tree without adopting 0755 private parents
	// or removing residual resources. The real filesystem walk must preserve all admitted metadata.
	tests := map[string]struct {
		jailerMode      os.FileMode
		firecrackerMode os.FileMode
		residual        bool
		wantErr         bool
	}{
		"public jailer parent":      {jailerMode: 0755, firecrackerMode: 0700, wantErr: true},
		"public firecracker parent": {jailerMode: 0700, firecrackerMode: 0755, wantErr: true},
		"residual VM resource":      {jailerMode: 0700, firecrackerMode: 0700, residual: true, wantErr: true},
		"initialized private tree":  {jailerMode: 0700, firecrackerMode: 0700},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			paths := []string{"var", "var/lib", "var/lib/mvmctl", "var/lib/mvmctl/jailer",
				"var/lib/mvmctl/jailer/firecracker"}
			before := make(map[string]unix.Stat_t, len(paths))
			for index, path := range paths {
				mode := os.FileMode(0755)
				if index == 3 {
					mode = tc.jailerMode
				} else if index == 4 {
					mode = tc.firecrackerMode
				}
				require.NoError(t, os.Mkdir(filepath.Join(root, path), mode))
				require.NoError(t, os.Chmod(filepath.Join(root, path), mode))
			}
			residual := filepath.Join(root, paths[4], testVMID)
			if tc.residual {
				require.NoError(t, os.WriteFile(residual, []byte("retained VM resource"), 0600))
			}
			for _, path := range paths {
				var stat unix.Stat_t
				require.NoError(t, unix.Lstat(filepath.Join(root, path), &stat))
				before[path] = stat
			}
			rootFD, err := unix.Open(root, instanceDirectoryFlags, 0)
			require.NoError(t, err)
			guard := &HostResetGuard{state: &hostResetGuardState{roots: &instanceAuthorityRoots{
				deps: realInstanceAuthorityDeps(), retained: []int{rootFD},
			}}}
			t.Cleanup(func() { assert.NoError(t, guard.Release(t.Context())) })
			t.Cleanup(func() {
				after := make(map[string]unix.Stat_t, len(paths))
				for _, path := range paths {
					var stat unix.Stat_t
					require.NoError(t, unix.Lstat(filepath.Join(root, path), &stat))
					after[path] = stat
				}
				// Directory enumeration may update access time; authority metadata must not change.
				if diff := cmp.Diff(before, after, cmpopts.IgnoreFields(unix.Stat_t{}, "Atim")); diff != "" {
					t.Errorf("reset resource metadata changed (-want +got):\n%s", diff)
				}
				entries, err := os.ReadDir(filepath.Join(root, paths[4]))
				require.NoError(t, err)
				if tc.residual {
					require.Len(t, entries, 1)
					assert.Equal(t, testVMID, entries[0].Name())
					data, err := os.ReadFile(residual)
					require.NoError(t, err)
					assert.Equal(t, "retained VM resource", string(data))
				} else {
					assert.Empty(t, entries)
				}
			})

			err = guard.requireNoJailResources(t.Context())
			if tc.wantErr {
				require.Error(t, err)
				assert.Equal(t, errs.CodeVMAtomicFailed, errs.AsDomainError(err).Code)
				return
			}
			assert.NoError(t, err)
		})
	}
}
