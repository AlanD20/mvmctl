package host

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

func TestConfigureSudoers_StateDetectionAndWrite(t *testing.T) {
	wantContent := GenerateSudoersContent(infra.MVMUnixGroup)
	tests := []struct {
		name        string
		readData    []byte
		readErr     error
		wantChanged bool
		wantWrites  int
	}{
		{
			name:       "current policy is unchanged",
			readData:   []byte(wantContent),
			wantWrites: 0,
		},
		{
			name:        "missing policy is installed",
			readErr:     os.ErrNotExist,
			wantChanged: true,
			wantWrites:  1,
		},
		{
			name:        "stale policy is replaced",
			readData:    []byte("%mvm ALL=(root) NOPASSWD: /old/mvm *\n"),
			wantChanged: true,
			wantWrites:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readPath := ""
			writePath := ""
			writeContent := ""
			writes := 0
			deps := sudoersConfigDeps{
				verifySystemBinary: func() error { return nil },
				readFile: func(path string) ([]byte, error) {
					readPath = path
					return tt.readData, tt.readErr
				},
				writeSudoers: func(_ context.Context, path, content string) error {
					writes++
					writePath = path
					writeContent = content
					return nil
				},
			}

			changed, err := configureSudoers(context.Background(), deps)
			require.NoError(t, err)
			assert.Equal(t, tt.wantChanged, changed)
			assert.Equal(t, tt.wantWrites, writes)
			assert.Equal(t, infra.SudoersDropInPath(), readPath)
			if tt.wantWrites > 0 {
				assert.Equal(t, infra.SudoersDropInPath(), writePath)
				assert.Equal(t, wantContent, writeContent)
			}
		})
	}
}

func TestConfigureSudoers_FailsClosed(t *testing.T) {
	tests := []struct {
		name     string
		readErr  error
		writeErr error
	}{
		{name: "unreadable existing policy", readErr: os.ErrPermission},
		{name: "write or visudo failure", writeErr: errors.New("visudo rejected policy")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writes := 0
			deps := sudoersConfigDeps{
				verifySystemBinary: func() error { return nil },
				readFile: func(string) ([]byte, error) {
					if tt.readErr != nil {
						return nil, tt.readErr
					}
					return []byte("stale"), nil
				},
				writeSudoers: func(context.Context, string, string) error {
					writes++
					return tt.writeErr
				},
			}

			changed, err := configureSudoers(context.Background(), deps)
			require.Error(t, err)
			assert.False(t, changed)
			assert.NotNil(t, errs.AsDomainError(err))
			if tt.readErr != nil {
				assert.Zero(t, writes)
			} else {
				assert.Equal(t, 1, writes)
			}
		})
	}
}

func TestConfigureSudoers_VerifiesCanonicalSystemBinaryBeforePolicyAccess(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*systemBinaryInstallFixture)
		wantErr bool
	}{
		{
			name:    "absent target",
			mutate:  func(f *systemBinaryInstallFixture) { f.targetExists = false },
			wantErr: true,
		},
		{
			name: "target symlink",
			mutate: func(f *systemBinaryInstallFixture) {
				stat := f.stats[installTargetFD]
				stat.Mode = unix.S_IFLNK | 0777
				f.stats[installTargetFD] = stat
			},
			wantErr: true,
		},
		{
			name: "wrong target owner",
			mutate: func(f *systemBinaryInstallFixture) {
				stat := f.stats[installTargetFD]
				stat.Uid = 1000
				f.stats[installTargetFD] = stat
			},
			wantErr: true,
		},
		{
			name: "wrong target mode",
			mutate: func(f *systemBinaryInstallFixture) {
				stat := f.stats[installTargetFD]
				stat.Mode = unix.S_IFREG | 0775
				f.stats[installTargetFD] = stat
			},
			wantErr: true,
		},
		{
			name: "unsafe ancestor",
			mutate: func(f *systemBinaryInstallFixture) {
				stat := f.stats[installLocalFD]
				stat.Mode = unix.S_IFDIR | 0775
				f.stats[installLocalFD] = stat
			},
			wantErr: true,
		},
		{name: "valid canonical target", mutate: func(*systemBinaryInstallFixture) {}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newSystemBinaryInstallFixture()
			tt.mutate(fixture)
			reads := 0
			writes := 0
			deps := sudoersConfigDeps{
				verifySystemBinary: func() error {
					return verifySystemBinaryForSudoers(
						fixture.deps,
						productionSystemBinaryInstallPolicy(),
					)
				},
				readFile: func(string) ([]byte, error) {
					reads++
					return nil, os.ErrNotExist
				},
				writeSudoers: func(context.Context, string, string) error {
					writes++
					return nil
				},
			}

			changed, err := configureSudoers(context.Background(), deps)
			if tt.wantErr {
				require.Error(t, err)
				assert.False(t, changed)
				assert.Zero(t, reads)
				assert.Zero(t, writes)
				return
			}
			require.NoError(t, err)
			assert.True(t, changed)
			assert.Equal(t, 1, reads)
			assert.Equal(t, 1, writes)
		})
	}
}
