package jailer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
)

func TestObserveManagedVMCgroupReadsProtectedLeaf(t *testing.T) {
	fixture := newManagedVMCgroupTestFixture(t)
	requested := testManagedVMCgroupLimits()
	fixture.addVMLeafWithLimits(testVMID, requested)
	addObservedCgroupUsageFiles(t, fixture.vmLeafPath(testVMID))

	got, err := observeManagedVMCgroup(t.Context(), fixture.deps(), testVMID, 4242, requested)
	require.NoError(t, err)
	want := model.VMCgroupState{
		Path:      filepath.Join(infra.CgroupV2Root, infra.JailerCgroupParent, testVMID),
		Requested: requested, Actual: &requested,
		Usage:  &model.VMCgroupUsage{CPUUsageMicros: 901, MemoryBytes: 4096, SwapBytes: 0, PIDsCurrent: 1},
		Status: model.VMCgroupEnforcementEnforced,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("protected cgroup observation (-want +got):\n%s", diff)
	}
}

func TestObserveManagedVMCgroupRejectsForeignMembership(t *testing.T) {
	fixture := newManagedVMCgroupTestFixture(t)
	requested := testManagedVMCgroupLimits()
	fixture.addVMLeafWithLimits(testVMID, requested)
	addObservedCgroupUsageFiles(t, fixture.vmLeafPath(testVMID))
	fixture.setMembership(testVMID, "4242\n4243\n")

	_, err := observeManagedVMCgroup(t.Context(), fixture.deps(), testVMID, 4242, requested)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "inexact process membership")
}

func TestObserveManagedVMCgroupReportsChangedLimit(t *testing.T) {
	fixture := newManagedVMCgroupTestFixture(t)
	requested := testManagedVMCgroupLimits()
	fixture.addVMLeafWithLimits(testVMID, requested)
	addObservedCgroupUsageFiles(t, fixture.vmLeafPath(testVMID))
	weight := filepath.Join(fixture.vmLeafPath(testVMID), "cpu.weight")
	require.NoError(t, os.WriteFile(weight, []byte("101\n"), 0o644))

	got, err := observeManagedVMCgroup(t.Context(), fixture.deps(), testVMID, 4242, requested)
	require.NoError(t, err)
	assert.Equal(t, model.VMCgroupEnforcementMismatch, got.Status)
	assert.Equal(t, []string{"CPU weight"}, got.Mismatches)
	require.NotNil(t, got.Actual)
	assert.Equal(t, int64(101), got.Actual.CPUWeight)
}

func addObservedCgroupUsageFiles(t *testing.T, leafPath string) {
	t.Helper()
	contents := map[string]string{
		"cpu.stat":       "usage_usec 901\nuser_usec 400\nsystem_usec 501\n",
		"memory.current": "4096\n", "memory.swap.current": "0\n", "pids.current": "1\n",
	}
	for name, value := range contents {
		path := filepath.Join(leafPath, name)
		require.NoError(t, os.WriteFile(path, []byte(value), 0o444))
		require.NoError(t, os.Chmod(path, 0o444))
	}
}
