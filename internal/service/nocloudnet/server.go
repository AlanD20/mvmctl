package nocloudnet

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/system"
)

// NoCloudServer is a subprocess-based HTTP server that serves cloud-init files
// (meta-data, user-data, network-config) via the nocloud-net datasource.
// Matches Python's NoCloudNetServerManager.
//
// ARCHITECTURE: Like Python, the NoCloud server is spawned as a subprocess
// that survives beyond the CLI process lifetime. The subprocess receives
// CLI arguments (--cloud-init-dir, --port, --host, --pid-file, --log-file)
// matching the Python pattern exactly.
type NoCloudServer struct {
	mu             sync.Mutex
	id             string
	name           string
	path           string // vm_dir
	host           string
	port           int
	dir            string // cloud_init_dir (set at Start time)
	started        bool
	pid            int
	cmd            *exec.Cmd
	pidPath        string
	logPath        string
	portRangeStart int
	portRangeEnd   int
	maxRetries     int
}

// NewNoCloudServer creates a new NoCloudServer manager.
// Matches Python's NoCloudNetServerManager.__init__().
// If port is 0, auto-allocation from the range [portRangeStart, portRangeEnd] is used.
func NewNoCloudServer(
	id, name, path, host string,
	port int,
	portRangeStart, portRangeEnd, maxRetries int,
) *NoCloudServer {
	if portRangeEnd <= portRangeStart {
		slog.Warn("Invalid port range, adjusting end to start+1",
			"port_range_end", portRangeEnd,
			"port_range_start", portRangeStart,
		)
		portRangeEnd = portRangeStart + 1
	}
	return &NoCloudServer{
		id:             id,
		name:           name,
		path:           path,
		host:           host,
		port:           port,
		portRangeStart: portRangeStart,
		portRangeEnd:   portRangeEnd,
		maxRetries:     maxRetries,
	}
}

// ID returns the server's unique identifier.
func (s *NoCloudServer) ID() string {
	return s.id
}

// Name returns the human-readable name.
func (s *NoCloudServer) Name() string {
	return s.name
}

// PID returns the subprocess PID.
func (s *NoCloudServer) PID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pid
}

// Port returns the allocated port.
func (s *NoCloudServer) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.port
}

// URL returns the base URL for this server. Must be called after Start().
func (s *NoCloudServer) URL() string {
	return fmt.Sprintf("http://%s:%d/", s.host, s.port)
}

// startNoCloudSubprocess spawns the nocloud server subprocess.
// Matches Python's subprocess.Popen(server_cmd, ...) pattern.
func startNoCloudSubprocess(dir string, port int, host, pidPath, logPath string) (*exec.Cmd, error) {
	args := []string{
		"--cloud-init-dir", dir,
		"--port", strconv.Itoa(port),
		"--host", host,
		"--pid-file", pidPath,
		"--log-file", logPath,
	}
	return system.SpawnSubprocess("nocloud-serve", nil, args...)
}

// Start launches the HTTP server as a subprocess and returns (url, port, pid).
// If port was 0 at construction time, Start first finds an available port by
// attempting to bind (matching Python's socket.bind() probe).
//
// Matches Python's NoCloudNetServerManager.start() — returns tuple[str, str, int] -> (url, port, pid).
func (s *NoCloudServer) Start(ctx context.Context, dir string) (string, int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return "", 0, 0, ErrNoCloudServerAlreadyRunning(s.id)
	}
	s.dir = dir
	// Define pid and log file paths matching Python's path / pid_filename / log_filename
	pidPath := filepath.Join(s.path, "nocloud-server.pid")
	logPath := filepath.Join(s.path, "cloud-init.log")
	var allocatedPort int
	var spid int
	var cmd *exec.Cmd
	if s.port == 0 {
		// Auto-allocate from port range — matches Python's behavior:
		// try socket.bind() first, then spawn subprocess on that port
		found := false
		for attempt := 0; attempt <= s.portRangeEnd-s.portRangeStart; attempt++ {
			port := s.portRangeStart + attempt
			if port > s.portRangeEnd {
				break
			}
			// Test port availability (matches Python's socket.bind() probe)
			addr := fmt.Sprintf("%s:%d", s.host, port)
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				continue
			}
			ln.Close()
			// Spawn subprocess on this port (matches Python's subprocess.Popen flow)
			spawnedCmd, spawnErr := startNoCloudSubprocess(s.path, port, s.host, pidPath, logPath)
			if spawnErr != nil {
				continue
			}
			// Verify process is alive (matches Python's time.sleep(0.2) + proc.poll())
			time.Sleep(200 * time.Millisecond)
			if err := spawnedCmd.Process.Signal(syscall.Signal(0)); err != nil {
				continue
			}
			cmd = spawnedCmd
			spid = cmd.Process.Pid
			allocatedPort = port
			found = true
			break
		}
		if !found {
			return "", 0, 0, ErrNoCloudServerError(
				fmt.Sprintf("No available port in range %d-%d",
					s.portRangeStart, s.portRangeEnd),
			)
		}
	} else {
		// Pre-allocated port
		spawnedCmd, spawnErr := startNoCloudSubprocess(s.path, s.port, s.host, pidPath, logPath)
		if spawnErr != nil {
			return "", 0, 0, ErrNoCloudServerError(
				fmt.Sprintf("Failed to spawn nocloud-net server process: %v", spawnErr))
		}
		// Verify process is alive (matches Python)
		time.Sleep(200 * time.Millisecond)
		if err := spawnedCmd.Process.Signal(syscall.Signal(0)); err != nil {
			return "", 0, 0, ErrNoCloudServerError(
				fmt.Sprintf("Pre-allocated port %d — spawned nocloud-net server exited immediately", s.port))
		}
		cmd = spawnedCmd
		spid = cmd.Process.Pid
		allocatedPort = s.port
	}
	s.cmd = cmd
	s.pid = spid
	s.port = allocatedPort
	s.pidPath = pidPath
	s.logPath = logPath
	s.started = true
	slog.Info("Started NoCloud-net server",
		"name", s.name,
		"host", s.host,
		"port", allocatedPort,
		"pid", spid,
	)
	url := fmt.Sprintf("http://%s:%d/", s.host, allocatedPort)
	return url, allocatedPort, spid, nil
}

// Stop gracefully stops the NoCloud-net server subprocess.
// Matches Python's NoCloudNetServerManager.stop() — returns bool.
func (s *NoCloudServer) Stop() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pid == 0 {
		return false
	}

	syscall.Kill(s.pid, syscall.SIGTERM)
	os.Remove(s.pidPath)
	s.pid = 0
	s.started = false
	s.cmd = nil
	slog.Info("Terminated NoCloud-net server", "name", s.name)
	return true
}

// Terminate forcefully stops the NoCloud-net server subprocess.
// Matches Python's NoCloudNetServerManager.terminate() — returns bool.
func (s *NoCloudServer) Terminate() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.pid == 0 {
		return false
	}

	syscall.Kill(s.pid, syscall.SIGTERM)
	os.Remove(s.pidPath)
	s.pid = 0
	s.started = false
	s.cmd = nil
	slog.Info("Terminated NoCloud-net server", "name", s.name)
	return true
}

// IsRunning checks if the server subprocess is currently alive.
// Matches Python's NoCloudNetServerManager.is_running().
func (s *NoCloudServer) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pid == 0 {
		return false
	}
	// Check liveness via signal 0 (matches Python's _send_signal(pid, 0))
	err := syscall.Kill(s.pid, syscall.Signal(0))
	return err == nil
}

// =========================================================================
// Standalone HTTP server (subprocess entry point)
// =========================================================================
//
// ServeNoCloudHTTP is the entry point for the nocloud server subprocess.
// It reads configuration from CLI args and serves cloud-init files.
// This function should be called from main.go when the "_nocloud_serve"
// subcommand is used.
// After serving, it calls os.Exit(0).
//
// Matches Python's mvmctl/services/nocloud_server/process.py.
// _cloudInitRequestHandler is the custom HTTP handler for cloud-init files.
// Matches Python's _CloudInitRequestHandler.
type _cloudInitRequestHandler struct {
	cloudInitDir string
}

func (h *_cloudInitRequestHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Suppress HTTP request logging (matches Python's log_message override)
	// Add headers to prevent caching (matches Python's end_headers)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	// Security: prevent path traversal
	requestedPath := strings.TrimPrefix(r.URL.Path, "/")
	requestedPath = filepath.Clean(requestedPath)
	if strings.Contains(requestedPath, "..") {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	fullPath := filepath.Join(h.cloudInitDir, requestedPath)
	fullPathAbs, err := filepath.Abs(fullPath)
	if err != nil || !strings.HasPrefix(fullPathAbs, h.cloudInitDir) {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	info, err := os.Stat(fullPathAbs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if info.IsDir() {
		http.NotFound(w, r)
		return
	}
	data, err := os.ReadFile(fullPathAbs)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(data)
}

// ServeNoCloudHTTP is the entry point for the nocloud server subprocess.
// It parses CLI args matching Python's process.py argument parser and serves
// cloud-init files until shutdown is requested.
//
// Called from main.go when the "_nocloud_serve" subcommand is routed.
func ServeNoCloudHTTP(ctx context.Context, args []string) {
	// Parse args: --cloud-init-dir DIR --port PORT --host HOST --pid-file PIDFILE --log-file LOGFILE
	var cloudInitDir, host, pidFile, logFile string
	var port int
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--cloud-init-dir":
			if i+1 < len(args) {
				cloudInitDir = args[i+1]
				i++
			}
		case "--port":
			if i+1 < len(args) {
				port, _ = strconv.Atoi(args[i+1])
				i++
			}
		case "--host":
			if i+1 < len(args) {
				host = args[i+1]
				i++
			}
		case "--pid-file":
			if i+1 < len(args) {
				pidFile = args[i+1]
				i++
			}
		case "--log-file":
			if i+1 < len(args) {
				logFile = args[i+1]
				i++
			}
		}
	}
	if cloudInitDir == "" || host == "" || port == 0 || pidFile == "" || logFile == "" {
		fmt.Fprintf(
			os.Stderr,
			"Error: Missing required arguments. Usage: _nocloud_serve --cloud-init-dir DIR --port PORT --host HOST --pid-file PIDFILE --log-file LOGFILE\n",
		)
		os.Exit(1)
	}
	// Validate cloud-init directory (matches Python's process.py main())
	info, err := os.Stat(cloudInitDir)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr, "Error: Cloud-init directory does not exist: %s\n", cloudInitDir)
		os.Exit(1)
	}
	// Write PID file (matches Python's: args.pid_file.write_text(str(os.getpid())))
	pidDir := filepath.Dir(pidFile)
	if err := os.MkdirAll(pidDir, infra.DirPerm); err == nil {
		_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0644)
	} else {
		fmt.Fprintf(os.Stderr, "Error: Cannot write PID file: %v\n", err)
		os.Exit(1)
	}
	// Redirect stdout/stderr to log file (matches Python's log_fp handling)
	logFP, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Cannot open log file: %v\n", err)
		os.Exit(1)
	}
	defer logFP.Close()
	os.Stdout = logFP
	os.Stderr = logFP
	// Set up shutdown flag
	var shutdownRequested bool
	// Start HTTP server
	addr := fmt.Sprintf("%s:%d", host, port)
	handler := &_cloudInitRequestHandler{cloudInitDir: cloudInitDir}
	server := &http.Server{
		Addr:    addr,
		Handler: handler,
	}
	fmt.Fprintf(logFP, "NoCloud-net HTTP server starting on %s\n", addr)
	fmt.Fprintf(logFP, "Serving cloud-init files from: %s\n", cloudInitDir)
	fmt.Fprintf(logFP, "PID written to: %s\n", pidFile)
	// Signal handler for graceful shutdown (matches Python's signal.signal())
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		<-sigCh
		fmt.Fprintf(logFP, "NoCloud-net server received signal, shutting down...\n")
		shutdownRequested = true
		if server != nil {
			server.Shutdown(ctx)
		}
	}()
	err = server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "Error starting server: %v\n", err)
		// Clean up PID file
		os.Remove(pidFile)
		os.Exit(1)
	}
	// Graceful shutdown
	server.Close()
	// Clean up PID file
	os.Remove(pidFile)
	fmt.Fprintf(logFP, "NoCloud-net HTTP server stopped\n")
	if shutdownRequested {
		os.Exit(0)
	}
	os.Exit(0)
}
