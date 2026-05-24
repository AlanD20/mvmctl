package host

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
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

func strPtr(s string) *string {
	return &s
}

// RunAll runs all pre-flight probes and returns aggregated result.
// Matches Python's HostProbe.run_all().
func (p *Probe) RunAll() *model.ProbeResult {
	result := &model.ProbeResult{}

	for _, check := range p.checkVMHost() {
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

	for _, check := range p.checkFirewallReadiness() {
		if !check.Passed {
			result.Warnings = append(result.Warnings, check)
		} else {
			result.Info = append(result.Info, check)
		}
	}

	for _, check := range p.checkSystemResources() {
		if !check.Passed {
			result.Warnings = append(result.Warnings, check)
		} else {
			result.Info = append(result.Info, check)
		}
	}

	return result
}

// checkVMHost checks KVM and VM host prerequisites.
// Matches Python's HostProbe.check_vm_host() exactly.
func (p *Probe) checkVMHost() []model.ProbeCheck {
	var checks []model.ProbeCheck

	// --- CPU virtualization support (VMX/SVM) ---
	hasVirt := false
	data, err := os.ReadFile("/proc/cpuinfo")
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "flags") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					flags := strings.Fields(parts[1])
					for _, f := range flags {
						if f == "vmx" || f == "svm" {
							hasVirt = true
							break
						}
					}
				}
				break
			}
		}
	}

	msg := "CPU virtualization extensions (VMX/SVM)"
	var details *string
	if !hasVirt {
		msg = "CPU does not support hardware virtualization (VMX/SVM)"
		details = strPtr("Enable VT-x/AMD-V in BIOS. Without it, VMs will be extremely slow.")
	}
	checks = append(checks, model.ProbeCheck{
		Name:    "cpu_virtualization",
		Passed:  hasVirt,
		Message: msg,
		Details: details,
	})

	// --- /dev/kvm ---
	kvmPath := "/dev/kvm"
	cpuVirtOK := hasVirt

	if _, err := os.Stat(kvmPath); os.IsNotExist(err) {
		checks = append(checks, model.ProbeCheck{
			Name:    "dev_kvm",
			Passed:  false,
			Message: "/dev/kvm does not exist",
			Details: strPtr("KVM kernel module not loaded. Run: sudo modprobe kvm && sudo modprobe kvm_intel (or kvm_amd)"),
		})
	} else if !system.AccessRW(kvmPath) {
		checks = append(checks, model.ProbeCheck{
			Name:    "dev_kvm",
			Passed:  false,
			Message: "/dev/kvm exists but is not readable/writable",
			Details: strPtr("Add user to kvm group: sudo usermod -aG kvm $USER && newgrp kvm"),
		})
	} else if !cpuVirtOK {
		checks = append(checks, model.ProbeCheck{
			Name:    "dev_kvm",
			Passed:  false,
			Message: "/dev/kvm exists but no CPU virtualization support detected",
			Details: strPtr("CPU may not support virtualization, or KVM is built into the kernel without /dev/kvm"),
		})
	} else {
		checks = append(checks, model.ProbeCheck{
			Name:    "dev_kvm",
			Passed:  true,
			Message: "/dev/kvm is accessible",
		})
	}

	// --- /dev/net/tun ---
	// Matches Python: tun_path.exists() and os.access(tun_path, os.R_OK | os.W_OK)
	tunPath := "/dev/net/tun"
	tunOK := system.AccessRW(tunPath)
	tunMsg := "/dev/net/tun is accessible"
	var tunDetails *string
	if !tunOK {
		tunMsg = "/dev/net/tun is not accessible"
		tunDetails = strPtr("TUN/TAP networking will not work. Check permissions or load tun module.")
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
	kvmModuleOK := system.AccessRW(kvmPath) && cpuVirtOK
	if !kvmModuleOK {
		data, err = os.ReadFile("/proc/modules")
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				parts := strings.Fields(line)
				if len(parts) > 0 && parts[0] == "kvm" {
					kvmModuleOK = true
					break
				}
			}
		}
	}

	kvmMsg := "KVM kernel module loaded"
	var kvmDetails *string
	if !kvmModuleOK {
		kvmMsg = "KVM kernel module not loaded"
		kvmDetails = strPtr("Run: sudo modprobe kvm")
	}
	checks = append(checks, model.ProbeCheck{
		Name:    "kvm_module",
		Passed:  kvmModuleOK,
		Message: kvmMsg,
		Details: kvmDetails,
	})

	// --- Kernel minimum version ---
	release := system.KernelRelease()
	re := regexp.MustCompile(`(\d+)\.(\d+)`)
	match := re.FindStringSubmatch(release)
	kernelMet := false
	if match != nil {
		var major, minor int
		fmt.Sscanf(match[1], "%d", &major)
		fmt.Sscanf(match[2], "%d", &minor)
		kernelMet = (major > 5) || (major == 5 && minor >= 10)
	}

	kernelMsg := fmt.Sprintf("Kernel %s meets minimum 5.10", release)
	var kernelDetails *string
	if !kernelMet {
		kernelMsg = fmt.Sprintf("Kernel %s is below minimum 5.10", release)
		kernelDetails = strPtr("Firecracker requires Linux kernel 5.10 or later.")
	}
	checks = append(checks, model.ProbeCheck{
		Name:    "kernel_version",
		Passed:  kernelMet,
		Message: kernelMsg,
		Details: kernelDetails,
	})

	// --- Nested virtualization ---
	nestedVirt := false
	for _, nestedPath := range []string{
		"/sys/module/kvm_intel/parameters/nested",
		"/sys/module/kvm_amd/parameters/nested",
	} {
		data, err := os.ReadFile(nestedPath)
		if err != nil {
			continue
		}
		val := strings.TrimSpace(string(data))
		lower := strings.ToLower(val)
		if lower == "y" || lower == "1" || lower == "yes" || lower == "on" {
			nestedVirt = true
			break
		}
	}

	nestedMsg := "Nested virtualization supported"
	var nestedDetails *string
	if !nestedVirt {
		nestedMsg = "Nested virtualization not available"
		nestedDetails = strPtr("Only needed for running VMs inside VMs. Set kvm_intel.nested=1 or kvm_amd.nested=1.")
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
		var details *string
		if !found {
			msg = fmt.Sprintf("Required binary '%s' not found", name)
			details = strPtr(fmt.Sprintf("Install the package that provides '%s'", name))
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
func (p *Probe) checkFirewallReadiness() []model.ProbeCheck {
	var checks []model.ProbeCheck

	_, nftLookupErr := exec.LookPath("nft")
	nftAvailable := nftLookupErr == nil
	_, iptLookupErr := exec.LookPath("iptables")
	iptAvailable := iptLookupErr == nil

	msg := "nftables available"
	if !nftAvailable {
		msg = "nftables not available"
	}
	checks = append(checks, model.ProbeCheck{
		Name:    "nftables",
		Passed:  nftAvailable,
		Message: msg,
		Details: nil,
	})

	msg = "iptables available"
	if !iptAvailable {
		msg = "iptables not available"
	}
	checks = append(checks, model.ProbeCheck{
		Name:    "iptables",
		Passed:  iptAvailable,
		Message: msg,
		Details: nil,
	})

	// Mixed backend detection
	if nftAvailable && iptAvailable {
		hasConflict := detectIPTablesBackendConflict()
		if hasConflict {
			checks = append(checks, model.ProbeCheck{
				Name:    "firewall_conflict",
				Passed:  false,
				Message: "Mixed iptables backends detected",
				Details: strPtr("Both legacy and nft iptables backends are active. This may cause networking issues."),
			})
		}
	}

	return checks
}

// checkSystemResources checks system resource thresholds.
// Matches Python's HostProbe.check_system_resources().
func (p *Probe) checkSystemResources() []model.ProbeCheck {
	var checks []model.ProbeCheck

	// Swap check
	totalMem := 0
	totalSwap := 0
	data, err := os.ReadFile("/proc/meminfo")
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "MemTotal:") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					totalMem, _ = strconv.Atoi(parts[1])
				}
			} else if strings.HasPrefix(line, "SwapTotal:") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					totalSwap, _ = strconv.Atoi(parts[1])
				}
			}
		}
	}

	totalMemMiB := totalMem / 1024
	totalSwapMiB := totalSwap / 1024
	if totalSwapMiB < totalMemMiB/2 && totalMemMiB > 1024 {
		msg := fmt.Sprintf("Swap (%d MiB) is less than half of RAM (%d MiB)", totalSwapMiB, totalMemMiB)
		checks = append(checks, model.ProbeCheck{
			Name:    "swap_size",
			Passed:  false,
			Message: msg,
			Details: strPtr("Low swap may cause OOM under high VM load. Consider increasing swap."),
		})
	}

	// cloud-localds
	_, clLookupErr := exec.LookPath("cloud-localds")
	clAvailable := clLookupErr == nil
	clMsg := "cloud-localds available"
	var clDetails *string
	if !clAvailable {
		clMsg = "cloud-localds not found"
		clDetails = strPtr("Install cloud-image-utils (Debian/Ubuntu) or cloud-utils (Arch)")
	}
	checks = append(checks, model.ProbeCheck{
		Name:    "cloud_localds",
		Passed:  clAvailable,
		Message: clMsg,
		Details: clDetails,
	})

	// Huge pages info
	nrHugepages := 0
	data, err = os.ReadFile("/sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages")
	if err == nil {
		nrHugepages, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	if nrHugepages > 0 {
		checks = append(checks, model.ProbeCheck{
			Name:    "hugepages",
			Passed:  true,
			Message: fmt.Sprintf("%d x 2MB hugepages configured", nrHugepages),
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
		for _, line := range strings.Split(legacyResult.Stdout, "\n") {
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
		for _, line := range strings.Split(nftResult.Stdout, "\n") {
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
