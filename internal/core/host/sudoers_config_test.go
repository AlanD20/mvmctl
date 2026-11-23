package host

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestConfigureSudoersVerifiesCanonicalBinaryBeforePolicyAccess(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*systemBinaryInstallFixture)
	}{
		{"absent", func(f *systemBinaryInstallFixture) { f.targetExists = false }},
		{"symlink", func(f *systemBinaryInstallFixture) {
			s := f.stats[installTargetFD]
			s.Mode = unix.S_IFLNK | 0777
			f.stats[installTargetFD] = s
		}},
		{"owner", func(f *systemBinaryInstallFixture) {
			s := f.stats[installTargetFD]
			s.Uid = 1000
			f.stats[installTargetFD] = s
		}},
		{"mode", func(f *systemBinaryInstallFixture) {
			s := f.stats[installTargetFD]
			s.Mode = unix.S_IFREG | 0775
			f.stats[installTargetFD] = s
		}},
		{"writable ancestor", func(f *systemBinaryInstallFixture) {
			s := f.stats[installLocalFD]
			s.Mode = unix.S_IFDIR | 0775
			f.stats[installLocalFD] = s
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newSystemBinaryInstallFixture()
			tt.mutate(fixture)
			changed, err := configureSudoers(context.Background(), sudoersConfigDeps{
				verifySystemBinary: func() error {
					return verifySystemBinaryForSudoers(fixture.deps, productionSystemBinaryInstallPolicy())
				},
				// Nil effect dependencies make any policy access a test failure.
			})
			require.Error(t, err)
			assert.False(t, changed)
		})
	}
}
