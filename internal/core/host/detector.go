package host

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/model"
	"mvmctl/internal/infra/system"
	"mvmctl/internal/infra/validators"
)

// ── CPU vendor maps ──
// Matches Python's _CPU_VENDOR_MAP_X86 and _CPU_IMPLEMENTER_MAP_AARCH64.
var cpuVendorMapX86 = map[string]string{
	"GenuineIntel": "intel",
	"AuthenticAMD": "amd",
}

var cpuImplementerMapAarch64 = map[string]string{
	"0x41": "arm",
	"0x42": "broadcom",
	"0x43": "cavium",
	"0x44": "dec",
	"0x4e": "nvidia",
	"0x51": "qualcomm",
	"0x53": "samsung",
	"0x56": "marvell",
	"0x61": "apple",
	"0x66": "faraday",
	"0x69": "intel",
}

// Per-VM resource overhead estimates (MiB)
const (
	vmOverheadMiB    = 50
	vmMemoryMiB      = 512
	vmReservedMiB    = 2048
	vmReservedPIDs   = 200
	vmPIDsPerVM      = 3
	vmConntrackPerVM = 64
)

// VM host kernel modules to check against /proc/modules — matching Python.
var vmHostKernelModules = []string{
	"kvm",
	"kvm_intel",
	"kvm_amd",
	"tun",
	"bridge",
	"vhost_vsock",
	"nft_chain_nat",
}

// readMeminfo reads /proc/meminfo once and returns all key->value KiB mappings.
// Replaces repeated individual reads for efficiency — /proc/meminfo is a single
// syscall for all fields instead of one per field.
func readMeminfo() map[string]int {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil
	}
	result := make(map[string]int)
	for line := range strings.SplitSeq(string(data), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) == 0 {
			continue
		}
		val, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		result[parts[0]] = val
	}
	return result
}

// ── DetectHardware ──
// Matches Python's HostDetector.detect_hardware().
func DetectHardware() (*model.HostHardware, error) {
	hostname := system.Hostname()

	cpuModel := ""
	cpuVendor := ""
	cpuArchitecture := getUnameM()
	cpuHasVMX := false
	cpuHypervisor := false

	// Read /proc/cpuinfo once and reuse for all parsing
	cpuinfoData, cpuinfoErr := os.ReadFile("/proc/cpuinfo")
	_ = cpuinfoErr // file not found handled by nil check below
	if cpuinfoData != nil {
		lines := strings.Split(string(cpuinfoData), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "model name") && cpuModel == "" {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					cpuModel = strings.TrimSpace(parts[1])
				}
			}
			if strings.HasPrefix(line, "vendor_id") && cpuVendor == "" {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					raw := strings.TrimSpace(parts[1])
					if v, ok := cpuVendorMapX86[raw]; ok {
						cpuVendor = v
					} else {
						cpuVendor = raw
					}
				}
			}
			if cpuModel == "" && strings.HasPrefix(cpuArchitecture, "arm") {
				if strings.HasPrefix(line, "CPU part") && cpuModel == "" {
					parts := strings.SplitN(line, ":", 2)
					if len(parts) == 2 {
						cpuModel = strings.TrimSpace(parts[1])
					}
				}
			}
			if cpuModel != "" && cpuVendor != "" {
				break
			}
		}
		// Detect virtualization flags
		for _, line := range lines {
			if strings.HasPrefix(line, "flags") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					for f := range strings.FieldsSeq(parts[1]) {
						if f == "vmx" || f == "svm" {
							cpuHasVMX = true
						}
						if f == "hypervisor" {
							cpuHypervisor = true
						}
					}
				}
				break
			}
		}
	}

	// Fallback vendor detection for aarch64 — reuses cpuinfoData from above
	if cpuVendor == "" && (strings.HasPrefix(cpuArchitecture, "arm") || strings.HasPrefix(cpuArchitecture, "aarch64")) {
		if cpuinfoData != nil {
			for line := range strings.SplitSeq(string(cpuinfoData), "\n") {
				if strings.HasPrefix(line, "CPU implementer") {
					parts := strings.SplitN(line, ":", 2)
					if len(parts) == 2 {
						impl := strings.TrimSpace(parts[1])
						if v, ok := cpuImplementerMapAarch64[impl]; ok {
							cpuVendor = v
						} else {
							cpuVendor = impl
						}
					}
					break
				}
			}
		}
	}

	cpuCores := runtime.NumCPU()

	// NUMA nodes
	numaNodes := 1
	entries, err := os.ReadDir("/sys/devices/system/node")
	if err == nil {
		count := 0
		for _, e := range entries {
			if e.IsDir() && strings.HasPrefix(e.Name(), "node") {
				count++
			}
		}
		if count > 0 {
			numaNodes = count
		}
	}

	meminfo := readMeminfo()
	memoryTotalMiB := meminfo["MemTotal"] / 1024

	// Storage: use root cache dir (fallback to / if needed)
	cacheDir, err := infra.GetCacheDir()
	if err != nil {
		return nil, err
	}
	storageTotalBytes := 0
	var diskUsage syscall.Statfs_t
	err = syscall.Statfs(cacheDir, &diskUsage)
	if err == nil {
		storageTotalBytes = int(diskUsage.Blocks) * int(diskUsage.Bsize)
	}

	kernelVersion := system.KernelRelease()

	// OS release from /etc/os-release
	osRelease := ""
	data2, err := os.ReadFile("/etc/os-release")
	if err == nil {
		for line := range strings.SplitSeq(string(data2), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					osRelease = strings.Trim(strings.TrimSpace(parts[1]), "\"")
				}
				break
			}
		}
		if osRelease == "" {
			osID := ""
			osVersion := ""
			for line := range strings.SplitSeq(string(data2), "\n") {
				if strings.HasPrefix(line, "ID=") {
					parts := strings.SplitN(line, "=", 2)
					if len(parts) == 2 {
						osID = strings.Trim(strings.TrimSpace(parts[1]), "\"")
					}
				}
				if strings.HasPrefix(line, "VERSION_ID=") {
					parts := strings.SplitN(line, "=", 2)
					if len(parts) == 2 {
						osVersion = strings.Trim(strings.TrimSpace(parts[1]), "\"")
					}
				}
			}
			if osID != "" {
				osRelease = strings.TrimSpace(fmt.Sprintf("%s %s", osID, osVersion))
			}
		}
	}

	if cpuModel == "" {
		cpuModel = cpuArchitecture
	}
	if cpuVendor == "" {
		cpuVendor = "unknown"
	}
	if osRelease == "" {
		osRelease = "unknown"
	}

	return &model.HostHardware{
		Hostname:          hostname,
		CPUModel:          cpuModel,
		CPUVendor:         cpuVendor,
		CPUCores:          cpuCores,
		CPUArchitecture:   cpuArchitecture,
		NumaNodes:         numaNodes,
		MemoryTotalMiB:    memoryTotalMiB,
		StorageTotalBytes: storageTotalBytes,
		KernelVersion:     kernelVersion,
		OSRelease:         osRelease,
		CPUHasVMX:         cpuHasVMX,
		CPUHypervisor:     cpuHypervisor,
	}, nil
}

// ── DetectLimits ──
func DetectLimits() *model.HostLimits {
	pidMax := infra.ReadInt("/proc/sys/kernel/pid_max", 32768)
	fdMax := infra.ReadInt("/proc/sys/fs/file-max", 100000)
	conntrackMax := infra.ReadInt("/proc/sys/net/netfilter/nf_conntrack_max", 0)

	tapDevicesMax := infra.ReadInt("/sys/module/tun/parameters/max_tap_devices", 0)
	// 0 means unlimited (kernel default when module param not set)
	if tapDevicesMax == 0 {
		tapDevicesMax = -1
	}

	ipLocalPortRange := [2]int{infra.DefaultIPLocalPortRangeStart, infra.DefaultIPLocalPortRangeEnd}
	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err == nil {
		parts := strings.Fields(strings.TrimSpace(string(data)))
		if len(parts) >= 2 {
			low, err1 := strconv.Atoi(parts[0])
			high, err2 := strconv.Atoi(parts[1])
			if err1 == nil && err2 == nil {
				ipLocalPortRange = [2]int{low, high}
			}
		}
	}

	// Nested virtualization
	nestedVirtAvailable := false
	for _, nestedPath := range []string{
		"/sys/module/kvm_intel/parameters/nested",
		"/sys/module/kvm_amd/parameters/nested",
	} {
		val := infra.ReadInt(nestedPath, -1)
		if val == 1 {
			nestedVirtAvailable = true
			break
		}
	}

	// EPT
	eptAvailable := infra.ReadInt("/sys/module/kvm_intel/parameters/ept", 0) == 1

	// Hugepages 2MB
	hugepageCount2MB := infra.ReadInt("/sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages", 0)

	// KSM
	ksmRun := infra.ReadInt("/sys/kernel/mm/ksm/run", 0)
	ksmDisabled := ksmRun == 0

	// Cgroup version
	cgroupVersion := 1
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err == nil {
		cgroupVersion = 2
	}

	// Swap total
	meminfoDl := readMeminfo()
	swapTotalMiB := meminfoDl["SwapTotal"] / 1024

	// Kernel minimum version
	kernelMinimumMet := checkKernelVersion()

	return &model.HostLimits{
		PIDMax:              pidMax,
		FDMax:               fdMax,
		ConntrackMax:        conntrackMax,
		TAPDevicesMax:       tapDevicesMax,
		IPLocalPortRange:    ipLocalPortRange,
		NestedVirtAvailable: nestedVirtAvailable,
		EPTAvailable:        eptAvailable,
		HugepageCount2MB:    hugepageCount2MB,
		KSMDisabled:         ksmDisabled,
		CgroupVersion:       cgroupVersion,
		SwapTotalMiB:        swapTotalMiB,
		KernelMinimumMet:    kernelMinimumMet,
	}
}

var kernelVersionRE = regexp.MustCompile(`(\d+)\.(\d+)`)

// ── checkKernelVersion ──
// Matches Python's HostDetector._check_kernel_version().
func checkKernelVersion() bool {
	release := system.KernelRelease()
	re := kernelVersionRE
	match := re.FindStringSubmatch(release)
	if match == nil {
		return false
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	return (major > infra.MinKernelMajor) || (major == infra.MinKernelMajor && minor >= infra.MinKernelMinor)
}

// ── parseModules ──
func parseModules() map[string]bool {
	result := make(map[string]bool)
	data, err := os.ReadFile("/proc/modules")
	if err != nil {
		return result
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) > 0 {
			result[parts[0]] = true
		}
	}
	return result
}

// ── DetectResources ──
func DetectResources(
	ctx context.Context,
	hardware *model.HostHardware,
	limits *model.HostLimits,
	vmDirPath string,
) (*model.HostResources, error) {
	meminfo := readMeminfo()
	memoryAvailableMiB := meminfo["MemAvailable"] / 1024

	// TAP devices in use
	tapDevicesUsed := 0
	entries, err := os.ReadDir("/sys/class/net")
	if err == nil {
		for _, entry := range entries {
			tunFlagsPath := filepath.Join("/sys/class/net", entry.Name(), "tun_flags")
			if _, err := os.Stat(tunFlagsPath); err == nil {
				tapDevicesUsed++
			}
		}
	}

	// PIDs
	pidsCurrent := 0
	procEntries, err := os.ReadDir("/proc")
	if err == nil {
		for _, entry := range procEntries {
			if entry.IsDir() && validators.IsDigits(entry.Name()) {
				pidsCurrent++
			}
		}
	}

	// FD current
	fdCurrent := infra.ReadInt("/proc/sys/fs/file-nr", 0)

	// Conntrack current
	conntrackCurrent := infra.ReadInt("/proc/sys/net/netfilter/nf_conntrack_count", 0)

	// ARP entries
	arpCurrent := 0
	data, err := os.ReadFile("/proc/net/arp")
	if err == nil {
		arpCount := 0
		for l := range strings.SplitSeq(string(data), "\n") {
			if strings.TrimSpace(l) != "" {
				arpCount++
			}
		}
		arpCurrent = max(arpCount-1, 0) // -1 for header
	}

	// Storage free
	storageFreeBytes := 0
	var stat syscall.Statfs_t
	if err := syscall.Statfs(vmDirPath, &stat); err == nil {
		storageFreeBytes = int(stat.Bavail) * int(stat.Bsize)
	}

	// Compute recommended max VMs
	type candidate struct {
		name string
		val  int
	}
	var candidates []candidate

	// CPU: leave 1 core for host
	cpuVMs := max(hardware.CPUCores-1, 0)
	candidates = append(candidates, candidate{"cpu", cpuVMs})

	// Memory
	memoryVMs := max((memoryAvailableMiB-vmReservedMiB)/(vmOverheadMiB+vmMemoryMiB), 0)
	candidates = append(candidates, candidate{"memory", memoryVMs})

	// TAP devices
	tapAvailable := max(limits.TAPDevicesMax-tapDevicesUsed, 0)
	if limits.TAPDevicesMax > 0 {
		candidates = append(candidates, candidate{"tap_devices", tapAvailable})
	}

	// PIDs
	pidVMs := max((limits.PIDMax-vmReservedPIDs)/vmPIDsPerVM, 0)
	candidates = append(candidates, candidate{"pids", pidVMs})

	// Conntrack
	if limits.ConntrackMax > 0 {
		conntrackVMs := limits.ConntrackMax / vmConntrackPerVM
		candidates = append(candidates, candidate{"conntrack", conntrackVMs})
	}

	// Find minimum
	recommendedMaxVMs := 0
	limitingResource := ""
	if len(candidates) > 0 {
		recommendedMaxVMs = candidates[0].val
		limitingResource = candidates[0].name
		for _, c := range candidates[1:] {
			if c.val < recommendedMaxVMs {
				recommendedMaxVMs = c.val
				limitingResource = c.name
			}
		}
	}

	// Module detection
	modules := parseModules()
	modulesLoaded := make(map[string]bool)
	for _, m := range vmHostKernelModules {
		_, loaded := modules[m]
		modulesLoaded[m] = loaded
	}

	// Swap used
	swapFreeMiB := meminfo["SwapFree"] / 1024
	swapTotalMiB := meminfo["SwapTotal"] / 1024
	swapUsedMiB := max(swapTotalMiB-swapFreeMiB, 0)

	// Hugepages free
	hugepagesFree2MB := infra.ReadInt("/sys/kernel/mm/hugepages/hugepages-2048kB/free_hugepages", 0)

	// SMT (Hyper-Threading) — matching Python's bool(_read_int(...)) which treats any non-zero as true
	smtActive := infra.ReadInt("/sys/devices/system/cpu/smt/active", 0) != 0

	// Binary availability
	_, nftLookupErr := exec.LookPath("nft")
	nftablesAvailable := nftLookupErr == nil
	_, iptLookupErr := exec.LookPath("iptables")
	iptablesAvailable := iptLookupErr == nil
	_, clLookupErr := exec.LookPath("cloud-localds")
	cloudLocaldsAvailable := clLookupErr == nil

	// /dev/kvm status
	kvmPath := "/dev/kvm"
	devKVMStatus := ""
	if _, err := os.Stat(kvmPath); os.IsNotExist(err) {
		devKVMStatus = "missing"
	} else if !system.AccessRW(kvmPath) {
		devKVMStatus = "no_permission"
	} else if !hardware.CPUHasVMX {
		devKVMStatus = "no_hardware"
	} else {
		devKVMStatus = "ok"
	}

	// User in kvm group
	// Matches Python: grp.getgrnam("kvm") + pwd.getpwuid(os.getuid()).pw_name + user in g.gr_mem
	userInKVMGroup := false
	if g, err := user.LookupGroup("kvm"); err == nil {
		currentUser, err := user.Current()
		if err == nil {
			// Check group members via NSS (getent)
			members, parseErr := system.GroupMembersViaNSS(ctx, "kvm")
			if parseErr == nil {
				userInKVMGroup = slices.Contains(members, currentUser.Username)
			}
			_ = g // gid used for primary group check
		}
	}

	// /dev/net/tun accessibility
	tunPath := "/dev/net/tun"
	devNetTUNAccessible := system.AccessRW(tunPath)

	var limResPtr *string
	if limitingResource != "" {
		limResPtr = &limitingResource
	}

	return &model.HostResources{
		MemoryAvailableMiB:    memoryAvailableMiB,
		TAPDevicesUsed:        tapDevicesUsed,
		PIDsCurrent:           pidsCurrent,
		FDCurrent:             fdCurrent,
		ConntrackCurrent:      conntrackCurrent,
		ARPCurrent:            arpCurrent,
		StorageFreeBytes:      storageFreeBytes,
		RecommendedMaxVMs:     recommendedMaxVMs,
		LimitingResource:      limResPtr,
		ModulesLoaded:         modulesLoaded,
		SwapUsedMiB:           swapUsedMiB,
		HugepagesFree2MB:      hugepagesFree2MB,
		SMTActive:             smtActive,
		NftablesAvailable:     nftablesAvailable,
		IptablesAvailable:     iptablesAvailable,
		CloudLocaldsAvailable: cloudLocaldsAvailable,
		DevKVMStatus:          devKVMStatus,
		UserInKVMGroup:        userInKVMGroup,
		DevNetTUNAccessible:   devNetTUNAccessible,
	}, nil
}

// getUnameM returns the machine hardware name, matching Python's platform.machine().
// Python's platform.machine() calls uname -m internally, returning values like
// "x86_64" or "aarch64". We use the syscall.Uname syscall to get the real machine name.
func getUnameM() string {
	var uname syscall.Utsname
	if err := syscall.Uname(&uname); err != nil {
		// Fallback to runtime.GOARCH mapping on error
		switch runtime.GOARCH {
		case "amd64":
			return "x86_64"
		case "arm64":
			return "aarch64"
		default:
			return runtime.GOARCH
		}
	}
	// Convert [65]int8 to string, stopping at null byte.
	b := make([]byte, 0, len(uname.Machine))
	for _, c := range uname.Machine {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	machine := string(b)
	if machine != "" {
		return machine
	}
	return "x86_64"
}
