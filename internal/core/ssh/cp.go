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
	"unicode"

	"mvmctl/internal/infra/model"
)

// ProgressCallback is called with the number of bytes copied during a transfer.
type ProgressCallback func(bytesCopied int64)

// ── Constants matching Python's _pipeChunkSize, _GNU_CREATE_EXTRAS,
//    _GNU_EXTRACT_EXTRAS, and _tarCache ─────────────────────────────

const pipeChunkSize = 65536

// gnuCreateExtras mirrors Python's _GNU_CREATE_EXTRAS:
//
//	["--xattrs", "--acls"]
var gnuCreateExtras = []string{"--xattrs", "--acls"}

// gnuExtractExtras mirrors Python's _GNU_EXTRACT_EXTRAS:
//
//	["-p", "--same-owner", "--delay-directory-restore"]
var gnuExtractExtras = []string{"-p", "--same-owner", "--delay-directory-restore"}

// tarCache mirrors Python's _tar_cache: "user@host" → is_gnu_bool
// The "local" key caches the host tar detection.
var (
	tarCacheMu sync.RWMutex
	tarCache   = map[string]bool{}
)

// CPService provides tar-over-SSH file copy between host and VM.
// Matches Python's CPService (tar-over-SSH pipe chain).
// NEVER falls back to SCP — always uses tar pipe, even for single files.
type CPService struct{}

func NewCPService() *CPService {
	return &CPService{}
}

// ── Path parsing ───────────────────────────────────────────────────

// _parseVMPath splits a path into optional (vmIdentifier, remotePath).
// Matches Python's CPService._parse_vm_path() exactly.
// Python returns tuple[str | None, str]; Go returns (vmID, path)
// where vmID="" corresponds to Python's None.
func (s *CPService) _parseVMPath(path string) (vmID string, filePath string) {
	prefix, rest, found := strings.Cut(path, ":")
	if found {
		return prefix, rest
	}
	return "", path
}

// ── SSH command prefix ─────────────────────────────────────────────

// _buildSSHPrefix builds the SSH command prefix list.
// Matches Python's CPService._build_ssh_prefix() EXACTLY —
// no port flag, exact same options.
func (s *CPService) _buildSSHPrefix(ip, user, keyPath string) []string {
	prefix := []string{
		"ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "BatchMode=yes",
		"-o", "ServerAliveInterval=2",
		"-o", "ServerAliveCountMax=3",
		"-o", "ConnectTimeout=5",
	}
	if keyPath != "" {
		prefix = append(prefix, "-i", keyPath)
	}
	prefix = append(prefix, fmt.Sprintf("%s@%s", user, ip))
	return prefix
}

// ── Remote path probing ────────────────────────────────────────────

// _probeRemotePath probes a remote path and returns (pathType, sizeInBytes, error).
// pathType is "FILE" or "DIR".
// Matches Python's CPService._probe_remote_path() exactly.
//
// Python uses run_cmd(cmd, capture=True, check=True). With check=True, run_cmd
// raises ProcessError on any failure (SSH error, non-zero exit, etc.). The
// ProcessError propagates unwrapped to the caller. Go mirrors this by returning
// the raw exec error without wrapping in a domain error.
func (s *CPService) _probeRemotePath(sshPrefix []string, remotePath string) (string, int64, error) {
	probeCmd := fmt.Sprintf(
		"test -f '%s' && echo FILE && stat -c%%s '%s' || (test -d '%s' && echo DIR && du -sb '%s' | cut -f1) || echo NONE",
		remotePath,
		remotePath,
		remotePath,
		remotePath,
	)
	cmd := append([]string{}, sshPrefix...)
	cmd = append(cmd, probeCmd)

	execCmd := exec.Command(cmd[0], cmd[1:]...)
	out, err := execCmd.Output()
	if err != nil {
		// Python: ProcessError propagates from run_cmd(cmd, capture=True, check=True).
		// We return the raw error (matching Python's exception propagation).
		return "", 0, err
	}

	lines := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)
	if len(lines) == 0 {
		return "", 0, ErrCPSourceNotFound(
			fmt.Sprintf("Remote path not found: %s", remotePath),
		)
	}

	pathType := strings.TrimSpace(lines[0])
	if pathType == "NONE" {
		return "", 0, ErrCPSourceNotFound(
			fmt.Sprintf("Remote path not found: %s", remotePath),
		)
	}

	var size int64
	if len(lines) > 1 {
		fmt.Sscanf(strings.TrimSpace(lines[1]), "%d", &size)
	}
	return pathType, size, nil
}

// ── Tar capability probing ─────────────────────────────────────────

// _probeRemoteTar probes remote tar and returns true if it's GNU tar.
// Results are cached per host. Matches Python's CPService._probe_remote_tar() exactly:
//
//	target = ssh_prefix[-1] if ssh_prefix else "unknown"
func (s *CPService) _probeRemoteTar(sshPrefix []string) bool {
	target := "unknown"
	if len(sshPrefix) > 0 {
		target = sshPrefix[len(sshPrefix)-1]
	}

	tarCacheMu.RLock()
	isGnu, ok := tarCache[target]
	tarCacheMu.RUnlock()
	if ok {
		return isGnu
	}

	probeCmd := "tar --version 2>/dev/null | head -1"
	cmd := append([]string{}, sshPrefix...)
	cmd = append(cmd, probeCmd)

	execCmd := exec.Command(cmd[0], cmd[1:]...)
	out, err := execCmd.Output()
	if err != nil {
		isGnu = false
	} else {
		isGnu = strings.Contains(string(out), "GNU tar")
	}

	tarCacheMu.Lock()
	tarCache[target] = isGnu
	tarCacheMu.Unlock()
	return isGnu
}

// _isLocalTarGnu checks if local tar is GNU tar. Result cached.
// Matches Python's CPService._is_local_tar_gnu().
func (s *CPService) _isLocalTarGnu() bool {
	tarCacheMu.RLock()
	isGnu, ok := tarCache["local"]
	tarCacheMu.RUnlock()
	if ok {
		return isGnu
	}

	cmd := exec.Command("tar", "--version")
	out, err := cmd.Output()
	if err != nil {
		isGnu = false
	} else {
		isGnu = strings.Contains(string(out), "GNU tar")
	}

	tarCacheMu.Lock()
	tarCache["local"] = isGnu
	tarCacheMu.Unlock()
	return isGnu
}

// ── Tar command builders ───────────────────────────────────────────

// _buildSourceTar builds the tar create command list for a local path.
// Matches Python's CPService._build_source_tar() exactly.
func (s *CPService) _buildSourceTar(path string, isDir bool, gnuExtras bool) []string {
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

// _buildRemoteSourceTar builds the tar create shell command string for a remote path.
// Matches Python's CPService._build_remote_source_tar().
func (s *CPService) _buildRemoteSourceTar(path string, isDir bool, gnuExtras bool) string {
	var extraFlags string
	if gnuExtras {
		parts := make([]string, len(gnuCreateExtras))
		for i, f := range gnuCreateExtras {
			parts[i] = shellQuote(f)
		}
		extraFlags = strings.Join(parts, " ")
	}

	quotedPath := shellQuote(path)
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
		return fmt.Sprintf("tar cf - %s -C %s %s", extraFlags, shellQuote(parent), shellQuote(base))
	}
	return fmt.Sprintf("tar cf - -C %s %s", shellQuote(parent), shellQuote(base))
}

// _buildDestTar builds the tar extract command list for a local destination.
// Matches Python's CPService._build_dest_tar() exactly.
func (s *CPService) _buildDestTar(dstPath string, gnuExtras bool, noOverwrite bool) []string {
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

// _buildRemoteDestTar builds the tar extract shell command string for a remote path.
// Matches Python's CPService._build_remote_dest_tar() exactly.
func (s *CPService) _buildRemoteDestTar(dstPath string, gnuExtras bool, noOverwrite bool) string {
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
	parts = append(parts, "-C", shellQuote(dstPath))
	return "tar xf - " + strings.Join(parts, " ")
}

// shellQuote matches Python's shlex.quote() exactly.
//
// Python's shlex.quote:
//   - Empty string → "”"
//   - If the string contains only safe characters (\w@%+=:,./-), return as-is
//   - Otherwise wrap in single quotes, escaping embedded single quotes as '"'"'
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if isSafe(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// isSafe checks if a string contains only shell-safe characters (matches
// Python's shlex._find_unsafe: r'[^\w@%+=:,./-]').
func isSafe(s string) bool {
	for _, r := range s {
		if !isShellSafeRune(r) {
			return false
		}
	}
	return true
}

// isShellSafeRune returns true for characters that are safe in shell arguments
// without quoting. Matches Python's shlex unsafe regex [^\w@%+=:,./-].
func isShellSafeRune(r rune) bool {
	if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
		return true
	}
	switch r {
	case '@', '%', '+', '=', ':', ',', '.', '/', '-':
		return true
	}
	return false
}

// ── Directory size helper ──────────────────────────────────────────

// _getDirectorySize returns the approximate total size of a directory
// by summing file sizes. Matches Python's CPService._get_directory_size().
func (s *CPService) _getDirectorySize(path string) int64 {
	var total int64
	filepath.Walk(path, func(fp string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible files, matching Python's OSError pass
		}
		if !fi.IsDir() {
			total += fi.Size()
		}
		return nil
	})
	return total
}

// ── Pipe with progress ─────────────────────────────────────────────

// _pipeWithProgress pipes source stdout into dest stdin, reporting progress.
// Matches Python's CPService._pipe_with_progress() exactly.
//
// Python:
//   - Reads src stdout in 64 KiB chunks
//   - Writes each chunk to dest stdin
//   - Calls on_progress(len(chunk)) when set
//   - Closes dest stdin and src stdout after copying
//   - Checks src exit code → CPError(code="cp.source_failed")
//   - Checks dest exit code → CPDestinationExistsError if
//     "Cannot open"/"Exists"/"File exists" in stderr, otherwise CPError
//
// checkDestExists controls the error classification (separate from pipe
// progress). Python passes checkDestExists implicitly by which copy method
// calls _pipe_with_progress. The behavior is:
//   - copy_host_to_vm: checkExists=true
//   - copy_vm_to_host and copy_vm_to_vm: checkExists=false
func (s *CPService) _pipeWithProgress(
	ctx context.Context,
	sourceCmd, destCmd []string,
	totalSize int64,
	onProgress func(int64),
	checkDestExists bool,
) error {
	srcProc := exec.CommandContext(ctx, sourceCmd[0], sourceCmd[1:]...)
	destProc := exec.CommandContext(ctx, destCmd[0], destCmd[1:]...)

	srcStdout, err := srcProc.StdoutPipe()
	if err != nil {
		return ErrCPFailed("Failed to set up pipe between processes")
	}
	destStdin, err := destProc.StdinPipe()
	if err != nil {
		return ErrCPFailed("Failed to set up pipe between processes")
	}
	destStderr, err := destProc.StderrPipe()
	if err != nil {
		return ErrCPFailed("Failed to set up pipe between processes")
	}

	if err := srcProc.Start(); err != nil {
		return ErrCPFailed("Failed to set up pipe between processes")
	}
	if err := destProc.Start(); err != nil {
		return ErrCPFailed("Failed to set up pipe between processes")
	}

	// Read src stdout in 64 KiB chunks, write to dest stdin
	buf := make([]byte, pipeChunkSize)
	for {
		n, readErr := srcStdout.Read(buf)
		if n > 0 {
			if _, writeErr := destStdin.Write(buf[:n]); writeErr != nil {
				break
			}
			if onProgress != nil {
				onProgress(int64(n))
			}
		}
		if readErr != nil {
			break
		}
	}

	destStdin.Close()
	srcStdout.Close()

	srcErr := srcProc.Wait()
	destErr := destProc.Wait()

	// Python reads dest stderr AFTER both processes finish (not concurrently):
	//   dest_stderr = dest_proc.stderr.read().decode() if dest_proc.stderr else ""
	destStderrStr := ""
	if destStderr != nil {
		stderrBytes, _ := io.ReadAll(destStderr)
		destStderrStr = strings.TrimSpace(string(stderrBytes))
	}

	if srcErr != nil {
		exitCode := 1
		if exitErr, ok := srcErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		// Python: raise CPError(f"Source tar process failed (exit {src_rc})", code="cp.source_failed")
		return ErrCPSourceFailed(exitCode)
	}
	if destErr != nil {
		exitCode := 1
		if exitErr, ok := destErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
		msg := destStderrStr
		if msg == "" {
			msg = fmt.Sprintf("Destination process failed (exit %d)", exitCode)
		}
		if strings.Contains(msg, "Cannot open") || strings.Contains(msg, "Exists") ||
			strings.Contains(msg, "File exists") {
			// Python: raise CPDestinationExistsError(f"Destination exists: {msg}", code="cp.destination_exists")
			return ErrCPDestinationExists(fmt.Sprintf("Destination exists: %s", msg))
		}
		// Python: raise CPError(msg, code="cp.destination_failed")
		return ErrCPDestinationFailed(msg)
	}

	return nil
}

// ── Copy directions ────────────────────────────────────────────────

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
	onProgress ProgressCallback,
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
				return 0, "", ErrCPSourceNotFound(fmt.Sprintf("Local path not found: %s", src))
			}
			return 0, "", err
		}
		// Python: if not os.path.isfile(p) and not os.path.isdir(p): raise CPSourceNotFoundError
		if !fi.Mode().IsRegular() && !fi.IsDir() {
			return 0, "", ErrCPSourceNotFound(fmt.Sprintf("Local path not found: %s", src))
		}
		isDir := fi.IsDir()
		size := int64(0)
		if isDir {
			size = s._getDirectorySize(src)
		} else {
			size = fi.Size()
		}
		sourceInfos = append(sourceInfos, sourceInfo{path: src, isDir: isDir, size: size})
		totalSize += size
	}

	sshPrefix := s._buildSSHPrefix(info.Host, info.User, info.KeyPath)
	remoteGnu := s._probeRemoteTar(sshPrefix)
	localGnu := s._isLocalTarGnu()

	isMulti := len(srcs) > 1

	// Build combined tar source
	var srcCmd []string
	if isMulti {
		srcCmd = s._buildMultiSourceTar(srcs, localGnu)
	} else {
		srcCmd = s._buildSourceTar(sourceInfos[0].path, sourceInfos[0].isDir, localGnu)
	}

	noOverwrite := !force
	remoteDestCmdStr := s._buildRemoteDestTar(dest, remoteGnu, noOverwrite)
	destCmd := append([]string{}, sshPrefix...)
	destCmd = append(destCmd, remoteDestCmdStr)

	if onProgress != nil {
		// Python progress path: _pipe_with_progress(src_cmd, dest_cmd, total_size, on_progress)
		if err := s._pipeWithProgress(ctx, srcCmd, destCmd, totalSize, onProgress, true); err != nil {
			return 0, "", err
		}
	} else {
		// Python non-progress path: inline bash pipe chain
		//   src_tar_str = " ".join(shlex.quote(a) for a in _build_source_tar(...))
		//   dest_tar_str = _build_remote_dest_tar(...)
		//   ssh_opts_str = " ".join(shlex.quote(a) for a in ssh_prefix)
		//   pipe_cmd = f"set -o pipefail && {src_tar_str} | {ssh_opts_str} {shlex.quote(dest_tar_str)}"
		//   run_cmd(["bash", "-c", pipe_cmd], capture=True, check=False)
		srcParts := make([]string, len(srcCmd))
		for i, a := range srcCmd {
			srcParts[i] = shellQuote(a)
		}
		dstParts := make([]string, len(destCmd))
		for i, a := range destCmd {
			dstParts[i] = shellQuote(a)
		}
		srcStr := strings.Join(srcParts, " ")
		dstStr := strings.Join(dstParts, " ")
		pipeCmd := fmt.Sprintf("set -o pipefail && %s | %s", srcStr, dstStr)

		bashCmd := exec.CommandContext(ctx, "bash", "-c", pipeCmd)
		var stderrBuf strings.Builder
		bashCmd.Stderr = &stderrBuf
		_, err := bashCmd.Output()
		if err != nil {
			stderr := strings.TrimSpace(stderrBuf.String())
			exitCode := 1
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			}
			// Python: check for "Cannot open"/"Exists" in stderr
			if stderr != "" && (strings.Contains(stderr, "Cannot open") || strings.Contains(stderr, "Exists")) {
				return 0, "", ErrCPDestinationExists(fmt.Sprintf("Destination exists: %s", stderr))
			}
			// Python: raise CPError(f"Copy failed (exit {rc}): {stderr}", code="cp.copy_failed")
			return 0, "", ErrCPCopyFailed(exitCode, stderr)
		}
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
	onProgress ProgressCallback,
) (int64, string, error) {
	sshPrefix := s._buildSSHPrefix(info.Host, info.User, info.KeyPath)
	remoteGnu := s._probeRemoteTar(sshPrefix)
	localGnu := s._isLocalTarGnu()

	// Probe remote path to determine type and size
	pathType, totalSize, err := s._probeRemotePath(sshPrefix, src)
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
		if err := os.MkdirAll(dstPathObj, 0755); err != nil {
			return 0, "", err
		}
	} else {
		// For files, check if the target file exists (Python: inline validation)
		if noOverwrite {
			if _, err := os.Stat(dstPathObj); err == nil {
				return 0, "", ErrCPDestinationExists(
					fmt.Sprintf("Local destination exists: %s. Use --force to overwrite.", dest),
				)
			}
		}
		// Ensure parent directory exists
		parentDir := filepath.Dir(dstPathObj)
		if parentDir == "" {
			parentDir = "."
		}
		if err := os.MkdirAll(parentDir, 0755); err != nil {
			return 0, "", err
		}
	}

	basename := filepath.Base(strings.TrimRight(src, "/"))
	remoteTarCmdStr := s._buildRemoteSourceTar(src, isDir, remoteGnu)

	// Build tar pipe
	srcCmd := append([]string{}, sshPrefix...)
	srcCmd = append(srcCmd, remoteTarCmdStr)

	var destCmd []string
	if isDir {
		destCmd = s._buildDestTar(dstPathObj, localGnu, noOverwrite)
	} else {
		destParent := filepath.Dir(dstPathObj)
		if destParent == "" {
			destParent = "."
		}
		destCmd = s._buildDestTar(destParent, localGnu, noOverwrite)
	}

	if onProgress != nil {
		// Python progress path: _pipe_with_progress
		if err := s._pipeWithProgress(ctx, srcCmd, destCmd, totalSize, onProgress, false); err != nil {
			return 0, "", err
		}
	} else {
		// Python non-progress path: inline bash pipe chain
		//   src_ssh_str = " ".join(shlex.quote(a) for a in ssh_prefix)
		//   dest_tar_str = " ".join(shlex.quote(a) for a in _build_dest_tar(...))
		//   pipe_cmd = f"set -o pipefail && {src_ssh_str} {shlex.quote(remote_tar_cmd)} | {dest_tar_str}"
		//   result = run_cmd(["bash", "-c", pipe_cmd], capture=True, check=False)
		srcParts := make([]string, len(srcCmd))
		for i, a := range srcCmd {
			srcParts[i] = shellQuote(a)
		}
		dstParts := make([]string, len(destCmd))
		for i, a := range destCmd {
			dstParts[i] = shellQuote(a)
		}
		srcStr := strings.Join(srcParts, " ")
		dstStr := strings.Join(dstParts, " ")
		pipeCmd := fmt.Sprintf("set -o pipefail && %s | %s", srcStr, dstStr)

		bashCmd := exec.CommandContext(ctx, "bash", "-c", pipeCmd)
		var stderrBuf strings.Builder
		bashCmd.Stderr = &stderrBuf
		_, err := bashCmd.Output()
		if err != nil {
			stderr := strings.TrimSpace(stderrBuf.String())
			exitCode := 1
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			}
			// Python: raise CPError(f"Copy failed (exit {rc}): {stderr}", code="cp.copy_failed")
			return 0, "", ErrCPCopyFailed(exitCode, stderr)
		}
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
	onProgress ProgressCallback,
) (int64, string, error) {
	noOverwrite := !force
	srcSSHPrefix := s._buildSSHPrefix(srcVMInfo.Host, srcVMInfo.User, srcVMInfo.KeyPath)
	destSSHPrefix := s._buildSSHPrefix(destVMInfo.Host, destVMInfo.User, destVMInfo.KeyPath)

	// Probe remote source to determine type
	pathType, totalSize, err := s._probeRemotePath(srcSSHPrefix, src)
	if err != nil {
		return 0, "", err
	}
	isDir := pathType == "DIR"

	// Build source tar command (remote)
	remoteGnu := s._probeRemoteTar(srcSSHPrefix)
	sourceTarCmdStr := s._buildRemoteSourceTar(src, isDir, remoteGnu)

	// Build dest tar command (remote)
	destGnu := s._probeRemoteTar(destSSHPrefix)
	destTarCmdStr := s._buildRemoteDestTar(dest, destGnu, noOverwrite)

	// Build pipe: ssh srcVM "tar cf - src" → ssh destVM "tar xf - -C dest"
	srcCmd := append([]string{}, srcSSHPrefix...)
	srcCmd = append(srcCmd, sourceTarCmdStr)

	destCmd := append([]string{}, destSSHPrefix...)
	destCmd = append(destCmd, destTarCmdStr)

	if onProgress != nil {
		// Python progress path: _pipe_with_progress
		if err := s._pipeWithProgress(ctx, srcCmd, destCmd, totalSize, onProgress, false); err != nil {
			return 0, "", err
		}
	} else {
		// Python non-progress path: inline bash pipe chain
		srcParts := make([]string, len(srcCmd))
		for i, a := range srcCmd {
			srcParts[i] = shellQuote(a)
		}
		dstParts := make([]string, len(destCmd))
		for i, a := range destCmd {
			dstParts[i] = shellQuote(a)
		}
		srcStr := strings.Join(srcParts, " ")
		dstStr := strings.Join(dstParts, " ")
		pipeCmd := fmt.Sprintf("set -o pipefail && %s | %s", srcStr, dstStr)

		bashCmd := exec.CommandContext(ctx, "bash", "-c", pipeCmd)
		var stderrBuf strings.Builder
		bashCmd.Stderr = &stderrBuf
		_, err := bashCmd.Output()
		if err != nil {
			stderr := strings.TrimSpace(stderrBuf.String())
			exitCode := 1
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			}
			// Python: raise CPError(f"Copy failed (exit {rc}): {stderr}", code="cp.copy_failed")
			return 0, "", ErrCPCopyFailed(exitCode, stderr)
		}
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

// sourceInfo holds metadata about a source path for multi-source copy.
type sourceInfo struct {
	path  string
	isDir bool
	size  int64
}

// _buildMultiSourceTar builds a combined tar command for multiple paths.
// Matches Python's CPService._build_multi_source_tar() exactly.
// Uses `-C <parent> <base>` for ALL paths (both files and directories),
// matching Python's behavior: parent = os.path.dirname(path) or ".",
// base = os.path.basename(path); cmd.extend(["-C", parent, base])
func (s *CPService) _buildMultiSourceTar(srcs []string, gnuExtras bool) []string {
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
