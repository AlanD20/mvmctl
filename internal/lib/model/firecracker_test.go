package model_test

import (
	"database/sql/driver"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/lib/model"
)

func TestNewVMCgroupLimits_DerivesDeterministicEnvelope(t *testing.T) {
	got := model.NewVMCgroupLimits(2, 512, model.VMCgroupPolicy{
		VMMHeadroomMiB: 128,
		CPUWeight:      100,
		PIDsMax:        256,
		SwapMaxBytes:   0,
	})
	want := model.VMCgroupLimits{
		PolicyVersion:   1,
		CPUQuotaMicros:  200000,
		CPUPeriodMicros: 100000,
		CPUWeight:       100,
		MemoryHighBytes: 640 * 1024 * 1024,
		MemoryMaxBytes:  640 * 1024 * 1024,
		SwapMaxBytes:    0,
		PIDsMax:         256,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("derived cgroup limits mismatch (-want +got):\n%s", diff)
	}
}

func TestVMCgroupLimits_SQLRoundTrip(t *testing.T) {
	want := model.NewVMCgroupLimits(4, 1024, model.VMCgroupPolicy{
		VMMHeadroomMiB: 128, CPUWeight: 250, PIDsMax: 512, SwapMaxBytes: 0,
	})
	value, err := want.Value()
	require.NoError(t, err)
	encoded, ok := value.([]byte)
	require.True(t, ok)

	for name, source := range map[string]any{"bytes": encoded, "string": string(encoded)} {
		t.Run(name, func(t *testing.T) {
			var got model.VMCgroupLimits
			require.NoError(t, got.Scan(source))
			assert.Equal(t, want, got)
		})
	}

	var got model.VMCgroupLimits
	require.NoError(t, got.Scan(nil))
	assert.Equal(t, model.VMCgroupLimits{}, got)
	assert.Error(t, got.Scan(driver.Value(int64(1))))
}

func TestSnapshotExtraConfig_PreservesCgroupEnvelope(t *testing.T) {
	wantLimits := model.NewVMCgroupLimits(3, 768, model.VMCgroupPolicy{
		VMMHeadroomMiB: 128, CPUWeight: 100, PIDsMax: 256, SwapMaxBytes: 0,
	})
	want := model.SnapshotExtraConfig{BootArgs: "quiet", CgroupLimits: wantLimits}
	value, err := want.Value()
	require.NoError(t, err)

	var got model.SnapshotExtraConfig
	require.NoError(t, got.Scan(value))
	assert.Equal(t, want, got)
}
