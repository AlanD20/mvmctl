package host

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
)

// ── Probe ──
// Matches Python's HostProbe class — pre-flight checks for host readiness.
type Probe struct{}

func NewProbe() *Probe {
	return &Probe{}
}

// RunAll runs all pre-flight probes and returns aggregated result.
// Matches Python's HostProbe.run_all().
//
// Takes detection results as input instead of re-reading system files —
// detector.go is the single source of truth for all /proc data.
func (p *Probe) RunAll(hardware *model.HostHardware, limits *model.HostLimits, resources *model.HostResources) *model.ProbeResult {
	result := &model.ProbeResult{}

	for _, check := range p.checkVMHost(hardware, limits, resources) {
		if !check.Passed {
			result.Critical = append(result.Critical, check)
		} else {
			result.Info = append(result.Info, check)
		}
	}

	for _, check := range p.checkInitBinaries() {
		if !check.Passed {
			result.Critical = append(result.Critical, check)
		} else {
			result.Info = append(result.Info, check)
		}
	}

	for _, check := range p.checkFirewallReadiness(resources) {
		if !check.Passed {
			result.Warnings = append(result.Warnings, check)
		} else {
			result.Info = append(result.Info, check)
		}
	}

	for _, check := range p.checkSystemResources(hardware, limits, resources) {
		if !check.Passed {
			result.Warnings = append(result.Warnings, check)
		} else {
			result.Info = append(result.Info, check)
		}
	}

	return result
}

// checkVMHost checks KVM and VM host prerequisites using pre-detected data.
// Matches Python's HostProbe.check_vm_host() exactly.
// No file I/O — all data comes from detector.go models.
func (p *Probe) checkVMHost(hardware *model.HostHardware, limits *model.HostLimits, resources *model.HostResources) []model.ProbeCheck {
	var checks []model.ProbeCheck

	// --- CPU virtualization support (VMX/SVM) ---
	hasVirt := hardware.CPUHasVMX
	msg := "CPU virtualization extensions (VMX/SVM)"
	var details string
	if !hasVirt {
		msg = "CPU does not support hardware virtualization (VMX/SVM)"
		details = "Enable VT-x/AMD-V in BIOS. Without it, VMs will be extremely slow."
	}
	checks = append(checks, model.ProbeCheck{
		Name:    "cpu_virtualization",
		Passed:  hasVirt,
		Message: msg,
		Details: details,
	})

	// --- /dev/kvm ---
	switch resources.DevKVMStatus {
	case "missing":
		checks = append(checks, model.ProbeCheck{
			Name:    "dev_kvm",
			Passed:  false,
			Message: "/dev/kvm does not exist",
			Details: "KVM kernel module not loaded. Run: sudo modprobe kvm && sudo modprobe kvm_intel (or kvm_amd)",
		})
	case "no_permission":
		checks = append(checks, model.ProbeCheck{
			Name:    "dev_kvm",
			Passed:  false,
			Message: "/dev/kvm exists but is not readable/writable",
			Details: "Add user to kvm group: sudo usermod -aG kvm $USER && newgrp kvm",
		})
	case "no_hardware":
		checks = append(checks, model.ProbeCheck{
			Name:    "dev_kvm",
			Passed:  false,
			Message: "/dev/kvm exists but no CPU virtualization support detected",
			Details: "CPU may not support virtualization, or KVM is built into the kernel without /dev/kvm",
		})
	default: // "ok"
		checks = append(checks, model.ProbeCheck{
			Name:    "dev_kvm",
			Passed:  true,
			Message: "/dev/kvm is accessible",
		})
	}

	// --- /dev/net/tun ---
	tunOK := resources.DevNetTUNAccessible
	tunMsg := "/dev/net/tun is accessible"
	var tunDetails string
	if !tunOK {
		tunMsg = "/dev/net/tun is not accessible"
		tunDetails = "TUN/TAP networking will not work. Check permissions or load tun module."
	}
	checks = append(checks, model.ProbeCheck{
		Name:    "dev_net_tun",
		Passed:  tunOK,
		Message: tunMsg,
		Details: tunDetails,
	})

	// --- kvm kernel module ---
	// Two paths to pass:
	//   1. KVM built into kernel (/dev/kvm accessible, no module needed)
	//   2. KVM loadable module listed in /proc/modules
	// Matches Python behavior: Python's lsmod reads /proc/modules internally.
	// When KVM is built-in (CONFIG_KVM_INTEL=y), /proc/modules won't list it
	// but /dev/kvm is still fully functional — the probe accepts this.
	kvmModuleOK := resources.DevKVMStatus == "ok" && hasVirt
	if !kvmModuleOK {
		kvmModuleOK = resources.ModulesLoaded["kvm"]
	}

	kvmMsg := "KVM kernel module loaded"
	var kvmDetails string
	if !kvmModuleOK {
		kvmMsg = "KVM kernel module not loaded"
		kvmDetails = "Run: sudo modprobe kvm"
	}
	checks = append(checks, model.ProbeCheck{
		Name:    "kvm_module",
		Passed:  kvmModuleOK,
		Message: kvmMsg,
		Details: kvmDetails,
	})

	// --- Kernel minimum version ---
	release := hardware.KernelVersion
	kernelMet := limits.KernelMinimumMet
	kernelMsg := fmt.Sprintf("Kernel %s meets minimum 5.10", release)
	var kernelDetails string
	if !kernelMet {
		kernelMsg = fmt.Sprintf("Kernel %s is below minimum 5.10", release)
		kernelDetails = "Firecracker requires Linux kernel 5.10 or later."
	}
	checks = append(checks, model.ProbeCheck{
		Name:    "kernel_version",
		Passed:  kernelMet,
		Message: kernelMsg,
		Details: kernelDetails,
	})

	// --- Nested virtualization ---
	nestedVirt := limits.NestedVirtAvailable
	nestedMsg := "Nested virtualization supported"
	var nestedDetails string
	if !nestedVirt {
		nestedMsg = "Nested virtualization not available"
		nestedDetails = "Only needed for running VMs inside VMs. Set kvm_intel.nested=1 or kvm_amd.nested=1."
	}
	checks = append(checks, model.ProbeCheck{
		Name:    "nested_virtualization",
		Passed:  nestedVirt,
		Message: nestedMsg,
		Details: nestedDetails,
	})

	return checks
}

// checkInitBinaries checks all binaries required for host initialization.
// Matches Python's HostProbe.check_init_binaries().
func (p *Probe) checkInitBinaries() []model.ProbeCheck {
	var checks []model.ProbeCheck
	for _, name := range infra.InitBinaries {
		_, lookupErr := exec.LookPath(name)
		found := lookupErr == nil
		msg := fmt.Sprintf("Required binary '%s' found", name)
		var details string
		if !found {
			msg = fmt.Sprintf("Required binary '%s' not found", name)
			details = fmt.Sprintf("Install the package that provides '%s'", name)
		}
		checks = append(checks, model.ProbeCheck{
			Name:    "binary:" + name,
			Passed:  found,
			Message: msg,
			Details: details,
		})
	}
	return checks
}

// checkFirewallReadiness checks firewall backend availability and detect conflicts.
// Matches Python's HostProbe.check_firewall_readiness().
func (p *Probe) checkFirewallReadiness(resources *model.HostResources) []model.ProbeCheck {
	var checks []model.ProbeCheck

	nftAvailable := resources.NftablesAvailable
	iptAvailable := resources.IptablesAvailable

	msg := "nftables available"
	if !nftAvailable {
		msg = "nftables not available"
	}
	checks = append(checks, model.ProbeCheck{
		Name:    "nftables",
		Passed:  nftAvailable,
		Message: msg,
	})

	msg = "iptables available"
	if !iptAvailable {
		msg = "iptables not available"
	}
	checks = append(checks, model.ProbeCheck{
		Name:    "iptables",
		Passed:  iptAvailable,
		Message: msg,
	})

	// Mixed backend detection
	if nftAvailable && iptAvailable {
		hasConflict := detectIPTablesBackendConflict()
		if hasConflict {
			checks = append(checks, model.ProbeCheck{
				Name:    "firewall_conflict",
				Passed:  false,
				Message: "Mixed iptables backends detected",
				Details: "Both legacy and nft iptables backends are active. This may cause networking issues.",
			})
		}
	}

	return checks
}

// checkSystemResources checks system resource thresholds.
// Matches Python's HostProbe.check_system_resources().
func (p *Probe) checkSystemResources(hardware *model.HostHardware, limits *model.HostLimits, resources *model.HostResources) []model.ProbeCheck {
	var checks []model.ProbeCheck

	// Swap check
	totalMemMiB := hardware.MemoryTotalMiB
	totalSwapMiB := limits.SwapTotalMiB
	if totalSwapMiB < totalMemMiB/2 && totalMemMiB > 1024 {
		msg := fmt.Sprintf("Swap (%d MiB) is less than half of RAM (%d MiB)", totalSwapMiB, totalMemMiB)
		checks = append(checks, model.ProbeCheck{
			Name:    "swap_size",
			Passed:  false,
			Message: msg,
			Details: "Low swap may cause OOM under high VM load. Consider increasing swap.",
		})
	}

	// cloud-localds
	clAvailable := resources.CloudLocaldsAvailable
	clMsg := "cloud-localds available"
	var clDetails string
	if !clAvailable {
		clMsg = "cloud-localds not found"
		clDetails = "Install cloud-image-utils (Debian/Ubuntu) or cloud-utils (Arch)"
	}
	checks = append(checks, model.ProbeCheck{
		Name:    "cloud_localds",
		Passed:  clAvailable,
		Message: clMsg,
		Details: clDetails,
	})

	// Huge pages info
	if limits.HugepageCount2MB > 0 {
		checks = append(checks, model.ProbeCheck{
			Name:    "hugepages",
			Passed:  true,
			Message: fmt.Sprintf("%d x 2MB hugepages configured", limits.HugepageCount2MB),
		})
	}

	return checks
}

// detectIPTablesBackendConflict checks if both iptables-legacy and iptables-nft have active rules.
// Matches Python's NetworkUtils.detect_iptables_backend_conflict() exactly,
// including the initial iptables --version call.
func detectIPTablesBackendConflict() bool {
	ctx := context.Background()

	// Python: run_cmd(["iptables", "--version"], check=False)
	// This determines the current iptables backend (nft vs legacy).
	// The diagnosis string is computed but discarded by the probe caller
	// (has_conflict, _ = ...); the call itself is preserved for completeness.
	versionOpts := system.DefaultRunCmdOpts()
	versionOpts.Check = false
	versionResult := system.RunCmdCompat(ctx, []string{"iptables", "--version"}, versionOpts)
	_ = versionResult // result not used by probe, but the system call is made

	legacyActive := false
	nftActive := false

	// Check legacy: iptables-legacy -L -n -v (with privileged=True, matching Python)
	legacyOpts := system.DefaultRunCmdOpts()
	legacyOpts.Check = false
	legacyOpts.Privileged = true
	legacyResult := system.RunCmdCompat(ctx, []string{"iptables-legacy", "-L", "-n", "-v"}, legacyOpts)
	if legacyResult.ExitCode == 0 {
		for line := range strings.SplitSeq(legacyResult.Stdout, "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				if pkts, err := strconv.Atoi(parts[0]); err == nil && pkts > 0 {
					legacyActive = true
					break
				}
			}
		}
	}

	// Check nft: iptables -L -n -v (with privileged=True, matching Python)
	nftOpts := system.DefaultRunCmdOpts()
	nftOpts.Check = false
	nftOpts.Privileged = true
	nftResult := system.RunCmdCompat(ctx, []string{"iptables", "-L", "-n", "-v"}, nftOpts)
	if nftResult.ExitCode == 0 {
		for line := range strings.SplitSeq(nftResult.Stdout, "\n") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				if pkts, err := strconv.Atoi(parts[0]); err == nil && pkts > 0 {
					nftActive = true
					break
				}
			}
		}
	}

	return legacyActive && nftActive
}
