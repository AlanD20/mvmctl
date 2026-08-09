package jailer

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestVMDirForManagedResource(t *testing.T) {
	tests := map[string]struct {
		resource string
		want     string
	}{
		"volume": {
			resource: "/home/user/.cache/mvmctl/volumes/data.raw",
			want:     filepath.Join("/home/user/.cache/mvmctl", "vms", testVMID),
		},
		"snapshot": {
			resource: "/home/user/.cache/mvmctl/snapshots/" + testResourceID,
			want:     filepath.Join("/home/user/.cache/mvmctl", "vms", testVMID),
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, vmDirForManagedResource(testVMID, tc.resource))
		})
	}
}

// Rationale: A cancelled caller must not launch the detached privileged
// service, because lifecycle ownership has not yet transferred to a process.
func TestLaunchRejectsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	process, err := Launch(ctx, testVMID, "/managed/vms/"+testVMID, nil, nil, nil)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, process)
}
