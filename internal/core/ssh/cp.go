package ssh

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/infra/event"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// ── Constants matching Python's _pipeChunkSize, _GNU_CREATE_EXTRAS,
//    _GNU_EXTRACT_EXTRAS, and _tarCache ─────────────────────────────

const pipeChunkSize = 65536
const sshConnectTimeout = 5 // seconds, matches Python's hardcoded connect timeout

// gnuCreateExtras mirrors Python's _GNU_CREATE_EXTRAS:
//
//	["--xattrs", "--acls"]
var gnuCreateExtras = []string{"--xattrs", "--acls"}

// gnuExtractExtras mirrors Python's _GNU_EXTRACT_EXTRAS:
//
//	["-p", "--same-owner", "--delay-directory-restore"]
var gnuExtractExtras = []string{"-p", "--same-owner", "--delay-directory-restore"}

// CPService provides tar-over-SSH file copy between host and VM.
// Matches Python's CPService (tar-over-SSH pipe chain).
// NEVER falls back to SCP — always uses tar pipe, even for single files.
type CPService struct {
	// tarCache mirrors Python's _tar_cache: "user@host" → is_gnu_bool.
	// The "local" key caches the host tar detection.
	tarCache   map[string]bool
	tarCacheMu sync.RWMutex
}

func NewCPService() *CPService {
	return &CPService{
		tarCache: map[string]bool{},
	}
}

// ── Remote path probing ────────────────────────────────────────────

// probeRemotePath probes a remote path and returns (pathType, sizeInBytes, error).
// pathType is "FILE" or "DIR".
// Matches Python's CPService._probe_remote_path() exactly.
//
// Python uses run_cmd(cmd, capture=True, check=True). With check=True, run_cmd
// raises ProcessError on any failure (SSH error, non-zero exit, etc.). The
// ProcessError propagates unwrapped to the caller. Go mirrors this by returning
// the raw exec error without wrapping in a domain error.
func (s *CPService) probeRemotePath(ctx context.Context, sshPrefix []string, remotePath string) (string, int64, error) {
	probeCmd := fmt.Sprintf(
		"test -f '%[1]s' && echo FILE && stat -c%%s '%[1]s' || (test -d '%[1]s' && echo DIR && du -sb '%[1]s' | cut -f1) || echo NONE",
		remotePath,
	)
	cmdArgs := append([]string{}, sshPrefix...)
	cmdArgs = append(cmdArgs, probeCmd)

	c := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
	c.Env = append(os.Environ(), "MVM_SSH_CONNECTION=1")
	stdout, err := c.Output()
	if err != nil {
		return "", 0, err
	}

	lines := strings.SplitN(strings.TrimSpace(string(stdout)), "\n", 2)

	// Guard against empty/near-empty output, matching Python's:
	//   lines = result.stdout.strip().splitlines()
	//   if not lines:
	//       raise CPSourceNotFoundError(...)
	if len(lines) == 0 || lines[0] == "" {
		return "", 0, errs.NotFound(errs.CodeCPSourceNotFound,
			fmt.Sprintf("remote path not found: %s", remotePath))
	}

	pathType := strings.TrimSpace(lines[0])
	if pathType == "NONE" {
		return "", 0, errs.NotFound(errs.CodeCPSourceNotFound,
			fmt.Sprintf("remote path not found: %s", remotePath))
	}

	var size int64
	if len(lines) > 1 {
		if _, err := fmt.Sscanf(strings.TrimSpace(lines[1]), "%d", &size); err != nil {
			slog.Warn("failed to parse remote path size from probe output",
				"remotePath", remotePath,
				"output", lines[1],
				"error", err,
			)
		}
	}
	return pathType, size, nil
}

// ── Tar capability probing ─────────────────────────────────────────

// probeTarGNU probes tar --version and returns true if it's GNU tar.
// If sshPrefix is non-empty, the probe runs remotely via SSH.
// Results are cached per target (SSH host or "local").
// Matches Python's CPService._probe_remote_tar() and _is_local_tar_gnu().
func (s *CPService) probeTarGNU(ctx context.Context, sshPrefix ...string) bool {
	target := "local"
	if len(sshPrefix) > 0 {
		target = sshPrefix[len(sshPrefix)-1]
	}

	s.tarCacheMu.RLock()
	isGNU, ok := s.tarCache[target]
	s.tarCacheMu.RUnlock()
	if ok {
		return isGNU
	}

	var stdout []byte
	var err error
	if len(sshPrefix) > 0 {
		cmdArgs := append([]string{}, sshPrefix...)
		cmdArgs = append(cmdArgs, "tar --version 2>/dev/null | head -1")
		c := exec.CommandContext(ctx, cmdArgs[0], cmdArgs[1:]...)
		c.Env = append(os.Environ(), "MVM_SSH_CONNECTION=1")
		stdout, err = c.Output()
	} else {
		c := exec.CommandContext(ctx, "tar", "--version")
		stdout, err = c.Output()
	}

	if err != nil {
		isGNU = false
	} else {
		isGNU = strings.Contains(string(stdout), "GNU tar")
	}

	s.tarCacheMu.Lock()
	s.tarCache[target] = isGNU
	s.tarCacheMu.Unlock()
	return isGNU
}

// ── Tar command builders ───────────────────────────────────────────

// buildSourceTar builds the tar create command list for a local path.
// Matches Python's CPService._build_source_tar() exactly.
func (s *CPService) buildSourceTar(path string, isDir bool, gnuExtras bool) []string {
	var extra []string
	if gnuExtras {
		extra = gnuCreateExtras
	}
	if isDir {
		return append([]string{"tar", "cf", "-"}, append(extra, "-C", path, ".")...)
	}
	parent := filepath.Dir(path)
	if parent == "" || parent == "." {
		parent = "."
	}
	base := filepath.Base(path)
	return append([]string{"tar", "cf", "-"}, append(extra, "-C", parent, base)...)
}

// buildRemoteSourceTar builds the tar create shell command string for a remote path.
// Matches Python's CPService._build_remote_source_tar().
func (s *CPService) buildRemoteSourceTar(path string, isDir bool, gnuExtras bool) string {
	var extraFlags string
	if gnuExtras {
		parts := make([]string, len(gnuCreateExtras))
		for i, f := range gnuCreateExtras {
			parts[i] = infra.ShlexQuote(f)
		}
		extraFlags = strings.Join(parts, " ")
	}

	quotedPath := infra.ShlexQuote(path)
	if isDir {
		if extraFlags != "" {
			return fmt.Sprintf("tar cf - %s -C %s .", extraFlags, quotedPath)
		}
		return fmt.Sprintf("tar cf - -C %s .", quotedPath)
	}
	parent := filepath.Dir(path)
	if parent == "" || parent == "." {
		parent = "."
	}
	base := filepath.Base(path)
	if extraFlags != "" {
		return fmt.Sprintf("tar cf - %s -C %s %s", extraFlags, infra.ShlexQuote(parent), infra.ShlexQuote(base))
	}
	return fmt.Sprintf("tar cf - -C %s %s", infra.ShlexQuote(parent), infra.ShlexQuote(base))
}

// buildDestTar builds the tar extract command list for a local destination.
// Matches Python's CPService._build_dest_tar() exactly.
func (s *CPService) buildDestTar(dstPath string, gnuExtras bool, noOverwrite bool) []string {
	var flags []string
	if noOverwrite {
		flags = append(flags, "-k")
	}
	var extra []string
	if gnuExtras {
		extra = gnuExtractExtras
	}
	extra = append(extra, "--no-same-owner")
	args := []string{"tar", "xf", "-"}
	args = append(args, flags...)
	args = append(args, extra...)
	args = append(args, "-C", dstPath)
	return args
}

// buildRemoteDestTar builds the tar extract shell command string for a remote path.
// Matches Python's CPService._build_remote_dest_tar() exactly.
func (s *CPService) buildRemoteDestTar(dstPath string, gnuExtras bool, noOverwrite bool) string {
	var parts []string
	if noOverwrite {
		parts = append(parts, "-k")
	} else if gnuExtras {
		parts = append(parts, "--overwrite")
	}
	if gnuExtras {
		for _, f := range gnuExtractExtras {
			parts = append(parts, f)
		}
	}
	parts = append(parts, "--no-same-owner")
	parts = append(parts, "-C", infra.ShlexQuote(dstPath))
	return "tar xf - " + strings.Join(parts, " ")
}

// ── Pipe ───────────────────────────────────────────────────────────

// progressWriter wraps an io.Writer, tracking bytes written and calling
// the onProgress callback after each write.
type progressWriter struct {
	w           io.Writer
	totalCopied *int64
	totalSize   int64
	onProgress  event.OnDownloadCallback
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n, err := pw.w.Write(p)
	*pw.totalCopied += int64(n)
	if pw.onProgress != nil && n > 0 {
		pw.onProgress(*pw.totalCopied, pw.totalSize)
	}
	return n, err
}

// pipe sets up a source→destination tar pipe, copies data, waits for
// completion, and returns a classified error. If onProgress is non-nil,
// progress is reported via callback. Always uses a 64 KiB copy buffer.
//
// Replaces Python's CPService._pipe_with_progress() and the old
// pipeWithProgress/runBashPipe split. Error classification:
//   - Source failure → ErrCPSourceFailed (code="cp.source_failed")
//   - Dest failure with "Cannot open"/"Exists"/"File exists" → CodeCPDestinationExists
//   - Other dest failure → CodeCPDestinationFailed
func (s *CPService) pipe(
	ctx context.Context,
	srcCmd, destCmd []string,
	totalSize int64,
	onProgress event.OnDownloadCallback,
) error {
	srcProc := exec.CommandContext(ctx, srcCmd[0], srcCmd[1:]...)
	srcProc.Env = append(os.Environ(), "MVM_SSH_CONNECTION=1")
	srcStderr, err := srcProc.StderrPipe()
	if err != nil {
		return errs.New(errs.CodeCPError, "failed to set up pipe between processes")
	}
	destProc := exec.CommandContext(ctx, destCmd[0], destCmd[1:]...)
	destProc.Env = append(os.Environ(), "MVM_SSH_CONNECTION=1")

	srcStdout, err := srcProc.StdoutPipe()
	if err != nil {
		return errs.New(errs.CodeCPError, "failed to set up pipe between processes")
	}
	destStdin, err := destProc.StdinPipe()
	if err != nil {
		return errs.New(errs.CodeCPError, "failed to set up pipe between processes")
	}
	destStderr, err := destProc.StderrPipe()
	if err != nil {
		return errs.New(errs.CodeCPError, "failed to set up pipe between processes")
	}

	// Deferred pipe closing — ensures cleanup on early failure,
	// matching Python's try/finally block in _pipe_with_progress.
	defer destStdin.Close()
	defer srcStdout.Close()

	if err := srcProc.Start(); err != nil {
		return errs.New(errs.CodeCPError, "failed to set up pipe between processes")
	}
	if err := destProc.Start(); err != nil {
		// srcProc is running with no consumer on its stdout — it would
		// hang forever. Kill and reap to avoid a zombie process.
		srcProc.Process.Kill()
		srcProc.Wait()
		return errs.New(errs.CodeCPError, "failed to set up pipe between processes")
	}

	// Copy src stdout → dest stdin with optional progress tracking
	var totalCopied int64
	copyDest := io.Writer(destStdin)
	if onProgress != nil {
		// Python progress path: use progressWriter wrapper
		copyDest = &progressWriter{
			w:           destStdin,
			totalCopied: &totalCopied,
			totalSize:   totalSize,
			onProgress:  onProgress,
		}
	}
	if _, err := io.CopyBuffer(copyDest, srcStdout, make([]byte, pipeChunkSize)); err != nil {
		slog.Debug("pipe copy ended with error", "error", err)
	}

	destStdin.Close()
	srcStdout.Close()

	// Read stderr from BOTH processes BEFORE Wait() — Wait() closes pipes.
	srcStderrStr := ""
	if srcStderr != nil {
		stderrBytes, err := io.ReadAll(srcStderr)
		if err != nil {
			slog.Warn("failed to read source process stderr", "error", err)
		}
		srcStderrStr = strings.TrimSpace(string(stderrBytes))
	}
	destStderrStr := ""
	if destStderr != nil {
		stderrBytes, err := io.ReadAll(destStderr)
		if err != nil {
			slog.Warn("failed to read destination process stderr", "error", err)
		}
		destStderrStr = strings.TrimSpace(string(stderrBytes))
	}

	srcErr := srcProc.Wait()
	destErr := destProc.Wait()

	if srcErr != nil {
		exitCode := exitCodeFromExitErr(srcErr)
		msg := srcStderrStr
		if msg == "" {
			msg = destStderrStr
		}
		if msg == "" {
			msg = fmt.Sprintf("source tar process failed (exit %d)", exitCode)
		}
		return errs.New(errs.CodeCPSourceFailed, fmt.Sprintf("source tar process failed (exit %d): %s", exitCode, msg))
	}
	if destErr != nil {
		exitCode := exitCodeFromExitErr(destErr)
		msg := destStderrStr
		if msg == "" {
			msg = fmt.Sprintf("destination process failed (exit %d)", exitCode)
		}
		if strings.Contains(msg, "Cannot open") || strings.Contains(msg, "Exists") ||
			strings.Contains(msg, "File exists") {
			// Python: raise CPDestinationExistsError(f"Destination exists: {msg}", code="cp.destination_exists")
			return errs.New(errs.CodeCPDestinationExists,
				fmt.Sprintf("destination exists: %s", msg))
		}
		// Python: raise CPError(msg, code="cp.destination_failed")
		return errs.New(errs.CodeCPDestinationFailed, msg)
	}

	return nil
}

// exitCodeFromExitErr extracts the exit code from an exec.ExitError, defaulting to 1.
func exitCodeFromExitErr(err error) int {
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode()
	}
	return 1
}

// ── Copy directions ────────────────────────────────────────────────

// sourceInfo holds metadata about a source path for multi-source copy.
type sourceInfo struct {
	path  string
	isDir bool
	size  int64
}

// CopyToVM copies one or more files/directories from host to VM using tar-over-SSH pipe.
// ALWAYS uses tar (never SCP), matching Python's CPService.copy_host_to_vm().
//
// Python accepts multiple local paths; this supports multiple src paths.
// The tar pipe stream is built exactly as Python does.
//
// force: if true, overwrite existing files (no_overwrite = false).
// Returns (total_bytes, message, error).
func (s *CPService) CopyToVM(
	ctx context.Context,
	srcs []string,
	dest string,
	info model.ConnectionInfo,
	force bool,
	onProgress event.OnDownloadCallback,
) (int64, string, error) {
	if len(srcs) == 0 {
		return 0, "", fmt.Errorf("no source paths specified")
	}

	// Validate all sources exist (Python: inline validation in copy_host_to_vm)
	var sourceInfos []sourceInfo
	var totalSize int64
	for _, src := range srcs {
		fi, err := os.Stat(src)
		if err != nil {
			if os.IsNotExist(err) {
				// Python: raise CPSourceNotFoundError(f"Local path not found: {p}", code="cp.source_not_found")
				return 0, "", errs.NotFound(errs.CodeCPSourceNotFound,
					fmt.Sprintf("local path not found: %s", src))
			}
			return 0, "", err
		}
		// Python: if not os.path.isfile(p) and not os.path.isdir(p): raise CPSourceNotFoundError
		if !fi.Mode().IsRegular() && !fi.IsDir() {
			return 0, "", errs.NotFound(errs.CodeCPSourceNotFound,
				fmt.Sprintf("local path not found: %s", src))
		}
		isDir := fi.IsDir()
		size := int64(0)
		if isDir {
			size = infra.DirSize(src)
		} else {
			size = fi.Size()
		}
		sourceInfos = append(sourceInfos, sourceInfo{path: src, isDir: isDir, size: size})
		totalSize += size
	}

	// Wait for VM to be SSH-reachable before attempting copy.
	probeTimeout := info.ProbeTimeout
	if probeTimeout <= 0 {
		probeTimeout = 10 * time.Second
	}
	if _, err := ProbeUntilReady(ctx, info.Host, info.User, info.KeyPath, probeTimeout); err != nil {
		return 0, "", fmt.Errorf("copy to VM: %w", err)
	}

	sshPrefix := buildSSHOpts(info.Host, info.User, info.KeyPath, sshConnectTimeout)
	remoteGNU := s.probeTarGNU(ctx, sshPrefix...)
	localGNU := s.probeTarGNU(ctx)

	isMulti := len(srcs) > 1

	// Build combined tar source
	var srcCmd []string
	if isMulti {
		srcCmd = s.buildMultiSourceTar(srcs, localGNU)
	} else {
		srcCmd = s.buildSourceTar(sourceInfos[0].path, sourceInfos[0].isDir, localGNU)
	}

	noOverwrite := !force
	remoteDestCmdStr := s.buildRemoteDestTar(dest, remoteGNU, noOverwrite)
	destCmd := append([]string{}, sshPrefix...)
	destCmd = append(destCmd, remoteDestCmdStr)

	// Python: _pipe_with_progress(src_cmd, dest_cmd, total_size, on_progress)
	// Unified pipe handles both progress and non-progress paths, including
	// the "Cannot open"/"Exists" check that was previously done inline.
	if err := s.pipe(ctx, srcCmd, destCmd, totalSize, onProgress); err != nil {
		return 0, "", err
	}

	// Python logger.info after successful copy
	if isMulti {
		slog.Info("Copied items to VM",
			"count", len(srcs),
			"user", info.User,
			"host", info.Host,
			"dest", dest,
			"bytes", totalSize,
		)
	} else {
		slog.Info("Copied file to VM",
			"source", sourceInfos[0].path,
			"user", info.User,
			"host", info.Host,
			"dest", dest,
			"bytes", totalSize,
		)
	}

	// Construct human-readable message (matches Python's return format exactly)
	var message string
	if isMulti {
		message = fmt.Sprintf("Copied %d items to %s:%s", len(srcs), info.Host, dest)
	} else {
		basename := filepath.Base(sourceInfos[0].path)
		message = fmt.Sprintf("Copied %s to %s:%s", basename, info.Host, dest)
	}

	return totalSize, message, nil
}

// CopyFromVM copies a file or directory from VM to host using tar-over-SSH pipe.
// ALWAYS uses tar (never SCP), matching Python's CPService.copy_vm_to_host().
//
// force: if true, overwrite existing destination files.
// Returns (total_bytes, message, error).
func (s *CPService) CopyFromVM(
	ctx context.Context,
	src, dest string,
	info model.ConnectionInfo,
	force bool,
	onProgress event.OnDownloadCallback,
) (int64, string, error) {
	// Wait for VM to be SSH-reachable before attempting copy.
	probeTimeout := info.ProbeTimeout
	if probeTimeout <= 0 {
		probeTimeout = 10 * time.Second
	}
	if _, err := ProbeUntilReady(ctx, info.Host, info.User, info.KeyPath, probeTimeout); err != nil {
		return 0, "", fmt.Errorf("copy from VM: %w", err)
	}

	sshPrefix := buildSSHOpts(info.Host, info.User, info.KeyPath, sshConnectTimeout)
	remoteGNU := s.probeTarGNU(ctx, sshPrefix...)
	localGNU := s.probeTarGNU(ctx)

	// Probe remote path to determine type and size
	pathType, totalSize, err := s.probeRemotePath(ctx, sshPrefix, src)
	if err != nil {
		return 0, "", err
	}
	isDir := pathType == "DIR"

	// Resolve local destination
	dstPathObj, err := filepath.Abs(dest)
	if err != nil {
		return 0, "", err
	}

	noOverwrite := !force

	if isDir {
		// For directories, local_dst is the parent directory
		// Python: if not os.path.exists(dst_dir): os.makedirs(dst_dir, exist_ok=True)
		if err := os.MkdirAll(dstPathObj, infra.DirPerm); err != nil {
			return 0, "", err
		}
	} else {
		// For files, check if the target file exists (Python: inline validation)
		if noOverwrite {
			if _, err := os.Stat(dstPathObj); err == nil {
				return 0, "", errs.New(errs.CodeCPDestinationExists,
					fmt.Sprintf("local destination exists: %s. Use --force to overwrite.", dest))
			}
		}
		// Ensure parent directory exists
		parentDir := filepath.Dir(dstPathObj)
		if parentDir == "" {
			parentDir = "."
		}
		if err := os.MkdirAll(parentDir, infra.DirPerm); err != nil {
			return 0, "", err
		}
	}

	basename := filepath.Base(strings.TrimRight(src, "/"))
	remoteTarCmdStr := s.buildRemoteSourceTar(src, isDir, remoteGNU)

	// Build tar pipe
	srcCmd := append([]string{}, sshPrefix...)
	srcCmd = append(srcCmd, remoteTarCmdStr)

	var destCmd []string
	if isDir {
		destCmd = s.buildDestTar(dstPathObj, localGNU, noOverwrite)
	} else {
		destParent := filepath.Dir(dstPathObj)
		if destParent == "" {
			destParent = "."
		}
		destCmd = s.buildDestTar(destParent, localGNU, noOverwrite)
	}

	// Python: _pipe_with_progress — unified pipe handles both paths
	if err := s.pipe(ctx, srcCmd, destCmd, totalSize, onProgress); err != nil {
		return 0, "", err
	}

	// Python logger.info after successful copy
	slog.Info("Copied file from VM",
		"user", info.User,
		"host", info.Host,
		"source", src,
		"dest", dest,
		"bytes", totalSize,
	)

	// Construct human-readable message (matches Python's return format exactly)
	message := fmt.Sprintf("Copied %s from %s:%s", basename, info.Host, src)
	return totalSize, message, nil
}

// CopyVMToVM copies between two VMs via the local host using tar-over-SSH pipe chain.
// Matches Python's CPService.copy_vm_to_vm() exactly.
// Uses a direct pipe: ssh VM1 "tar cf - src" | ssh VM2 "tar xf - -C dest"
// Returns (total_bytes, message, error).
func (s *CPService) CopyVMToVM(
	ctx context.Context,
	srcVMInfo, destVMInfo model.ConnectionInfo,
	src, dest string,
	force bool,
	onProgress event.OnDownloadCallback,
) (int64, string, error) {
	// Wait for source and destination VMs to be SSH-reachable.
	srcTimeout := srcVMInfo.ProbeTimeout
	if srcTimeout <= 0 {
		srcTimeout = 10 * time.Second
	}
	if _, err := ProbeUntilReady(ctx, srcVMInfo.Host, srcVMInfo.User, srcVMInfo.KeyPath, srcTimeout); err != nil {
		return 0, "", fmt.Errorf("copy between VMs (source): %w", err)
	}
	dstTimeout := destVMInfo.ProbeTimeout
	if dstTimeout <= 0 {
		dstTimeout = 10 * time.Second
	}
	if _, err := ProbeUntilReady(ctx, destVMInfo.Host, destVMInfo.User, destVMInfo.KeyPath, dstTimeout); err != nil {
		return 0, "", fmt.Errorf("copy between VMs (destination): %w", err)
	}

	noOverwrite := !force
	srcSSHPrefix := buildSSHOpts(srcVMInfo.Host, srcVMInfo.User, srcVMInfo.KeyPath, sshConnectTimeout)
	destSSHPrefix := buildSSHOpts(destVMInfo.Host, destVMInfo.User, destVMInfo.KeyPath, sshConnectTimeout)

	// Probe remote source to determine type
	pathType, totalSize, err := s.probeRemotePath(ctx, srcSSHPrefix, src)
	if err != nil {
		return 0, "", err
	}
	isDir := pathType == "DIR"

	// Build source tar command (remote)
	remoteGNU := s.probeTarGNU(ctx, srcSSHPrefix...)
	sourceTarCmdStr := s.buildRemoteSourceTar(src, isDir, remoteGNU)

	// Build dest tar command (remote)
	destGNU := s.probeTarGNU(ctx, destSSHPrefix...)
	destTarCmdStr := s.buildRemoteDestTar(dest, destGNU, noOverwrite)

	// Build pipe: ssh srcVM "tar cf - src" → ssh destVM "tar xf - -C dest"
	srcCmd := append([]string{}, srcSSHPrefix...)
	srcCmd = append(srcCmd, sourceTarCmdStr)

	destCmd := append([]string{}, destSSHPrefix...)
	destCmd = append(destCmd, destTarCmdStr)

	// Python: _pipe_with_progress — unified pipe handles both paths
	if err := s.pipe(ctx, srcCmd, destCmd, totalSize, onProgress); err != nil {
		return 0, "", err
	}

	// Python logger.info after successful copy
	slog.Info("Copied file between VMs",
		"src_user", srcVMInfo.User,
		"src_host", srcVMInfo.Host,
		"src_path", src,
		"dest_user", destVMInfo.User,
		"dest_host", destVMInfo.Host,
		"dest_path", dest,
		"bytes", totalSize,
	)

	// Construct human-readable message (matches Python's return format exactly)
	basename := filepath.Base(strings.TrimRight(src, "/"))
	message := fmt.Sprintf("Copied %s from %s:%s to %s:%s", basename, srcVMInfo.Host, src, destVMInfo.Host, dest)
	return totalSize, message, nil
}

// buildMultiSourceTar builds a combined tar command for multiple paths.
// Matches Python's CPService._build_multi_source_tar() exactly.
// Uses `-C <parent> <base>` for ALL paths (both files and directories),
// matching Python's behavior: parent = os.path.dirname(path) or ".",
// base = os.path.basename(path); cmd.extend(["-C", parent, base])
func (s *CPService) buildMultiSourceTar(srcs []string, gnuExtras bool) []string {
	args := []string{"tar", "cf", "-"}
	if gnuExtras {
		args = append(args, gnuCreateExtras...)
	}
	for _, path := range srcs {
		parent := filepath.Dir(path)
		if parent == "" || parent == "." {
			parent = "."
		}
		base := filepath.Base(path)
		args = append(args, "-C", parent, base)
	}
	return args
}
