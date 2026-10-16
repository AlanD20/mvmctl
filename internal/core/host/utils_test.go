package host_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"mvmctl/internal/core/host"
	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
)

// --- GenerateSudoersContent ---
// Rationale: Exact bytes keep the passwordless grant scoped to the root-owned
// system binary's early privileged marker, including for a different group.

func TestGenerateSudoersContent(t *testing.T) {
	for _, group := range []string{"mvm", "customgroup"} {
		t.Run(group, func(t *testing.T) {
			want := "# Managed by mvmctl — do not edit manually.\n" +
				"# To remove: sudo /usr/local/bin/mvm host reset --force\n" +
				"%" + group + " ALL=(root) NOPASSWD: /usr/local/bin/mvm __mvm_privileged_v1 *\n"
			if diff := cmp.Diff(want, host.GenerateSudoersContent(group)); diff != "" {
				t.Errorf("sudoers policy (-want +got):\n%s", diff)
			}
		})
	}
}

// --- HardwareFromState ---
// Rationale: Reconstructs HostHardware from persisted HostStateItem. Bugs here
// cause stale capacity detection to silently report zero/null values, which
// breaks VM placement and resource accounting.

func TestHardwareFromState(t *testing.T) {
	tests := map[string]struct {
		state *model.HostStateItem
		want  *model.HostHardware
	}{
		// Error/invalid case first: nil CPUModel means detection never ran.
		"nil_cpu_model_returns_nil": {
			state: &model.HostStateItem{
				CPUModel: nil,
			},
			want: nil,
		},
		// Happy path: all fields mapped correctly.
		"all_fields_set": {
			state: &model.HostStateItem{
				Hostname:          ptr("test-host"),
				CPUModel:          ptr("Intel(R) Xeon(R) Platinum 8375C"),
				CPUVendor:         ptr("GenuineIntel"),
				CPUCores:          ptr(8),
				CPUArchitecture:   ptr("x86_64"),
				NumaNodes:         ptr(2),
				MemoryTotalMiB:    ptr(16384),
				StorageTotalBytes: ptr(500000000000),
				KernelVersion:     ptr("6.2.0-42-generic"),
				OSRelease:         ptr("Ubuntu 22.04.3 LTS"),
				CPUHasVMX:         ptr(1),
				CPUHypervisor:     ptr(0),
			},
			want: &model.HostHardware{
				Hostname:          "test-host",
				CPUModel:          "Intel(R) Xeon(R) Platinum 8375C",
				CPUVendor:         "GenuineIntel",
				CPUCores:          8,
				CPUArchitecture:   "x86_64",
				NumaNodes:         2,
				MemoryTotalMiB:    16384,
				StorageTotalBytes: 500000000000,
				KernelVersion:     "6.2.0-42-generic",
				OSRelease:         "Ubuntu 22.04.3 LTS",
				CPUHasVMX:         true,
				CPUHypervisor:     false,
			},
		},
		// Partial fields: unset fields default to zero/empty, NumaNodes defaults to 1.
		"partial_fields_default_zero": {
			state: &model.HostStateItem{
				Hostname:  ptr("partial"),
				CPUModel:  ptr("Some CPU"),
				CPUCores:  ptr(4),
				CPUVendor: ptr("SomeVendor"),
			},
			want: &model.HostHardware{
				Hostname:      "partial",
				CPUModel:      "Some CPU",
				CPUVendor:     "SomeVendor",
				CPUCores:      4,
				NumaNodes:     1,
				CPUHypervisor: false,
			},
		},
		// NumaNodes nil → default to 1.
		"numanodes_nil_defaults_to_1": {
			state: &model.HostStateItem{
				CPUModel:  ptr("x"),
				NumaNodes: nil,
			},
			want: &model.HostHardware{
				CPUModel:      "x",
				NumaNodes:     1,
				CPUHypervisor: false,
			},
		},
		// NumaNodes 0 → default to 1.
		"numanodes_zero_defaults_to_1": {
			state: &model.HostStateItem{
				CPUModel:  ptr("x"),
				NumaNodes: ptr(0),
			},
			want: &model.HostHardware{
				CPUModel:      "x",
				NumaNodes:     1,
				CPUHypervisor: false,
			},
		},
		// CPUHasVMX 0 → false.
		"cpu_has_vmx_zero_is_false": {
			state: &model.HostStateItem{
				CPUModel:  ptr("x"),
				CPUHasVMX: ptr(0),
			},
			want: &model.HostHardware{
				CPUModel:      "x",
				NumaNodes:     1,
				CPUHasVMX:     false,
				CPUHypervisor: false,
			},
		},
		// CPUHasVMX non-zero → true.
		"cpu_has_vmx_nonzero_is_true": {
			state: &model.HostStateItem{
				CPUModel:  ptr("x"),
				CPUHasVMX: ptr(2),
			},
			want: &model.HostHardware{
				CPUModel:      "x",
				NumaNodes:     1,
				CPUHasVMX:     true,
				CPUHypervisor: false,
			},
		},
		// CPUHypervisor 0 → false.
		"cpu_hypervisor_zero_is_false": {
			state: &model.HostStateItem{
				CPUModel:      ptr("x"),
				CPUHypervisor: ptr(0),
			},
			want: &model.HostHardware{
				CPUModel:      "x",
				NumaNodes:     1,
				CPUHypervisor: false,
			},
		},
		// CPUHypervisor non-zero → true.
		"cpu_hypervisor_nonzero_is_true": {
			state: &model.HostStateItem{
				CPUModel:      ptr("x"),
				CPUHypervisor: ptr(1),
			},
			want: &model.HostHardware{
				CPUModel:      "x",
				NumaNodes:     1,
				CPUHypervisor: true,
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := host.HardwareFromState(tc.state)

			// Invalid case: nil CPUModel → nil result.
			if tc.want == nil {
				assert.Nil(t, got)
				return
			}

			require.NotNil(t, got)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("HardwareFromState() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// --- LimitsFromState ---
// Rationale: Reconstructs HostLimits from persisted HostStateItem. Incorrect
// defaults (port range, KSM, cgroup version) can silently break networking,
// overcommit host resources, or misreport virtualization capabilities.

func TestLimitsFromState(t *testing.T) {
	tests := map[string]struct {
		state *model.HostStateItem
		want  *model.HostLimits
	}{
		// Error/invalid case first: nil PIDMax means limits detection never ran.
		"nil_pid_max_returns_nil": {
			state: &model.HostStateItem{
				PIDMax: nil,
			},
			want: nil,
		},
		// Happy path: all fields mapped correctly.
		"all_fields_set": {
			state: &model.HostStateItem{
				PIDMax:              ptr(32768),
				FDMax:               ptr(1048576),
				ConntrackMax:        ptr(262144),
				TAPDevicesMax:       ptr(255),
				IPLocalPortRange:    ptr("32768,60999"),
				NestedVirtAvailable: ptr(1),
				EPTAvailable:        ptr(1),
				HugepageCount2MB:    ptr(1024),
				KSMDisabled:         ptr(1),
				CgroupVersion:       ptr(2),
				SwapTotalMiB:        ptr(4096),
				KernelMinimumMet:    ptr(1),
			},
			want: &model.HostLimits{
				PIDMax:              32768,
				FDMax:               1048576,
				ConntrackMax:        262144,
				TAPDevicesMax:       255,
				IPLocalPortRange:    [2]int{32768, 60999},
				NestedVirtAvailable: true,
				EPTAvailable:        true,
				HugepageCount2MB:    1024,
				KSMDisabled:         true,
				CgroupVersion:       2,
				SwapTotalMiB:        4096,
				KernelMinimumMet:    true,
			},
		},
		// IPLocalPortRange nil → default port range.
		"ip_local_port_range_nil_uses_default": {
			state: &model.HostStateItem{
				PIDMax:           ptr(100),
				IPLocalPortRange: nil,
			},
			want: &model.HostLimits{
				PIDMax:           100,
				IPLocalPortRange: infra.DefaultIPLocalPortRange,
				KSMDisabled:      true,
				CgroupVersion:    1,
			},
		},
		// IPLocalPortRange invalid format → default port range.
		"ip_local_port_range_invalid_uses_default": {
			state: &model.HostStateItem{
				PIDMax:           ptr(100),
				IPLocalPortRange: ptr("not-a-range"),
			},
			want: &model.HostLimits{
				PIDMax:           100,
				IPLocalPortRange: infra.DefaultIPLocalPortRange,
				KSMDisabled:      true,
				CgroupVersion:    1,
			},
		},
		// NestedVirtAvailable 0 → false.
		"nested_virt_available_zero_is_false": {
			state: &model.HostStateItem{
				PIDMax:              ptr(100),
				NestedVirtAvailable: ptr(0),
			},
			want: &model.HostLimits{
				PIDMax:              100,
				IPLocalPortRange:    infra.DefaultIPLocalPortRange,
				NestedVirtAvailable: false,
				KSMDisabled:         true,
				CgroupVersion:       1,
			},
		},
		// NestedVirtAvailable non-zero → true.
		"nested_virt_available_nonzero_is_true": {
			state: &model.HostStateItem{
				PIDMax:              ptr(100),
				NestedVirtAvailable: ptr(2),
			},
			want: &model.HostLimits{
				PIDMax:              100,
				IPLocalPortRange:    infra.DefaultIPLocalPortRange,
				NestedVirtAvailable: true,
				KSMDisabled:         true,
				CgroupVersion:       1,
			},
		},
		// EPTAvailable 0 → false.
		"ept_available_zero_is_false": {
			state: &model.HostStateItem{
				PIDMax:       ptr(100),
				EPTAvailable: ptr(0),
			},
			want: &model.HostLimits{
				PIDMax:           100,
				IPLocalPortRange: infra.DefaultIPLocalPortRange,
				EPTAvailable:     false,
				KSMDisabled:      true,
				CgroupVersion:    1,
			},
		},
		// EPTAvailable non-zero → true.
		"ept_available_nonzero_is_true": {
			state: &model.HostStateItem{
				PIDMax:       ptr(100),
				EPTAvailable: ptr(1),
			},
			want: &model.HostLimits{
				PIDMax:           100,
				IPLocalPortRange: infra.DefaultIPLocalPortRange,
				EPTAvailable:     true,
				KSMDisabled:      true,
				CgroupVersion:    1,
			},
		},
		// KSMDisabled nil → true (default to disabled is safe).
		"ksm_disabled_nil_defaults_true": {
			state: &model.HostStateItem{
				PIDMax:      ptr(100),
				KSMDisabled: nil,
			},
			want: &model.HostLimits{
				PIDMax:           100,
				IPLocalPortRange: infra.DefaultIPLocalPortRange,
				KSMDisabled:      true,
				CgroupVersion:    1,
			},
		},
		// KSMDisabled = 0 → false (KSM not disabled).
		"ksm_disabled_zero_is_false": {
			state: &model.HostStateItem{
				PIDMax:      ptr(100),
				KSMDisabled: ptr(0),
			},
			want: &model.HostLimits{
				PIDMax:           100,
				IPLocalPortRange: infra.DefaultIPLocalPortRange,
				KSMDisabled:      false,
				CgroupVersion:    1,
			},
		},
		// KSMDisabled non-zero → true (KSM is disabled).
		"ksm_disabled_nonzero_is_true": {
			state: &model.HostStateItem{
				PIDMax:      ptr(100),
				KSMDisabled: ptr(1),
			},
			want: &model.HostLimits{
				PIDMax:           100,
				IPLocalPortRange: infra.DefaultIPLocalPortRange,
				KSMDisabled:      true,
				CgroupVersion:    1,
			},
		},
		// CgroupVersion nil → default to 1.
		"cgroup_version_nil_defaults_1": {
			state: &model.HostStateItem{
				PIDMax:        ptr(100),
				CgroupVersion: nil,
			},
			want: &model.HostLimits{
				PIDMax:           100,
				IPLocalPortRange: infra.DefaultIPLocalPortRange,
				KSMDisabled:      true,
				CgroupVersion:    1,
			},
		},
		// CgroupVersion 0 → default to 1.
		"cgroup_version_zero_defaults_1": {
			state: &model.HostStateItem{
				PIDMax:        ptr(100),
				CgroupVersion: ptr(0),
			},
			want: &model.HostLimits{
				PIDMax:           100,
				IPLocalPortRange: infra.DefaultIPLocalPortRange,
				KSMDisabled:      true,
				CgroupVersion:    1,
			},
		},
		// CgroupVersion 2 → preserved.
		"cgroup_version_valid_preserved": {
			state: &model.HostStateItem{
				PIDMax:        ptr(100),
				CgroupVersion: ptr(2),
			},
			want: &model.HostLimits{
				PIDMax:           100,
				IPLocalPortRange: infra.DefaultIPLocalPortRange,
				KSMDisabled:      true,
				CgroupVersion:    2,
			},
		},
		// KernelMinimumMet 0 → false.
		"kernel_minimum_met_zero_is_false": {
			state: &model.HostStateItem{
				PIDMax:           ptr(100),
				KernelMinimumMet: ptr(0),
			},
			want: &model.HostLimits{
				PIDMax:           100,
				IPLocalPortRange: infra.DefaultIPLocalPortRange,
				KSMDisabled:      true,
				CgroupVersion:    1,
				KernelMinimumMet: false,
			},
		},
		// KernelMinimumMet non-zero → true.
		"kernel_minimum_met_nonzero_is_true": {
			state: &model.HostStateItem{
				PIDMax:           ptr(100),
				KernelMinimumMet: ptr(1),
			},
			want: &model.HostLimits{
				PIDMax:           100,
				IPLocalPortRange: infra.DefaultIPLocalPortRange,
				KSMDisabled:      true,
				CgroupVersion:    1,
				KernelMinimumMet: true,
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := host.LimitsFromState(tc.state)

			// Invalid case: nil PIDMax → nil result.
			if tc.want == nil {
				assert.Nil(t, got)
				return
			}

			require.NotNil(t, got)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("LimitsFromState() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
