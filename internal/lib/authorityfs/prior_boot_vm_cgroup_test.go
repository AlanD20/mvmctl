package authorityfs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/sys/unix"
)

func TestPriorBootCgroupModeAdmitted(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		mode uint16
		root bool
		want bool
	}{
		"read_only_cgroup2_root": {mode: unix.S_IFDIR | 0555, root: true, want: true},
		"writable_cgroup2_root":  {mode: unix.S_IFDIR | 0755, root: true, want: true},
		"parent_remains_0755":    {mode: unix.S_IFDIR | 0755, want: true},
		"parent_rejects_0555":    {mode: unix.S_IFDIR | 0555},
		"root_rejects_0700":      {mode: unix.S_IFDIR | 0700, root: true},
		"root_rejects_0777":      {mode: unix.S_IFDIR | 0777, root: true},
		"root_rejects_file":      {mode: unix.S_IFREG | 0555, root: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, priorBootCgroupModeAdmitted(tc.mode, tc.root))
		})
	}
}
