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
	"syscall"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/system"
)

// spawnSubprocess spawns the nocloud server subprocess with optional auto-kill.
func spawnSubprocess(dir string, port int, host, pidPath, logPath string, killAfter time.Duration) (*exec.Cmd, error) {
	args := []string{
		"--cloud-init-dir", dir,
		"--port", strconv.Itoa(port),
		"--host", host,
		"--pid-file", pidPath,
		"--log-file", logPath,
	}
	if killAfter > 0 {
		args = append(args, "--kill-after", killAfter.String())
	}
	return system.SpawnService("nocloud-serve", nil, args...)
}

// SpawnNoCloudServer spawns a nocloud-net HTTP server subprocess.
// It finds an available port (range [portRangeStart, portRangeEnd]),
// spawns the server with an auto-kill timer, and returns the URL, port, and PID.
// The server self-terminates gracefully after killAfter duration via server.Shutdown.
func SpawnNoCloudServer(vmID, vmDir, cloudInitDir, host string, port int, portRangeStart, portRangeEnd int, killAfter time.Duration) (url string, allocatedPort int, pid int, err error) {
	pidPath := filepath.Join(vmDir, "nocloud-server.pid")
	logPath := filepath.Join(vmDir, "cloud-init.log")

	if port == 0 {
		for attempt := 0; attempt <= portRangeEnd-portRangeStart; attempt++ {
			candidate := portRangeStart + attempt
			if candidate > portRangeEnd {
				break
			}
			addr := fmt.Sprintf("%s:%d", host, candidate)
			ln, probeErr := net.Listen("tcp", addr)
			if probeErr != nil {
				continue
			}
			ln.Close()
			spawnedCmd, spawnErr := spawnSubprocess(cloudInitDir, candidate, host, pidPath, logPath, killAfter)
			if spawnErr != nil {
				continue
			}
			time.Sleep(200 * time.Millisecond)
			if aliveErr := spawnedCmd.Process.Signal(syscall.Signal(0)); aliveErr != nil {
				continue
			}
			allocatedPort = candidate
			pid = spawnedCmd.Process.Pid
			port = candidate
			break
		}
		if port == 0 {
			return "", 0, 0, fmt.Errorf("no available port in range %d-%d", portRangeStart, portRangeEnd)
		}
	} else {
		spawnedCmd, spawnErr := spawnSubprocess(cloudInitDir, port, host, pidPath, logPath, killAfter)
		if spawnErr != nil {
			return "", 0, 0, fmt.Errorf("failed to spawn nocloud-net server process: %w", spawnErr)
		}
		time.Sleep(200 * time.Millisecond)
		if aliveErr := spawnedCmd.Process.Signal(syscall.Signal(0)); aliveErr != nil {
			return "", 0, 0, fmt.Errorf("pre-allocated port %d — spawned nocloud-net server exited immediately", port)
		}
		pid = spawnedCmd.Process.Pid
	}

	url = fmt.Sprintf("http://%s:%d/", host, allocatedPort)
	slog.Info("Started NoCloud-net server",
		"vm_id", vmID,
		"host", host,
		"port", allocatedPort,
		"pid", pid,
		"auto_kill", killAfter,
	)
	return url, allocatedPort, pid, nil
}

// =========================================================================
// Standalone HTTP server (subprocess entry point)
// =========================================================================
//
// ServeNoCloudHTTP is the entry point for the nocloud server subprocess.
// It reads configuration from CLI args and serves cloud-init files until
// shutdown is requested (via OS signal or auto-kill timer expiry).
//
// Called from main.go when the "nocloud-serve" subcommand is routed.
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
	// Parse args: --cloud-init-dir DIR --port PORT --host HOST --pid-file PIDFILE --log-file LOGFILE [--kill-after DURATION]
	var cloudInitDir, host, pidFile, logFile string
	var port int
	var killAfter time.Duration
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
		case "--kill-after":
			if i+1 < len(args) {
				killAfter, _ = time.ParseDuration(args[i+1])
				i++
			}
		}
	}
	if cloudInitDir == "" || host == "" || port == 0 || pidFile == "" || logFile == "" {
		fmt.Fprintf(
			os.Stderr,
			"Error: Missing required arguments. Usage: _nocloud_serve --cloud-init-dir DIR --port PORT --host HOST --pid-file PIDFILE --log-file LOGFILE [--kill-after DURATION]\n",
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
	// Shutdown triggers: OS signal (SIGTERM/SIGINT) or auto-kill timer.
	// Uses server.Shutdown for graceful drain of in-flight requests.
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		if killAfter > 0 {
			select {
			case <-sigCh:
				fmt.Fprintf(logFP, "NoCloud-net server received signal, shutting down...\n")
			case <-time.After(killAfter):
				fmt.Fprintf(logFP, "NoCloud-net server auto-kill timer expired after %v, shutting down\n", killAfter)
			}
		} else {
			// No auto-kill — wait indefinitely for OS signal
			<-sigCh
			fmt.Fprintf(logFP, "NoCloud-net server received signal, shutting down...\n")
		}
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
