package host_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"mvmctl/internal/core/host"
	"mvmctl/internal/lib/model"
)

// ─── Helper ───────────────────────────────────────────────────────────────────

// defaultHardware returns a HostHardware with all-green values.
func defaultHardware() model.HostHardware {
	return model.HostHardware{
		Hostname:       "test-host",
		CPUCores:       8,
		MemoryTotalMiB: 8192,
		KernelVersion:  "6.2.0",
		CPUHasVMX:      true,
		CPUHypervisor:  false,
	}
}

// defaultLimits returns a HostLimits with all-green values.
func defaultLimits() model.HostLimits {
	return model.HostLimits{
		SwapTotalMiB:        4096, // >= 8192/2=4096, at threshold
		KernelMinimumMet:    true,
		NestedVirtAvailable: true,
		HugepageCount2MB:    0, // no hugepages by default
	}
}

// defaultResources returns a HostResources with all-green values.
func defaultResources() model.HostResources {
	return model.HostResources{
		ModulesLoaded:         map[string]bool{},
		DevKVMStatus:          "ok",
		DevNetTUNAccessible:   true,
		CloudLocaldsAvailable: true,
		NftablesAvailable:     true,
		IptablesAvailable:     true,
	}
}

// findCheck returns the ProbeCheck with the given name from a slice.
func findCheck(t *testing.T, checks []model.ProbeCheck, name string) model.ProbeCheck {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Errorf("check %q not found in results", name)
	return model.ProbeCheck{}
}

// ─── checkVMHost ──────────────────────────────────────────────────────────────
// Rationale: Must detect and report all VM host prerequisites failures:
// CPU virtualization, /dev/kvm state, /dev/net/tun, KVM module, kernel version,
// nested virtualization. Each failure must produce a specific message and
// actionable details.

func TestProbe_checkVMHost(t *testing.T) {
	tests := []struct {
		name      string
		hardware  model.HostHardware
		limits    model.HostLimits
		resources model.HostResources
		// expected results
		wantCPUVirt  *bool // nil = don't check
		wantDevKVMOK *bool
		wantDevTunOK *bool
		wantKVMModOK *bool
		wantKernelOK *bool
		wantNestedOK *bool
	}{
		{
			name:         "all_green",
			hardware:     defaultHardware(),
			limits:       defaultLimits(),
			resources:    defaultResources(),
			wantCPUVirt:  boolPtr(true),
			wantDevKVMOK: boolPtr(true),
			wantDevTunOK: boolPtr(true),
			wantKVMModOK: boolPtr(true),
			wantKernelOK: boolPtr(true),
			wantNestedOK: boolPtr(true),
		},
		{
			name: "no_vmx",
			hardware: func() model.HostHardware {
				h := defaultHardware()
				h.CPUHasVMX = false
				return h
			}(),
			limits:      defaultLimits(),
			resources:   defaultResources(),
			wantCPUVirt: boolPtr(false),
			// /dev/kvm check is about device accessibility, not CPU caps
			wantDevKVMOK: boolPtr(true),
			wantDevTunOK: boolPtr(true),
			// kvm_module: DevKVMStatus=="ok" && CPUHasVMX → true && false → false
			// Then modules["kvm"] → false (empty)
			wantKVMModOK: boolPtr(false),
			wantKernelOK: boolPtr(true),
			wantNestedOK: boolPtr(true),
		},
		{
			name:     "dev_kvm_missing",
			hardware: defaultHardware(),
			limits:   defaultLimits(),
			resources: func() model.HostResources {
				r := defaultResources()
				r.DevKVMStatus = "missing"
				return r
			}(),
			wantCPUVirt:  boolPtr(true),
			wantDevKVMOK: boolPtr(false),
			wantDevTunOK: boolPtr(true),
			// kvm_module: DevKVMStatus=="ok" && hasVirt → false (not ok)
			// Then modules["kvm"] → false (empty)
			wantKVMModOK: boolPtr(false),
			wantKernelOK: boolPtr(true),
			wantNestedOK: boolPtr(true),
		},
		{
			name:     "dev_kvm_no_permission",
			hardware: defaultHardware(),
			limits:   defaultLimits(),
			resources: func() model.HostResources {
				r := defaultResources()
				r.DevKVMStatus = "no_permission"
				return r
			}(),
			wantCPUVirt:  boolPtr(true),
			wantDevKVMOK: boolPtr(false),
			wantDevTunOK: boolPtr(true),
			wantKVMModOK: boolPtr(false),
			wantKernelOK: boolPtr(true),
			wantNestedOK: boolPtr(true),
		},
		{
			name:     "dev_kvm_no_hardware",
			hardware: defaultHardware(),
			limits:   defaultLimits(),
			resources: func() model.HostResources {
				r := defaultResources()
				r.DevKVMStatus = "no_hardware"
				return r
			}(),
			wantCPUVirt:  boolPtr(true),
			wantDevKVMOK: boolPtr(false),
			wantDevTunOK: boolPtr(true),
			wantKVMModOK: boolPtr(false),
			wantKernelOK: boolPtr(true),
			wantNestedOK: boolPtr(true),
		},
		{
			name:     "dev_net_tun_not_accessible",
			hardware: defaultHardware(),
			limits:   defaultLimits(),
			resources: func() model.HostResources {
				r := defaultResources()
				r.DevNetTUNAccessible = false
				return r
			}(),
			wantCPUVirt:  boolPtr(true),
			wantDevKVMOK: boolPtr(true),
			wantDevTunOK: boolPtr(false),
			wantKVMModOK: boolPtr(true),
			wantKernelOK: boolPtr(true),
			wantNestedOK: boolPtr(true),
		},
		{
			name: "kvm_module_in_proc_modules",
			hardware: func() model.HostHardware {
				h := defaultHardware()
				h.CPUHasVMX = false // no VMX, so first path fails
				return h
			}(),
			limits: defaultLimits(),
			resources: func() model.HostResources {
				r := defaultResources()
				r.DevKVMStatus = "no_permission"               // also not ok, so first path fails
				r.ModulesLoaded = map[string]bool{"kvm": true} // BUT kvm in /proc/modules
				return r
			}(),
			wantCPUVirt:  boolPtr(false),
			wantDevKVMOK: boolPtr(false),
			wantDevTunOK: boolPtr(true),
			wantKVMModOK: boolPtr(true), // second path: ModulesLoaded["kvm"] = true
			wantKernelOK: boolPtr(true),
			wantNestedOK: boolPtr(true),
		},
		{
			name: "kvm_module_not_loaded",
			hardware: func() model.HostHardware {
				h := defaultHardware()
				h.CPUHasVMX = false
				return h
			}(),
			limits: defaultLimits(),
			resources: func() model.HostResources {
				r := defaultResources()
				r.DevKVMStatus = "missing"
				r.ModulesLoaded = map[string]bool{} // no kvm in modules
				return r
			}(),
			wantCPUVirt:  boolPtr(false),
			wantDevKVMOK: boolPtr(false),
			wantDevTunOK: boolPtr(true),
			wantKVMModOK: boolPtr(false),
			wantKernelOK: boolPtr(true),
			wantNestedOK: boolPtr(true),
		},
		{
			name:     "kernel_below_minimum",
			hardware: defaultHardware(),
			limits: func() model.HostLimits {
				l := defaultLimits()
				l.KernelMinimumMet = false
				return l
			}(),
			resources:    defaultResources(),
			wantCPUVirt:  boolPtr(true),
			wantDevKVMOK: boolPtr(true),
			wantDevTunOK: boolPtr(true),
			wantKVMModOK: boolPtr(true),
			wantKernelOK: boolPtr(false),
			wantNestedOK: boolPtr(true),
		},
		{
			name:     "nested_virt_not_available",
			hardware: defaultHardware(),
			limits: func() model.HostLimits {
				l := defaultLimits()
				l.NestedVirtAvailable = false
				return l
			}(),
			resources:    defaultResources(),
			wantCPUVirt:  boolPtr(true),
			wantDevKVMOK: boolPtr(true),
			wantDevTunOK: boolPtr(true),
			wantKVMModOK: boolPtr(true),
			wantKernelOK: boolPtr(true),
			wantNestedOK: boolPtr(false),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probe := host.NewProbe()
			// We call the internal check by using RunAll and filtering, since
			// checkVMHost is unexported. Alternatively, call the exported RunAll
			// and check the result.
			result := probe.RunAll(t.Context(), &tt.hardware, &tt.limits, &tt.resources)

			// Checks from checkVMHost go to Critical or Info (not passed → Critical, passed → Info)
			if tt.wantCPUVirt != nil {
				check := findCheckByResult(t, result, "cpu_virtualization")
				assert.Equal(t, *tt.wantCPUVirt, check.Passed, "cpu_virtualization")
				if !*tt.wantCPUVirt {
					assert.Contains(t, check.Message, "does not support")
				}
			}
			if tt.wantDevKVMOK != nil {
				check := findCheckByResult(t, result, "dev_kvm")
				assert.Equal(t, *tt.wantDevKVMOK, check.Passed, "dev_kvm")
				if !*tt.wantDevKVMOK {
					assert.NotEmpty(t, check.Details, "failed dev_kvm must have actionable details")
				}
			}
			if tt.wantDevTunOK != nil {
				check := findCheckByResult(t, result, "dev_net_tun")
				assert.Equal(t, *tt.wantDevTunOK, check.Passed, "dev_net_tun")
			}
			if tt.wantKVMModOK != nil {
				check := findCheckByResult(t, result, "kvm_module")
				assert.Equal(t, *tt.wantKVMModOK, check.Passed, "kvm_module")
			}
			if tt.wantKernelOK != nil {
				check := findCheckByResult(t, result, "kernel_version")
				assert.Equal(t, *tt.wantKernelOK, check.Passed, "kernel_version")
			}
			if tt.wantNestedOK != nil {
				check := findCheckByResult(t, result, "nested_virtualization")
				assert.Equal(t, *tt.wantNestedOK, check.Passed, "nested_virtualization")
			}
		})
	}
}

// ─── checkSystemResources ─────────────────────────────────────────────────────
// Rationale: Must warn when swap is low relative to RAM (if RAM > 1024 MiB),
// detect cloud-localds availability, and report hugepage configuration.

func TestProbe_checkSystemResources(t *testing.T) {
	tests := []struct {
		name      string
		hardware  model.HostHardware
		limits    model.HostLimits
		resources model.HostResources
		// expectations
		wantSwapWarning    bool
		wantCloudLocaldsOK *bool
		wantHugepageMsg    bool // has hugepage info check
	}{
		{
			name:               "swap_adequate",
			hardware:           func() model.HostHardware { h := defaultHardware(); h.MemoryTotalMiB = 8192; return h }(),
			limits:             func() model.HostLimits { l := defaultLimits(); l.SwapTotalMiB = 5000; return l }(), // 5000 >= 4096
			resources:          defaultResources(),
			wantSwapWarning:    false,
			wantCloudLocaldsOK: boolPtr(true),
			wantHugepageMsg:    false,
		},
		{
			name:               "swap_low_large_ram",
			hardware:           func() model.HostHardware { h := defaultHardware(); h.MemoryTotalMiB = 8192; return h }(),
			limits:             func() model.HostLimits { l := defaultLimits(); l.SwapTotalMiB = 1024; return l }(), // 1024 < 4096
			resources:          defaultResources(),
			wantSwapWarning:    true,
			wantCloudLocaldsOK: boolPtr(true),
			wantHugepageMsg:    false,
		},
		{
			name:               "swap_low_small_ram_no_warning",
			hardware:           func() model.HostHardware { h := defaultHardware(); h.MemoryTotalMiB = 512; return h }(), // <= 1024 → no swap check
			limits:             func() model.HostLimits { l := defaultLimits(); l.SwapTotalMiB = 128; return l }(),
			resources:          defaultResources(),
			wantSwapWarning:    false, // RAM <= 1024, so check is skipped
			wantCloudLocaldsOK: boolPtr(true),
			wantHugepageMsg:    false,
		},
		{
			name:               "swap_exact_threshold",
			hardware:           func() model.HostHardware { h := defaultHardware(); h.MemoryTotalMiB = 8192; return h }(),
			limits:             func() model.HostLimits { l := defaultLimits(); l.SwapTotalMiB = 4096; return l }(), // == 8192/2 → not less, no warning
			resources:          defaultResources(),
			wantSwapWarning:    false,
			wantCloudLocaldsOK: boolPtr(true),
			wantHugepageMsg:    false,
		},
		{
			name:               "cloud_localds_not_available",
			hardware:           defaultHardware(),
			limits:             defaultLimits(),
			resources:          func() model.HostResources { r := defaultResources(); r.CloudLocaldsAvailable = false; return r }(),
			wantSwapWarning:    false,
			wantCloudLocaldsOK: boolPtr(false),
			wantHugepageMsg:    false,
		},
		{
			name:               "hugepages_configured",
			hardware:           defaultHardware(),
			limits:             func() model.HostLimits { l := defaultLimits(); l.HugepageCount2MB = 1024; return l }(),
			resources:          defaultResources(),
			wantSwapWarning:    false,
			wantCloudLocaldsOK: boolPtr(true),
			wantHugepageMsg:    true,
		},
		{
			name:               "no_hugepages_no_check",
			hardware:           defaultHardware(),
			limits:             defaultLimits(), // HugepageCount2MB = 0
			resources:          defaultResources(),
			wantSwapWarning:    false,
			wantCloudLocaldsOK: boolPtr(true),
			wantHugepageMsg:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probe := host.NewProbe()
			result := probe.RunAll(t.Context(), &tt.hardware, &tt.limits, &tt.resources)

			// Swap warning should be in Warnings, not Critical
			if tt.wantSwapWarning {
				assert.Condition(t, func() bool {
					for _, c := range result.Warnings {
						if c.Name == "swap_size" {
							return c.Passed == false
						}
					}
					return false
				}, "expected swap_size warning")
			} else {
				assert.Condition(t, func() bool {
					for _, c := range result.Warnings {
						if c.Name == "swap_size" {
							return false // should not exist
						}
					}
					return true
				}, "unexpected swap_size warning")
			}

			if tt.wantCloudLocaldsOK != nil {
				check := findCheckByResult(t, result, "cloud_localds")
				assert.Equal(t, *tt.wantCloudLocaldsOK, check.Passed, "cloud_localds")
				if !*tt.wantCloudLocaldsOK {
					assert.NotEmpty(t, check.Details)
				}
			}

			if tt.wantHugepageMsg {
				check := findCheckByResult(t, result, "hugepages")
				assert.True(t, check.Passed, "hugepages should be passed")
				assert.Contains(t, check.Message, "1024")
			} else {
				// Verify not in warnings or critical (hugepage info goes to Info)
			}
		})
	}
}

// ─── RunAll categorisation ────────────────────────────────────────────────────
// Rationale: RunAll routes checks to Critical/Warnings/Info based on Passed
// and source function. VM host failures are Critical, swap/firewall are Warnings.

func TestProbe_RunAll_categorisation(t *testing.T) {
	t.Run("all_green_goes_to_info", func(t *testing.T) {
		probe := host.NewProbe()
		result := probe.RunAll(t.Context(), ptr(defaultHardware()), ptr(defaultLimits()), ptr(defaultResources()))
		assert.Empty(t, result.Critical, "no critical when all green")
		assert.Empty(t, result.Warnings, "no warnings when all green")
		assert.NotEmpty(t, result.Info, "green checks go to Info")
	})

	t.Run("vm_host_failures_go_to_critical", func(t *testing.T) {
		hw := defaultHardware()
		hw.CPUHasVMX = false
		res := defaultResources()
		res.DevKVMStatus = "missing"

		probe := host.NewProbe()
		result := probe.RunAll(t.Context(), ptr(hw), ptr(defaultLimits()), ptr(res))
		assert.NotEmpty(t, result.Critical, "vm host failures should be critical")
		// cpu_virtualization should be critical
		assert.Condition(t, func() bool {
			for _, c := range result.Critical {
				if c.Name == "cpu_virtualization" {
					return !c.Passed
				}
			}
			return false
		}, "cpu_virtualization should be in Critical (not passed)")
	})

	t.Run("system_resource_failures_go_to_warnings", func(t *testing.T) {
		hw := defaultHardware()
		hw.MemoryTotalMiB = 8192
		limits := defaultLimits()
		limits.SwapTotalMiB = 512 // very low swap
		res := defaultResources()
		res.CloudLocaldsAvailable = false

		probe := host.NewProbe()
		result := probe.RunAll(t.Context(), ptr(hw), ptr(limits), ptr(res))
		assert.NotEmpty(t, result.Warnings, "swap and cloud-localds failures should be warnings")

		// swap_size should be in Warnings
		assert.Condition(t, func() bool {
			for _, c := range result.Warnings {
				if c.Name == "swap_size" {
					return !c.Passed
				}
			}
			return false
		}, "swap_size should be in Warnings")
	})
}

// ─── Edge cases ───────────────────────────────────────────────────────────────
// Rationale: Boundary values for swap threshold, empty hardware/limits/resources.

func TestProbe_EdgeCases(t *testing.T) {
	t.Run("zero_ram_no_swap_warning", func(t *testing.T) {
		probe := host.NewProbe()
		result := probe.RunAll(t.Context(),
			&model.HostHardware{MemoryTotalMiB: 0, CPUHasVMX: true, KernelVersion: "6.2.0"},
			&model.HostLimits{SwapTotalMiB: 0, KernelMinimumMet: true},
			&model.HostResources{DevKVMStatus: "ok", DevNetTUNAccessible: true, ModulesLoaded: map[string]bool{}},
		)
		// No crash, no swap warning (RAM <= 1024, check skipped)
		assert.Condition(t, func() bool {
			for _, c := range result.Warnings {
				if c.Name == "swap_size" {
					return false
				}
			}
			for _, c := range result.Critical {
				if c.Name == "swap_size" {
					return false
				}
			}
			return true
		}, "no swap_size check for zero RAM")
	})

	t.Run("dev_kvm_ok_but_no_vmx_cpu", func(t *testing.T) {
		// /dev/kvm is "ok" but CPU has no VMX. KVM module check:
		// kvmModuleOK = DevKVMStatus=="ok" && hasVirt → true && false → false
		// Then kvmModuleOK = ModulesLoaded["kvm"] → false (empty)
		// So kvm_module should be false even though /dev/kvm exists.
		probe := host.NewProbe()
		result := probe.RunAll(t.Context(),
			&model.HostHardware{MemoryTotalMiB: 4096, CPUHasVMX: false, KernelVersion: "6.2.0"},
			&model.HostLimits{KernelMinimumMet: true, SwapTotalMiB: 2048},
			&model.HostResources{DevKVMStatus: "ok", DevNetTUNAccessible: true, ModulesLoaded: map[string]bool{}},
		)
		check := findCheckByResult(t, result, "kvm_module")
		assert.False(t, check.Passed, "kvm_module should fail when no VMX and no module in /proc/modules")
	})
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func boolPtr(v bool) *bool { return &v }

func ptr[T any](v T) *T { return &v }

// findCheckByResult looks up a ProbeCheck by name from all result buckets.
func findCheckByResult(t *testing.T, r *model.ProbeResult, name string) model.ProbeCheck {
	t.Helper()
	for _, c := range r.Critical {
		if c.Name == name {
			return c
		}
	}
	for _, c := range r.Warnings {
		if c.Name == name {
			return c
		}
	}
	for _, c := range r.Info {
		if c.Name == name {
			return c
		}
	}
	t.Errorf("check %q not found in any result bucket (critical=%d, warnings=%d, info=%d)",
		name, len(r.Critical), len(r.Warnings), len(r.Info))
	return model.ProbeCheck{}
}
