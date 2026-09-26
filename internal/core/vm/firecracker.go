package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	networksvc "mvmctl/internal/service/network"
	"mvmctl/pkg/errs"
)

// --- Constants ---
const (
	constSignalExitCodeBase     = 128
	constDefaultGracefulTimeout = 30.0
	constDefaultKillTimeout     = 5.0
	defaultLibguestfsSeedDir    = "/var/lib/cloud/seed/nocloud"
)

// FirecrackerSpawner manages Firecracker process lifecycle and config generation.
type FirecrackerSpawner struct {
	config           *model.FirecrackerConfig
	configPath       string
	logPath          string
	metricsPath      string
	serialOutputPath string
	pidPath          string
	APISocketPath    string
	PID              *int
	ProcessStartTime *int64
	fcLogFP          *os.File
	serialOutputFP   *os.File
}

// NewFirecrackerSpawner creates a new FirecrackerSpawner.
func NewFirecrackerSpawner(config *model.FirecrackerConfig) *FirecrackerSpawner {
	s := &FirecrackerSpawner{
		config: config,
	}
	s.configPath = config.ConfigPath
	s.logPath = config.LogPath
	s.metricsPath = config.MetricsPath
	s.serialOutputPath = config.SerialOutputPath
	s.pidPath = config.PIDPath
	s.APISocketPath = config.APISocketPath
	return s
}

// --- Spawn ---

// Spawn starts a Firecracker process.
//
// The typed implementation lives in lifecycle.go.

// --- Cleanup ---

// Cleanup performs cleanup of all created resources.
func (s *FirecrackerSpawner) Cleanup(ctx context.Context) {
	s.CloseFilePointers()
	if err := CleanupJail(ctx, s.vmID()); err != nil {
		slog.Warn("failed to clean VM jail", "vm_id", s.vmID(), "error", err)
	}
}

// --- Generate ---

// Generate builds the Firecracker VM config.
func (s *FirecrackerSpawner) Generate() (*model.FirecrackerVMConfig, error) {
	switch s.config.NetworkPlacement {
	case model.VMNetworkPlacementManaged:
		tapName, err := networksvc.VMTAPName(uint32(os.Geteuid()), s.config.NetworkID, s.vmID())
		if err != nil {
			return nil, err
		}
		s.config.TapName = tapName
	case model.VMNetworkPlacementNetworkless:
		if s.config.NetworkID != "" || s.config.GuestIP != "" || s.config.GuestMAC != "" ||
			s.config.TapName != "" || s.config.NetworkGateway != "" || s.config.NetworkNetmask != "" {
			return nil, errs.New(errs.CodeFirecrackerConfigError,
				"networkless VM configuration cannot include a managed network endpoint")
		}
		if s.config.CloudInitMode != nil && *s.config.CloudInitMode == model.CloudInitModeNET {
			return nil, errs.New(errs.CodeFirecrackerConfigError,
				"networkless VM configuration cannot use NoCloud NET")
		}
	default:
		return nil, errs.New(errs.CodeFirecrackerConfigError, "VM network placement is invalid")
	}
	// Nested virt requires PCI — force it on
	if s.config.NestedVirt {
		if s.config.ReleaseArchitecture != "x86_64" {
			return nil, errs.New(errs.CodeFirecrackerConfigError,
				"nested virtualization requires an admitted architecture-specific CPU profile")
		}
		s.config.PCIEnabled = true
	}

	bootArgs, err := s.buildBootArgs()
	if err != nil {
		return nil, err
	}

	config := &model.FirecrackerVMConfig{
		BootSource: model.BootSourceConfig{
			KernelImagePath: s.config.KernelPath,
			BootArgs:        bootArgs,
		},
		Drives:            s.buildDrivesConfig(),
		NetworkInterfaces: s.buildNetworkConfig(),
		MachineConfig: model.MachineConfig{
			VCPUCount:       s.config.VCPUCount,
			MemSizeMiB:      s.config.MemSizeMiB,
			SMT:             false,
			TrackDirtyPages: false,
		},
	}

	if s.config.EnableLogging {
		logger := s.buildLoggerConfig()
		config.Logger = &logger
	}

	if s.config.EnableMetrics {
		metrics := s.buildMetricsConfig()
		config.Metrics = &metrics
	}

	config.CPUConfig = s.buildCPUConfig()

	if s.config.VsockGuestCID != nil {
		if !model.IsValidVsockGuestCID(*s.config.VsockGuestCID) {
			return nil, errs.New(errs.CodeFirecrackerConfigError, "vsock guest CID is outside the supported range")
		}
		config.Vsock = &model.VsockConfig{
			GuestCID: *s.config.VsockGuestCID,
			UDSPath:  filepath.Join(s.config.VMDir, infra.VMVsockSocketFilename),
		}
	}

	return config, nil
}

// --- WriteToFile ---

// WriteToFile generates and writes the config to disk.
func (s *FirecrackerSpawner) WriteToFile() error {
	config, err := s.Generate()
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.configPath)
	if err := os.MkdirAll(dir, infra.DirPerm); err != nil {
		return err
	}
	data, err := json.Marshal(translateConfigForJail(config))
	if err != nil {
		return err
	}
	return os.WriteFile(s.configPath, data, 0600)
}

// --- CloseFilePointers ---

// CloseFilePointers closes both log and serial output file pointers.
func (s *FirecrackerSpawner) CloseFilePointers() {
	if s.fcLogFP != nil {
		if err := s.fcLogFP.Close(); err != nil {
			slog.Warn("Failed to close filepointer(s)", "error", err)
		}
		s.fcLogFP = nil
	}

	if s.serialOutputFP != nil {
		if err := s.serialOutputFP.Close(); err != nil {
			slog.Warn("Failed to close filepointer(s)", "error", err)
		}
		s.serialOutputFP = nil
	}
}

// --- Internal config builders ---

// buildDrivesConfig builds the drives section of the Firecracker config.
func (s *FirecrackerSpawner) buildDrivesConfig() []model.DriveConfig {
	// Resolve rootfs path to absolute path
	rootfsAbs, err := filepath.Abs(s.config.RootfsPath)
	if err != nil {
		rootfsAbs = s.config.RootfsPath
	}
	cacheType := model.CacheTypeUnsafe
	if s.config.Writeback {
		cacheType = model.CacheTypeWriteback
	}
	drives := []model.DriveConfig{
		{
			DriveID:      "rootfs",
			PathOnHost:   rootfsAbs,
			IsRootDevice: true,
			IsReadOnly:   false,
			CacheType:    cacheType,
			IOEngine:     "Sync",
		},
	}

	// Cloud-init ISO drive (if configured) — not affected by writeback flag,
	// ISO is read-only temporary data.
	cloudInitMode := s.config.CloudInitMode
	cloudInitISOPath := s.config.CloudInitISOPath
	if cloudInitMode != nil && *cloudInitMode != "" && *cloudInitMode != model.CloudInitModeOFF &&
		cloudInitISOPath != nil &&
		*cloudInitISOPath != "" {
		// Resolve cloud-init ISO path to absolute path
		ciAbs, err := filepath.Abs(*cloudInitISOPath)
		if err != nil {
			ciAbs = *cloudInitISOPath
		}
		cloudInitDrive := model.DriveConfig{
			DriveID:      "cloud-init",
			PathOnHost:   ciAbs,
			IsRootDevice: false,
			IsReadOnly:   true,
			CacheType:    "Unsafe",
			IOEngine:     "Sync",
		}
		drives = append(drives, cloudInitDrive)
	}

	// Extra drives (volumes) — already built with correct cache type by VolumesToDrives
	drives = append(drives, s.config.ExtraDrives...)

	return drives
}

// buildLoggerConfig builds the logger section of the Firecracker config.
func (s *FirecrackerSpawner) buildLoggerConfig() model.LoggerConfig {
	return model.LoggerConfig{
		LogPath:       s.logPath,
		Level:         s.config.LogLevel,
		ShowLevel:     true,
		ShowLogOrigin: true,
	}
}

// buildMetricsConfig builds the metrics section of the Firecracker config.
func (s *FirecrackerSpawner) buildMetricsConfig() model.MetricsConfig {
	return model.MetricsConfig{
		MetricsPath: s.metricsPath,
	}
}

// buildCPUConfig builds the cpu-config section for the Firecracker config.
//
// Returns a *CpuConfig when nested virt is enabled or a custom CPU template
// was provided. Returns nil when no CPU configuration is needed.
func (s *FirecrackerSpawner) buildCPUConfig() *model.CpuConfig {
	if s.config.CPUConfig != nil {
		return s.config.CPUConfig
	}
	if s.config.NestedVirt {
		return &model.CpuConfig{KvmCapabilities: []string{}}
	}
	return nil
}

// --- Network config ---

// buildNetworkConfig builds the network-interfaces section.
func (s *FirecrackerSpawner) buildNetworkConfig() []model.NetworkInterfaceConfig {
	if s.config.NetworkPlacement == model.VMNetworkPlacementNetworkless {
		return []model.NetworkInterfaceConfig{}
	}
	networks := []model.NetworkInterfaceConfig{
		{
			IfaceID:     "eth0",
			GuestMAC:    s.config.GuestMAC,
			HostDevName: s.config.TapName,
		},
	}
	return networks
}

// bootArgsBuilder maintains an ordered list of boot argument key-value pairs.
type bootArgEntry struct {
	key    string
	values []string
}

type bootArgsBuilder struct {
	entries []bootArgEntry
}

func newBootArgsBuilder() *bootArgsBuilder {
	return &bootArgsBuilder{}
}

// entryIndex returns the index of the entry with the given key, or -1 if not found.
func (b *bootArgsBuilder) entryIndex(key string) int {
	for i, e := range b.entries {
		if e.key == key {
			return i
		}
	}
	return -1
}

// set sets the values for a key, preserving insertion order on overwrite.
func (b *bootArgsBuilder) set(key string, values []string) {
	if i := b.entryIndex(key); i >= 0 {
		b.entries[i].values = values
	} else {
		b.entries = append(b.entries, bootArgEntry{key: key, values: values})
	}
}

// parseFromString populates the builder from a space-separated boot argument
// string (e.g. "pci=off quiet root=/dev/vda").  Multiple occurrences of the
// same key are accumulated into its value list.
func (b *bootArgsBuilder) parseFromString(s string) {
	if s == "" || strings.TrimSpace(s) == "" {
		return
	}
	for arg := range strings.FieldsSeq(s) {
		if key, value, found := strings.Cut(arg, "="); found {
			if i := b.entryIndex(key); i >= 0 {
				b.entries[i].values = append(b.entries[i].values, value)
			} else {
				b.entries = append(b.entries, bootArgEntry{key: key, values: []string{value}})
			}
		} else if b.entryIndex(arg) < 0 {
			b.entries = append(b.entries, bootArgEntry{key: arg})
		}
	}
}

// join returns the space-separated boot argument string in insertion order.
func (b *bootArgsBuilder) join() string {
	var parts []string
	for _, e := range b.entries {
		if len(e.values) == 0 {
			parts = append(parts, e.key)
		} else {
			for _, value := range e.values {
				parts = append(parts, fmt.Sprintf("%s=%s", e.key, value))
			}
		}
	}
	return strings.Join(parts, " ")
}

// --- Boot arguments ---

// buildBootArgs builds the kernel boot arguments string.
func (s *FirecrackerSpawner) buildBootArgs() (string, error) {
	bootArgs := newBootArgsBuilder()

	if s.config.BootArgs != "" {
		bootArgs.parseFromString(s.config.BootArgs)
	}

	// Inject console=ttyS0 when console is enabled and user didn't set a console=
	if s.config.EnableConsole && bootArgs.entryIndex("console") < 0 {
		bootArgs.set("console", []string{"ttyS0"})
	}

	if !s.config.PCIEnabled {
		bootArgs.set("pci", []string{"off"})
	}

	if s.config.NetworkPlacement == model.VMNetworkPlacementNetworkless {
		if bootArgs.entryIndex("ip") >= 0 {
			return "", errs.New(errs.CodeFirecrackerConfigError,
				"networkless VM configuration cannot include kernel ip boot arguments")
		}
		if bootArgs.entryIndex("ds") >= 0 {
			return "", errs.New(errs.CodeFirecrackerConfigError,
				"networkless VM configuration cannot include caller-supplied cloud-init datasource")
		}
	} else {
		// The managed endpoint must be ready before cloud-init begins.
		bootArgs.set(
			"ip",
			[]string{fmt.Sprintf("%s::%s:%s::eth0:off",
				s.config.GuestIP,
				s.config.NetworkGateway,
				s.config.NetworkNetmask,
			)},
		)
	}

	if s.config.LSMFlags != "" {
		bootArgs.set("lsm", []string{s.config.LSMFlags})
	}

	// Nested virtualization: add kernel parameter for Intel/AMD
	if s.config.NestedVirt && s.config.CPUVendor != nil && *s.config.CPUVendor != "" {
		cpuVendorLower := strings.ToLower(*s.config.CPUVendor)
		cpuArchLower := ""
		if s.config.CPUArchitecture != nil && *s.config.CPUArchitecture != "" {
			cpuArchLower = strings.ToLower(*s.config.CPUArchitecture)
		}
		// ARM/aarch64 doesn't use kvm-intel/kvm-amd module params
		// for nested virtualization — skip x86-specific boot args
		if strings.Contains(cpuArchLower, "arm") || strings.Contains(cpuArchLower, "aarch64") {
			// pass — no boot arg needed
		} else if strings.Contains(cpuVendorLower, "amd") || strings.Contains(cpuVendorLower, "hygon") {
			bootArgs.set("kvm-amd.nested", []string{"1"})
		} else {
			// Intel, Zhaoxin, Centaur/VIA, and all other x86 vendors
			// use Intel VT-x compatible virtualization with kvm-intel
			bootArgs.set("kvm-intel.nested", []string{"1"})
		}
	}

	if s.config.PCIEnabled && s.config.ImageFSUUID == "" {
		return "", errs.New(errs.CodeFirecrackerConfigError,
			"PCI transport enabled but no filesystem UUID available for "+
				"root device identification. Use an image with a known "+
				"filesystem UUID, or pass --no-pci to disable PCI transport.")
	}

	if s.config.ImageFSUUID != "" {
		bootArgs.set("root", []string{fmt.Sprintf("UUID=%s", s.config.ImageFSUUID)})
	} else {
		bootArgs.set("root", []string{"/dev/vda"})
	}

	if s.config.ImageFSUUID != "" {
		bootArgs.set("rootfstype", []string{s.config.ImageFSType})
	}

	// Determine cloud-init datasource string
	// Don't handle CloudInitMode.OFF since we don't have to add any boot args
	// Mask systemd-networkd-wait-online to prevent 2+ minute boot delay.
	// Managed VMs receive a kernel ip= endpoint; networkless VMs have no NIC.
	bootArgs.set("systemd.mask", []string{"systemd-networkd-wait-online.service"})

	cloudInitMode := s.config.CloudInitMode
	if cloudInitMode != nil && *cloudInitMode != "" && *cloudInitMode != model.CloudInitModeOFF {
		if *cloudInitMode == model.CloudInitModeNET {
			// For nocloud-net, validate URL is configured
			if s.config.CloudInitNoCloudURL == nil || *s.config.CloudInitNoCloudURL == "" {
				return "", errs.New(errs.CodeFirecrackerConfigError,
					"NoCloud URL must be set when using NET mode")
			}
			bootArgs.set("ds", []string{fmt.Sprintf("nocloud;seedfrom=%s", *s.config.CloudInitNoCloudURL)})
		} else if *cloudInitMode == model.CloudInitModeINJECT {
			bootArgs.set("ds", []string{fmt.Sprintf("ds=nocloud;s=file://%s/", defaultLibguestfsSeedDir)})
		} else if *cloudInitMode == model.CloudInitModeISO {
			// ISO mode: local nocloud datasource
			bootArgs.set("ds", []string{"nocloud"})
		}
	}

	return bootArgs.join(), nil
}

// FirecrackerConfigManager reads and modifies Firecracker config JSON files on disk.
type FirecrackerConfigManager struct {
	configPath string
	config     map[string]any
	loaded     bool
}

// NewFirecrackerConfigManager creates a new manager for the given config path.
func NewFirecrackerConfigManager(configPath string) *FirecrackerConfigManager {
	return &FirecrackerConfigManager{
		configPath: configPath,
	}
}

// load reads the config from disk.
func (m *FirecrackerConfigManager) load() (map[string]any, error) {
	if m.loaded {
		return m.config, nil
	}
	data, err := os.ReadFile(m.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			m.config = map[string]any{"drives": []any{}}
			m.loaded = true
			return m.config, nil
		}
		return nil, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	m.config = cfg
	m.loaded = true
	return m.config, nil
}

// save writes the current config back to disk with indentation.
func (m *FirecrackerConfigManager) save() error {
	dir := filepath.Dir(m.configPath)
	if err := os.MkdirAll(dir, infra.DirPerm); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m.config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.configPath, data, 0644)
}

// RemoveDrive removes a drive entry by drive_id.
// Returns true if a drive was actually removed.
func (m *FirecrackerConfigManager) RemoveDrive(driveID string) (bool, error) {
	cfg, err := m.load()
	if err != nil {
		return false, err
	}
	drives, _ := cfg["drives"].([]any)
	before := len(drives)
	var filtered []any
	for _, d := range drives {
		if dm, ok := d.(map[string]any); ok {
			if dm["drive_id"] != driveID {
				filtered = append(filtered, d)
			}
		} else {
			filtered = append(filtered, d)
		}
	}
	if len(filtered) < before {
		cfg["drives"] = filtered
		if err := m.save(); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// AddDrive adds or replaces a drive entry.
func (m *FirecrackerConfigManager) AddDrive(driveConfig model.DriveConfig) error {
	cfg, err := m.load()
	if err != nil {
		return err
	}
	drives, _ := cfg["drives"].([]any)
	driveID := driveConfig.DriveID
	found := false
	for i, d := range drives {
		if dm, ok := d.(map[string]any); ok {
			if dm["drive_id"] == driveID {
				drives[i] = driveConfig
				found = true
				break
			}
		}
	}
	if !found {
		drives = append(drives, driveConfig)
	}
	cfg["drives"] = drives
	return m.save()
}
