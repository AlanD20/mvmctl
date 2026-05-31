package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"mvmctl/internal/infra/model"
)

// ── Constants matching Python's constants.py ──
const (
	constHTTPStatusNoContent    = 204
	constHTTPStatusSuccess      = 200
	constPollStepSeconds        = 0.1
	constSocketTimeoutSeconds   = 5.0
	constSignalExitCodeBase     = 128
	constDefaultGracefulTimeout = 30.0
	constDefaultKillTimeout     = 5.0
	defaultLibguestfsSeedDir    = "/var/lib/cloud/seed/nocloud"
)

// ── Error types matching Python's exceptions.py ──

// FirecrackerClientError matches Python's FirecrackerClientError.
type FirecrackerClientError struct {
	Message string
}

func (e *FirecrackerClientError) Error() string {
	return e.Message
}

// IsMVMError marks this as an MVMError subclass, matching Python's MVMError hierarchy.
func (e *FirecrackerClientError) IsMVMError() bool { return true }

// FirecrackerSpawnError matches Python's FirecrackerSpawnError.
type FirecrackerSpawnError struct {
	Message string
}

func (e *FirecrackerSpawnError) Error() string {
	return e.Message
}

// IsMVMError marks this as an MVMError subclass, matching Python's MVMError hierarchy.
func (e *FirecrackerSpawnError) IsMVMError() bool { return true }

// SocketNotFoundError matches Python's SocketNotFoundError.
type SocketNotFoundError struct {
	Path string
}

func (e *SocketNotFoundError) Error() string {
	return fmt.Sprintf("Socket not found: %s", e.Path)
}

// IsMVMError marks this as an MVMError subclass, matching Python's MVMError hierarchy.
func (e *SocketNotFoundError) IsMVMError() bool { return true }

// FirecrackerConfigError matches Python's FirecrackerConfigError.
type FirecrackerConfigError struct {
	Message string
}

func (e *FirecrackerConfigError) Error() string {
	return e.Message
}

// IsMVMError marks this as an MVMError subclass, matching Python's MVMError hierarchy.
func (e *FirecrackerConfigError) IsMVMError() bool { return true }

// =============================================================================
// FirecrackerSpawner — matches Python's FirecrackerSpawner class exactly
// =============================================================================

// FirecrackerSpawner manages Firecracker process lifecycle and config generation.
// Matches Python's core/vm/_firecracker.py:FirecrackerSpawner exactly.
type FirecrackerSpawner struct {
	config           *model.FirecrackerConfig
	configPath       string
	logPath          string
	metricsPath      string
	serialOutputPath string
	pidPath          string
	apiSocketPath    string
	pid              *int
	processStartTime *int64
	fcLogFP          *os.File
	serialOutputFP   *os.File
}

// NewFirecrackerSpawner creates a new FirecrackerSpawner.
// Matches Python's FirecrackerSpawner.__init__(config, *, config_path=None).
func NewFirecrackerSpawner(config *model.FirecrackerConfig, configPath ...string) *FirecrackerSpawner {
	s := &FirecrackerSpawner{
		config: config,
	}
	if len(configPath) > 0 && configPath[0] != "" {
		s.configPath = configPath[0]
	} else {
		s.configPath = filepath.Join(config.VMDir, config.ConfigFilename)
	}
	s.logPath = filepath.Join(config.VMDir, config.LogFilename)
	s.metricsPath = filepath.Join(config.VMDir, config.MetricsFilename)
	s.serialOutputPath = filepath.Join(config.VMDir, config.SerialOutputFilename)
	s.pidPath = filepath.Join(config.VMDir, config.PIDFilename)
	s.apiSocketPath = filepath.Join(config.VMDir, config.APISocketFilename)
	return s
}

// ── Property methods matching Python @property ──

// LogPath returns the path to the log file.
// Matches Python's log_path property.
func (s *FirecrackerSpawner) LogPath() string {
	return s.logPath
}

// APISocketPath returns the path to the API socket.
// Matches Python's api_socket_path property.
func (s *FirecrackerSpawner) APISocketPath() string {
	return s.apiSocketPath
}

// PidPath returns the path to the PID file.
// Matches Python's pid_path property.
func (s *FirecrackerSpawner) PidPath() string {
	return s.pidPath
}

// SerialOutputPath returns the path to the serial output file.
// Matches Python's serial_output_path property.
func (s *FirecrackerSpawner) SerialOutputPath() string {
	return s.serialOutputPath
}

// MetricsPath returns the path to the metrics file.
// Matches Python's metrics_path property.
func (s *FirecrackerSpawner) MetricsPath() string {
	return s.metricsPath
}

// ConfigPath returns the path to the config file.
// Matches Python's config_path property.
func (s *FirecrackerSpawner) ConfigPath() string {
	return s.configPath
}

// PID returns the Firecracker process PID, or nil if not spawned.
// Matches Python's pid property on FirecrackerSpawner.
func (s *FirecrackerSpawner) PID() *int {
	return s.pid
}

// ProcessStartTime returns the Firecracker process start time (clock ticks),
// or nil if not spawned.
// Matches Python's process_start_time property on FirecrackerSpawner.
func (s *FirecrackerSpawner) ProcessStartTime() *int64 {
	return s.processStartTime
}

// ── Spawn ──

// Spawn starts a Firecracker process.
//
// Polls for the API socket to become available (up to 2s, every 0.1s) and
// exits early as soon as the socket appears. If the process dies before the
// socket is created, raises immediately.
//
// Matches Python's FirecrackerSpawner.spawn() exactly.
func (s *FirecrackerSpawner) Spawn() error {
	// Remove stale API socket from previous run
	if _, err := os.Stat(s.apiSocketPath); err == nil {
		os.Remove(s.apiSocketPath)
	}

	relayEnabled := s.config.RelayEnabled
	relayClientFD := s.config.RelayClientFD
	snapshotMode := s.config.SnapshotMode

	// In Go, we use os.DevNull for DEVNULL behavior (nil stdin/stdout = /dev/null)
	var fcStdin *os.File
	var fcStdout *os.File
	// fcStdin = nil → reads from /dev/null

	if !snapshotMode && s.config.EnableConsole && relayEnabled {
		if relayClientFD == nil || *relayClientFD == 0 {
			return &FirecrackerSpawnError{
				Message: "Console enabled but PTY client FD is None",
			}
		}
		relayFile := os.NewFile(uintptr(*relayClientFD), "relay-client")
		fcStdin = relayFile
		fcStdout = relayFile
	} else {
		var err error
		s.serialOutputFP, err = s.CreateFilepointer(s.serialOutputPath)
		if err != nil {
			return err // preserve original OSError — Python lets OSError propagate
		}
		fcStdout = s.serialOutputFP
	}

	var err error
	s.fcLogFP, err = s.CreateFilepointer(s.logPath)
	if err != nil {
		return err // preserve original OSError — Python lets OSError propagate
	}

	fcCmd := []string{
		s.config.BinaryPath,
		"--api-sock",
		s.apiSocketPath,
	}
	if s.config.PCIEnabled {
		fcCmd = append(fcCmd, "--enable-pci")
	}
	if !snapshotMode {
		fcCmd = append(fcCmd, "--config-file", s.configPath)
	}

	cmd := exec.Command(fcCmd[0], fcCmd[1:]...)
	cmd.Stdin = fcStdin
	cmd.Stdout = fcStdout
	cmd.Stderr = s.fcLogFP
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true, // matches Python's start_new_session=True which calls os.setsid()
	}

	// Relay FD is already passed via cmd.Stdin/cmd.Stdout (fd 0/1).
	// Python's pass_fds=[relay_client_fd] ensures the FD stays open in the child
	// process — this is already handled by setting Stdin/Stdout to relayFile above.
	// Go must NOT add a duplicate via ExtraFiles.

	if err := cmd.Start(); err != nil {
		return err // preserve original error — Python's subprocess.Popen() lets OSError propagate
	}

	// Wait for Firecracker to initialize (poll every 0.1s for up to 2s).
	// Exit early as soon as the API socket appears.
	maxStartupWait := 2.0
	waited := 0.0

	for waited < maxStartupWait {
		time.Sleep(time.Duration(constPollStepSeconds * float64(time.Second)))
		waited += constPollStepSeconds

		// Early exit: socket appeared → Firecracker is ready
		if _, err := os.Stat(s.apiSocketPath); err == nil {
			break
		}

		// Check if process is still alive using Signal(0).
		// cmd.ProcessState is NOT set by Start(), so cmd.ProcessState.Exited()
		// cannot be used here. Signal(0) returns nil if process exists.
		if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
			// Process is dead — wait for reaper to collect exit code
			ps, waitErr := cmd.Process.Wait()
			exitCode := -1
			if waitErr == nil && ps != nil {
				exitCode = ps.ExitCode()
			}
			return &FirecrackerSpawnError{
				Message: fmt.Sprintf("Firecracker process exited immediately with code %d", exitCode),
			}
		}
	}

	// Loop fell through without breaking → socket never appeared
	if _, err := os.Stat(s.apiSocketPath); os.IsNotExist(err) {
		return &FirecrackerSpawnError{
			Message: fmt.Sprintf("Firecracker API socket not available after %ds", int(maxStartupWait)),
		}
	}

	// Close file pointers since the firecracker process is managing them
	s.CloseFilePointers()

	pid := cmd.Process.Pid
	s.pid = &pid
	s.processStartTime = getProcessStartTime(pid)

	// Write PID file (matches Python's FsUtils.write_pid_file)
	if err := writePIDFile(s.pidPath, pid); err != nil {
		slog.Warn("Failed to write PID file", "path", s.pidPath, "error", err)
	}

	return nil
}

// writePIDFile writes a PID to a file with exclusive flock locking.
// Matches Python's FsUtils.write_pid_file() which uses fcntl.flock(fd, fcntl.LOCK_EX).
func writePIDFile(path string, pid int) error {
	fd, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("open pid file: %w", err)
	}
	defer fd.Close()

	// Exclusive lock — matches Python's fcntl.flock(fd, fcntl.LOCK_EX)
	if err := syscall.Flock(int(fd.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock pid file: %w", err)
	}
	defer syscall.Flock(int(fd.Fd()), syscall.LOCK_UN)

	if _, err := fd.WriteString(strconv.Itoa(pid)); err != nil {
		return fmt.Errorf("write pid file: %w", err)
	}
	return nil
}

// ── Cleanup ──

// Cleanup performs cleanup of all created resources.
// Matches Python's FirecrackerSpawner.cleanup().
func (s *FirecrackerSpawner) Cleanup() {
	s.CloseFilePointers()
}

// ── Generate ──

// Generate builds the Firecracker config dict.
// Matches Python's FirecrackerSpawner.generate() exactly.
func (s *FirecrackerSpawner) Generate() (map[string]any, error) {
	// Nested virt requires PCI — force it on
	if s.config.NestedVirt {
		s.config.PCIEnabled = true
	}

	// Build as regular map to allow dynamic optional keys
	bootArgs, err := s.buildBootArgs()
	if err != nil {
		return nil, err
	}
	config := map[string]any{
		"boot-source": map[string]any{
			"kernel_image_path": s.config.KernelPath,
			"boot_args":         bootArgs,
		},
		"drives":             s.buildDrivesConfig(),
		"network-interfaces": s.buildNetworkConfig(),
		"machine-config": map[string]any{
			"vcpu_count":        s.config.VCPUCount,
			"mem_size_mib":      s.config.MemSizeMiB,
			"smt":               false,
			"track_dirty_pages": false,
		},
	}

	if s.config.EnableLogging {
		config["logger"] = s.buildLoggerConfig()
	}

	if s.config.EnableMetrics {
		config["metrics"] = s.buildMetricsConfig()
	}

	// CPU config (nested virt or custom template)
	cpuConfig := s.buildCPUConfig()
	if cpuConfig != nil {
		config["cpu-config"] = cpuConfig
	}

	return config, nil
}

// ── WriteToFile ──

// WriteToFile generates and writes the config to disk.
// Matches Python's FirecrackerSpawner.write_to_file().
func (s *FirecrackerSpawner) WriteToFile() error {
	config, err := s.Generate()
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	return os.WriteFile(s.configPath, data, 0644)
}

// ── CreateFilepointer ──

// CreateFilepointer opens a file for writing with line buffering.
// Matches Python's FirecrackerSpawner.create_filepointer().
func (s *FirecrackerSpawner) CreateFilepointer(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
}

// ── CloseFilePointers ──

// CloseFilePointers closes both log and serial output file pointers,
// suppressing OSError. Matches Python's _close_filepointers().
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

// ── Internal config builders ──

// buildDrivesConfig builds the drives section of the Firecracker config.
// Matches Python's _build_drives_config() exactly — calls .absolute() on paths.
func (s *FirecrackerSpawner) buildDrivesConfig() []model.DriveConfig {
	// Python uses str(self._config.rootfs_path.absolute()) — resolve to absolute path
	rootfsAbs, err := filepath.Abs(s.config.RootfsPath)
	if err != nil {
		// Fallback to original if Abs fails (should not happen in practice)
		rootfsAbs = s.config.RootfsPath
	}
	drives := []model.DriveConfig{
		{
			DriveID:      "rootfs",
			PathOnHost:   rootfsAbs,
			IsRootDevice: true,
			IsReadOnly:   false,
			CacheType:    "Unsafe",
			IOEngine:     "Sync",
		},
	}

	// Cloud-init ISO drive (if configured)
	cloudInitMode := s.config.CloudInitMode
	cloudInitISOPath := s.config.CloudInitISOPath
	if cloudInitMode != nil && *cloudInitMode != "" && *cloudInitMode != model.CloudInitModeOFF &&
		cloudInitISOPath != nil &&
		*cloudInitISOPath != "" {
		// Python also calls .absolute() on cloud_init_iso_path
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

	// Extra drives (volumes)
	drives = append(drives, s.config.ExtraDrives...)

	return drives
}

// buildLoggerConfig builds the logger section of the Firecracker config.
// Matches Python's _build_logger_config() exactly.
func (s *FirecrackerSpawner) buildLoggerConfig() model.LoggerConfig {
	return model.LoggerConfig{
		LogPath:       s.logPath,
		Level:         s.config.LogLevel,
		ShowLevel:     true,
		ShowLogOrigin: true,
	}
}

// buildMetricsConfig builds the metrics section of the Firecracker config.
// Matches Python's _build_metrics_config() exactly.
func (s *FirecrackerSpawner) buildMetricsConfig() model.MetricsConfig {
	return model.MetricsConfig{
		MetricsPath: s.metricsPath,
	}
}

// buildCPUConfig builds the cpu-config section for the Firecracker config.
//
// Returns a map suitable for the "cpu-config" key when nested virt is enabled
// or a custom CPU template was provided. Returns nil when no CPU configuration
// is needed.
//
// Matches Python's _build_cpu_config() exactly.
func (s *FirecrackerSpawner) buildCPUConfig() any {
	if s.config.CPUConfig != nil {
		return s.config.CPUConfig
	}
	if s.config.NestedVirt {
		return map[string]any{"kvm_capabilities": []any{}}
	}
	return nil
}

// ── Network config ──

// buildNetworkConfig builds the network-interfaces section.
// Matches Python's _build_network_config() exactly.
func (s *FirecrackerSpawner) buildNetworkConfig() []model.NetworkInterfaceConfig {
	networks := []model.NetworkInterfaceConfig{
		{
			IfaceID:     "eth0",
			GuestMAC:    s.config.GuestMAC,
			HostDevName: s.config.TapName,
		},
	}
	// Extra networks — future improvement
	return networks
}

// bootArgsBuilder maintains an ordered map of boot argument keys to values,
// preserving insertion order to match Python 3.7+ dict semantics.
// Go maps have non-deterministic iteration order, so we maintain a separate
// key ordering slice.  When setBootArg overwrites an existing key, its
// position in the ordering is preserved (not moved to the end).
type bootArgsBuilder struct {
	data  map[string][]string
	order []string
}

func newBootArgsBuilder() *bootArgsBuilder {
	return &bootArgsBuilder{
		data:  make(map[string][]string),
		order: nil,
	}
}

// set sets the values for a key.  If the key does not yet exist it is
// appended to the insertion-order list.  If the key already exists its
// position is preserved — matching Python's dict[key] = value semantics
// (insertion order is preserved on overwrite).
func (b *bootArgsBuilder) set(key string, values []string) {
	if _, exists := b.data[key]; !exists {
		b.order = append(b.order, key)
	}
	b.data[key] = values
}

// keys returns the insertion-ordered list of keys.
func (b *bootArgsBuilder) keys() []string {
	return b.order
}

// parseFromString populates the builder from a space-separated boot argument
// string (e.g. "pci=off quiet root=/dev/vda").  Multiple occurrences of the
// same key are accumulated into its value list.  Existing entries in the
// builder are preserved and new keys are appended.
func (b *bootArgsBuilder) parseFromString(s string) {
	if s == "" || strings.TrimSpace(s) == "" {
		return
	}
	args := strings.Fields(s)
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		if key, value, found := strings.Cut(arg, "="); found {
			existing, exists := b.data[key]
			if !exists {
				b.order = append(b.order, key)
				b.data[key] = []string{value}
			} else {
				b.data[key] = append(existing, value)
			}
		} else {
			// Flag without value — store as nil (empty) slice
			if _, ok := b.data[arg]; !ok {
				b.order = append(b.order, arg)
				b.data[arg] = nil
			}
		}
	}
}

// join returns the space-separated boot argument string, iterating keys in
// insertion order.  Matches Python's _join_boot_args_dict() exactly.
func (b *bootArgsBuilder) join() string {
	var parts []string
	for _, key := range b.order {
		values := b.data[key]
		if len(values) == 0 {
			// Flag without value
			parts = append(parts, key)
		} else {
			for _, value := range values {
				parts = append(parts, fmt.Sprintf("%s=%s", key, value))
			}
		}
	}
	return strings.Join(parts, " ")
}

// =============================================================================
// Boot arguments — matches Python's boot args building exactly
// =============================================================================

// buildBootArgs builds the kernel boot arguments string.
// Matches Python's _build_boot_args() exactly (100+ lines).
func (s *FirecrackerSpawner) buildBootArgs() (string, error) {
	bootArgs := newBootArgsBuilder()

	if s.config.BootArgs != nil && *s.config.BootArgs != "" {
		bootArgs.parseFromString(*s.config.BootArgs)
	}

	if !s.config.PCIEnabled {
		bootArgs.set("pci", []string{"off"})
	}

	// Use static kernel ip= parameter for early network bringup
	// This ensures network is ready before cloud-init runs
	// For NO_CLOUD_NET mode, also include kernel ip= for initial network bringup
	// cloud-init's network-config will ensure the IP stays consistent
	bootArgs.set(
		"ip",
		[]string{fmt.Sprintf("%s::%s:%s::eth0:off",
			s.config.GuestIP,
			s.config.NetworkGateway,
			s.config.NetworkNetmask,
		)},
	)

	if s.config.LSMFlags != nil && *s.config.LSMFlags != "" {
		bootArgs.set("lsm", []string{*s.config.LSMFlags})
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

	if s.config.PCIEnabled && (s.config.ImageFSUUID == nil || *s.config.ImageFSUUID == "") {
		return "", &FirecrackerConfigError{
			Message: "PCI transport enabled but no filesystem UUID available for " +
				"root device identification. Use an image with a known " +
				"filesystem UUID, or pass --no-pci to disable PCI transport.",
		}
	}

	if s.config.ImageFSUUID != nil && *s.config.ImageFSUUID != "" {
		bootArgs.set("root", []string{fmt.Sprintf("UUID=%s", *s.config.ImageFSUUID)})
	} else {
		bootArgs.set("root", []string{"/dev/vda"})
	}

	if s.config.ImageFSUUID != nil && *s.config.ImageFSUUID != "" {
		bootArgs.set("rootfstype", []string{s.config.ImageFSType})
	}

	// Determine cloud-init datasource string
	// Don't handle CloudInitMode.OFF since we don't have to add any boot args
	cloudInitMode := s.config.CloudInitMode
	if cloudInitMode != nil && *cloudInitMode != "" && *cloudInitMode != model.CloudInitModeOFF {
		// Mask systemd-networkd-wait-online to prevent 2+ minute boot delay
		// The kernel ip= parameter already configures the network; this service
		// would block waiting for systemd-networkd to mark it as "online"
		bootArgs.set("systemd.mask", []string{"systemd-networkd-wait-online.service"})

		if *cloudInitMode == model.CloudInitModeNET {
			// For nocloud-net, validate URL is configured
			if s.config.CloudInitNoCloudURL == nil || *s.config.CloudInitNoCloudURL == "" {
				return "", &FirecrackerConfigError{
					Message: "NoCloud URL must be set when using NET mode",
				}
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

// =============================================================================
// getProcessStartTime — matches Python's ProcessSignalHandler._get_process_start_time()
// =============================================================================

// getProcessStartTime reads /proc/<pid>/stat (field 22, clock ticks).
// Returns nil if process doesn't exist or is unreadable.
func getProcessStartTime(pid int) *int64 {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return nil
	}
	content := string(data)
	// Find last ')' to handle comm names with spaces/parens
	idx := strings.LastIndex(content, ")")
	if idx < 0 {
		return nil
	}
	fields := strings.Fields(content[idx+2:])
	if len(fields) < 20 {
		return nil
	}
	var startTime int64
	if _, err := fmt.Sscanf(fields[19], "%d", &startTime); err != nil {
		return nil
	}
	return &startTime
}

// =============================================================================
// FirecrackerClient — matches Python's FirecrackerClient class
// =============================================================================

// FirecrackerClient provides HTTP access to the Firecracker API over a Unix socket.
// Matches Python's FirecrackerClient class.
type FirecrackerClient struct {
	socketPath string
	httpClient *http.Client
}

// NewFirecrackerClient creates a new client connected to the given Unix socket path.
// Matches Python's FirecrackerClient(socket_path).
func NewFirecrackerClient(socketPath string) *FirecrackerClient {
	transport := &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.DialTimeout("unix", socketPath, constSocketTimeoutSeconds*time.Second)
		},
	}
	return &FirecrackerClient{
		socketPath: socketPath,
		httpClient: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
		},
	}
}

// Close implements Python's FirecrackerClient.close().
func (fc *FirecrackerClient) Close() {
	fc.httpClient.CloseIdleConnections()
}

// ── Low-level HTTP request with retry ──

// request makes an HTTP request to the Firecracker API with retry on connection refused.
// Matches Python's _request() method:
//   - 5 retries with exponential backoff (0.1s, 0.2s, 0.4s, 0.8s, 1.6s)
//   - SocketNotFoundError when socket doesn't exist
//   - On ECONNREFUSED: retry with reconnect
//   - Returns (status, data_json, error)
func (fc *FirecrackerClient) request(
	ctx context.Context,
	method, path string,
	body map[string]any,
) (int, map[string]any, error) {
	// Check socket exists first (matches Python's _connect which checks)
	if _, err := os.Stat(fc.socketPath); os.IsNotExist(err) {
		return 0, nil, &SocketNotFoundError{Path: fc.socketPath}
	}

	var bodyStr string
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyStr = string(data)
	}

	maxRetries := 5
	delay := 0.1
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		var reqBodyReader strings.Reader
		if bodyStr != "" {
			reqBodyReader = *strings.NewReader(bodyStr)
		}

		req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("http://localhost%s", path), &reqBodyReader)
		if err != nil {
			return 0, nil, fmt.Errorf("failed to create request: %w", err)
		}
		if bodyStr != "" {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := fc.httpClient.Do(req)
		if err != nil {
			lastErr = err

			// Check for connection refused (ECONNREFUSED) — retry
			if isConnRefused(err) && attempt < maxRetries-1 {
				time.Sleep(time.Duration(delay * float64(time.Second)))
				delay *= 2
				// Reconnect by closing idle connections (matches Python's reconnect)
				fc.Close()
				continue
			}

			return 0, nil, &FirecrackerClientError{
				Message: fmt.Sprintf("API request failed: %s", err.Error()),
			}
		}
		defer resp.Body.Close()

		status := resp.StatusCode
		respData := make(map[string]any)

		if status == constHTTPStatusNoContent {
			return status, nil, nil
		}

		if err := json.NewDecoder(resp.Body).Decode(&respData); err != nil {
			// Non-JSON response — return nil data
			respData = nil
		}

		return status, respData, nil
	}

	// All retries exhausted
	return 0, nil, &FirecrackerClientError{
		Message: fmt.Sprintf("API request failed after %d retries: %s", maxRetries, lastErr.Error()),
	}
}

func isConnRefused(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	return strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "ECONNREFUSED")
}

// ── Snapshot Operations ──

// CreateSnapshot creates a VM snapshot via PUT /snapshot/create.
// Matches Python's create_snapshot().
func (fc *FirecrackerClient) CreateSnapshot(ctx context.Context, memPath, snapshotPath string) (bool, error) {
	slog.Info("Creating snapshot...")
	body := map[string]any{
		"mem_file_path": memPath,
		"snapshot_path": snapshotPath,
	}
	status, data, err := fc.request(ctx, "PUT", "/snapshot/create", body)
	if err != nil {
		return false, err
	}
	if status == constHTTPStatusNoContent {
		slog.Info("Snapshot created", "mem", memPath, "state", snapshotPath)
		return true, nil
	}
	msg := fmt.Sprintf("Failed to create snapshot: %d", status)
	if data != nil {
		msg += fmt.Sprintf(" Response: %v", data)
	}
	return false, &FirecrackerClientError{Message: msg}
}

// LoadSnapshot loads a VM from snapshot via PUT /snapshot/load.
// Matches Python's load_snapshot().
func (fc *FirecrackerClient) LoadSnapshot(
	ctx context.Context,
	memPath, snapshotPath string,
	resume bool,
) (bool, error) {
	slog.Info("Loading snapshot...")
	body := map[string]any{
		"mem_file_path": memPath,
		"snapshot_path": snapshotPath,
		"resume_vm":     resume,
	}
	status, data, err := fc.request(ctx, "PUT", "/snapshot/load", body)
	if err != nil {
		return false, err
	}
	if status == constHTTPStatusNoContent {
		slog.Info("Snapshot loaded")
		return true, nil
	}
	msg := fmt.Sprintf("Failed to load snapshot: %d", status)
	if data != nil {
		msg += fmt.Sprintf(" Response: %v", data)
	}
	return false, &FirecrackerClientError{Message: msg}
}

// ── Instance Info Operations ──

// GetInstanceInfo returns VM instance information via GET /.
// Matches Python's get_instance_info().
func (fc *FirecrackerClient) GetInstanceInfo(ctx context.Context) (*model.InstanceInfo, error) {
	status, data, err := fc.request(ctx, "GET", "/", nil)
	if err != nil {
		return nil, err
	}
	if status == constHTTPStatusSuccess && data != nil {
		info := &model.InstanceInfo{}
		if id, ok := data["id"].(string); ok {
			info.ID = id
		}
		if state, ok := data["state"].(string); ok {
			info.State = state
		}
		if vcpu, ok := data["vcpu_count"].(float64); ok {
			info.VCPUCount = int(vcpu)
		}
		if mem, ok := data["mem_size_mib"].(float64); ok {
			info.MemSizeMiB = int(mem)
		}
		return info, nil
	}
	return nil, nil
}

// DescribeInstance returns a VM description via GET /vm.
// Matches Python's describe_instance().
func (fc *FirecrackerClient) DescribeInstance(ctx context.Context) (*model.InstanceDescription, error) {
	status, data, err := fc.request(ctx, "GET", "/vm", nil)
	if err != nil {
		return nil, err
	}
	if status == constHTTPStatusSuccess && data != nil {
		desc := &model.InstanceDescription{}
		if id, ok := data["id"].(string); ok {
			desc.ID = id
		}
		if state, ok := data["state"].(string); ok {
			desc.State = state
		}
		if vcpu, ok := data["vcpu_count"].(float64); ok {
			desc.VCPUCount = int(vcpu)
		}
		if mem, ok := data["mem_size_mib"].(float64); ok {
			desc.MemSizeMiB = int(mem)
		}
		if flags, ok := data["flags"].([]any); ok {
			for _, f := range flags {
				if s, ok := f.(string); ok {
					desc.Flags = append(desc.Flags, s)
				}
			}
		}
		if ifAddr, ok := data["if_addr"].(map[string]any); ok {
			desc.IfAddr = make(map[string]string)
			for k, v := range ifAddr {
				if s, ok := v.(string); ok {
					desc.IfAddr[k] = s
				}
			}
		}
		if devices, ok := data["used_block_devices"].([]any); ok {
			for _, d := range devices {
				if s, ok := d.(string); ok {
					desc.UsedBlockDevices = append(desc.UsedBlockDevices, s)
				}
			}
		}
		return desc, nil
	}
	return nil, nil
}

// ── VM Lifecycle Operations ──

// StartInstance starts the VM instance via PUT /actions with action_type InstanceStart.
// Matches Python's start_instance().
func (fc *FirecrackerClient) StartInstance(ctx context.Context) (bool, error) {
	slog.Info("Starting VM...")
	status, _, err := fc.request(ctx, "PUT", "/actions", map[string]any{"action_type": "InstanceStart"})
	if err != nil {
		return false, err
	}
	if status == constHTTPStatusNoContent {
		slog.Info("VM started")
		return true, nil
	}
	return false, &FirecrackerClientError{Message: fmt.Sprintf("Failed to start VM: %d", status)}
}

// SendCtrlAltDel sends Ctrl+Alt+Del to the VM via PUT /actions.
// Matches Python's send_ctrl_alt_del():
//   - SocketNotFoundError and FirecrackerClientError are absorbed (return false, nil)
//   - All other errors propagate (return false, err)
func (fc *FirecrackerClient) SendCtrlAltDel(ctx context.Context) (bool, error) {
	status, _, err := fc.request(ctx, "PUT", "/actions", map[string]any{"action_type": "SendCtrlAltDel"})
	if err != nil {
		// Only absorb expected error types (matches Python's except SocketNotFoundError, FirecrackerClientError)
		switch err.(type) {
		case *SocketNotFoundError, *FirecrackerClientError:
			slog.Error("Failed to send Ctrl+Alt+Del")
			return false, nil
		default:
			// Unexpected error — propagate (matches Python's bare raise)
			return false, err
		}
	}
	if status == constHTTPStatusNoContent {
		slog.Info("Ctrl+Alt+Del sent")
		return true, nil
	}
	slog.Error("Failed to send Ctrl+Alt+Del", "status", status)
	return false, nil
}

// PauseVM pauses the microVM via PATCH /vm with state: "Paused".
// Matches Python's pause_vm().
func (fc *FirecrackerClient) PauseVM(ctx context.Context) error {
	slog.Info("Pausing VM...")
	status, _, err := fc.request(ctx, "PATCH", "/vm", map[string]any{"state": "Paused"})
	if err != nil {
		return err
	}
	if status == constHTTPStatusNoContent {
		slog.Info("VM paused")
		return nil
	}
	return &FirecrackerClientError{Message: fmt.Sprintf("Failed to pause VM: %d", status)}
}

// ResumeVM resumes a paused microVM via PATCH /vm with state: "Resumed".
// Matches Python's resume_vm().
func (fc *FirecrackerClient) ResumeVM(ctx context.Context) error {
	slog.Info("Resuming VM...")
	status, _, err := fc.request(ctx, "PATCH", "/vm", map[string]any{"state": "Resumed"})
	if err != nil {
		return err
	}
	if status == constHTTPStatusNoContent {
		slog.Info("VM resumed")
		return nil
	}
	return &FirecrackerClientError{Message: fmt.Sprintf("Failed to resume VM: %d", status)}
}

// ── Drive Operations ──

// PutDrive attaches or updates a drive via PUT /drives/{drive_id}.
// Matches Python's put_drive().
func (fc *FirecrackerClient) PutDrive(ctx context.Context, driveConfig model.DriveConfig) error {
	body := map[string]any{
		"drive_id":       driveConfig.DriveID,
		"path_on_host":   driveConfig.PathOnHost,
		"is_root_device": driveConfig.IsRootDevice,
		"is_read_only":   driveConfig.IsReadOnly,
		"cache_type":     driveConfig.CacheType,
		"io_engine":      driveConfig.IOEngine,
	}
	status, data, err := fc.request(ctx, "PUT", "/drives/"+driveConfig.DriveID, body)
	if err != nil {
		return err
	}
	if status == constHTTPStatusSuccess || status == constHTTPStatusNoContent {
		return nil
	}
	msg := fmt.Sprintf("Failed to attach drive: %d", status)
	if data != nil {
		msg += fmt.Sprintf(" Response: %v", data)
	}
	return &FirecrackerClientError{Message: msg}
}

// PatchDrive removes a drive from a running VM via PATCH /drives/{drive_id}.
// Matches Python's patch_drive().
func (fc *FirecrackerClient) PatchDrive(ctx context.Context, driveID string) error {
	body := map[string]any{"drive_id": driveID}
	status, data, err := fc.request(ctx, "PATCH", "/drives/"+driveID, body)
	if err != nil {
		return err
	}
	if status == constHTTPStatusSuccess || status == constHTTPStatusNoContent {
		return nil
	}
	msg := fmt.Sprintf("Failed to detach drive: %d", status)
	if data != nil {
		msg += fmt.Sprintf(" Response: %v", data)
	}
	return &FirecrackerClientError{Message: msg}
}

// DeleteDrive removes a drive from a running VM via DELETE /drives/{drive_id}.
// Matches Python's delete_drive().
func (fc *FirecrackerClient) DeleteDrive(ctx context.Context, driveID string) error {
	status, data, err := fc.request(ctx, "DELETE", "/drives/"+driveID, nil)
	if err != nil {
		return err
	}
	if status == constHTTPStatusNoContent {
		return nil
	}
	msg := fmt.Sprintf("Failed to delete drive: %d", status)
	if data != nil {
		msg += fmt.Sprintf(" Response: %v", data)
	}
	return &FirecrackerClientError{Message: msg}
}

// =============================================================================
// FirecrackerConfigManager — matches Python's FirecrackerConfigManager class
// =============================================================================

// FirecrackerConfigManager reads and modifies Firecracker config JSON files on disk.
// Matches Python's FirecrackerConfigManager.
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

// load reads the config from disk. Matches Python's _load().
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
// Matches Python's _save().
func (m *FirecrackerConfigManager) save() error {
	dir := filepath.Dir(m.configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m.config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.configPath, data, 0644)
}

// RemoveDrive removes a drive entry by drive_id. Matches Python's remove_drive().
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

// AddDrive adds or replaces a drive entry. Matches Python's add_drive().
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
