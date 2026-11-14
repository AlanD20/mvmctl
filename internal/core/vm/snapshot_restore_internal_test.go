package vm

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSnapshotVsockOverride(t *testing.T) {
	for name, tc := range map[string]struct {
		enabled bool
		want    string
	}{
		"absent":  {want: ""},
		"present": {enabled: true, want: "/run/mvm/vsock.sock"},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, snapshotVsockOverride(tc.enabled))
		})
	}
}
