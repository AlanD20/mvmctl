package vsock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"mvmctl/internal/infra/event"
	"mvmctl/internal/service/vsockagent"
)

// FTResult holds the summary of a file transfer operation.
type FTResult struct {
	Files  int
	Bytes  int64
	Errors int
}

// fileEntry holds an absolute path and its relative path from the source root.
type fileEntry struct {
	absPath      string
	relativePath string
}

// expandSources walks each source path. Regular files are added as-is
// (relativePath = basename). Directories are walked recursively; each file
// gets a relativePath rooted at the directory parent.
//
// Symlinks are followed: symlinks to regular files copy the target content
// under the symlink's logical name; symlinks to directories recursively walk
// the target contents, preserving the symlink's name in the logical path.
// Broken symlinks and non-regular files (sockets, FIFOs, devices) are skipped
// with a warning. Symlink cycles are detected via a per-branch stack of
// resolved physical directory paths.
func expandSources(srcPaths []string) ([]fileEntry, error) {
	var entries []fileEntry
	for _, src := range srcPaths {
		fi, err := os.Lstat(src)
		if err != nil {
			return nil, fmt.Errorf("source not found: %s", src)
		}

		// Handle top-level symlink source.
		if fi.Mode()&os.ModeSymlink != 0 {
			tfi, err := os.Stat(src)
			if err != nil {
				slog.Warn("ft: skipping broken symlink source", "path", src)
				continue
			}
			if tfi.IsDir() {
				// Symlink to directory: walk contents through symlink path.
				baseDir := filepath.Dir(src)
				sub, err := walkWithSymlinks(baseDir, src, nil)
				if err != nil {
					return nil, fmt.Errorf("walk source %s: %w", src, err)
				}
				entries = append(entries, sub...)
			} else if tfi.Mode().IsRegular() {
				entries = append(entries, fileEntry{absPath: src, relativePath: filepath.Base(src)})
			} else {
				slog.Warn("ft: skipping non-regular symlink source", "path", src, "mode", tfi.Mode())
			}
			continue
		}

		if !fi.IsDir() {
			entries = append(entries, fileEntry{absPath: src, relativePath: filepath.Base(src)})
			continue
		}

		// Regular directory — walk with symlink awareness.
		baseDir := filepath.Dir(src)
		sub, err := walkWithSymlinks(baseDir, filepath.Clean(src), nil)
		if err != nil {
			return nil, fmt.Errorf("walk source %s: %w", src, err)
		}
		entries = append(entries, sub...)
	}
	return entries, nil
}

// walkWithSymlinks recursively walks a directory tree, handling symlinks
// according to the policy described in expandSources.
//
// baseDir is the directory containing the root of the walk (used for relative
// path computation — the same role as filepath.Dir of the original source).
// currentDir is the directory being walked right now.
// stack is a per-branch ancestry chain of resolved physical directory paths.
// A symlink that resolves to a path already in the stack is a cycle and is
// skipped. Sibling symlinks pointing to the same target are not cycles and
// are followed normally. Callers pass nil at the root.
func walkWithSymlinks(baseDir, currentDir string, stack []string) ([]fileEntry, error) {
	// Resolve to physical path for cycle detection.
	realPath, err := filepath.EvalSymlinks(currentDir)
	if err != nil {
		slog.Warn("ft: cannot resolve directory, skipping", "path", currentDir, "error", err)
		return nil, nil
	}
	if slices.Contains(stack, realPath) {
		slog.Warn("ft: symlink cycle detected, skipping", "path", currentDir)
		return nil, nil
	}

	// Build child stack (copy-on-write to preserve parent's slice).
	childStack := make([]string, len(stack), len(stack)+1)
	copy(childStack, stack)
	childStack = append(childStack, realPath)

	var entries []fileEntry

	f, err := os.Open(currentDir)
	if err != nil {
		return nil, fmt.Errorf("open directory %s: %w", currentDir, err)
	}
	defer f.Close()

	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", currentDir, err)
	}

	for _, name := range names {
		path := filepath.Join(currentDir, name)

		lfi, err := os.Lstat(path)
		if err != nil {
			slog.Warn("ft: cannot lstat entry, skipping", "path", path, "error", err)
			continue
		}

		// Handle symlinks explicitly.
		if lfi.Mode()&os.ModeSymlink != 0 {
			tfi, err := os.Stat(path)
			if err != nil {
				slog.Warn("ft: skipping broken symlink", "path", path)
				continue
			}
			if tfi.IsDir() {
				// Symlink to directory: walk target contents with symlink name as prefix.
				sub, err := walkWithSymlinks(baseDir, path, childStack)
				if err != nil {
					return nil, err
				}
				entries = append(entries, sub...)
			} else if tfi.Mode().IsRegular() {
				rel, _ := filepath.Rel(baseDir, path)
				entries = append(entries, fileEntry{absPath: path, relativePath: rel})
			} else {
				slog.Warn("ft: skipping non-regular symlink target", "path", path, "mode", tfi.Mode())
			}
			continue
		}

		// Regular directory.
		if lfi.IsDir() {
			sub, err := walkWithSymlinks(baseDir, path, childStack)
			if err != nil {
				return nil, err
			}
			entries = append(entries, sub...)
			continue
		}

		// Regular file.
		if lfi.Mode().IsRegular() {
			rel, _ := filepath.Rel(baseDir, path)
			entries = append(entries, fileEntry{absPath: path, relativePath: rel})
			continue
		}

		// Non-regular file (socket, FIFO, device, etc.).
		slog.Warn("ft: skipping non-regular file", "path", path, "mode", lfi.Mode())
	}

	return entries, nil
}

// --- FTCopyToVM (host -> VM push) ---

// FTCopyToVM copies files from the host to the VM using the binary frame protocol.
// It connects to the guest agent, performs a JSON handshake, then switches to
// binary frames to push each file.
//
// destPath determines the copy mode:
// - Trailing "/" or empty → directory mode: preserve source basename inside destPath
// - No trailing "/"       → file mode: use destPath as the exact destination filename.
// Multi-source with file mode returns an error.
func (c *Client) FTCopyToVM(
	ctx context.Context,
	srcPaths []string,
	destPath string,
	overwrite bool,
	noSync bool,
	onProgress event.OnDownloadCallback,
) (*FTResult, error) {
	conn, err := c.ensureAgent(ctx)
	if err != nil {
		slog.Error("ft: vsock dial and handshake failed", "vm_id", c.item.VmID, "error", err)
		return nil, fmt.Errorf("vsock connection failed: %w", err)
	}
	defer conn.Close()

	// --- JSON handshake ---
	req := execRequest{
		ID:    "1",
		Type:  requestTypeFileTransfer,
		Token: c.item.Token,
	}
	if err := sendFrame(conn, req); err != nil {
		slog.Error("ft: send handshake failed", "vm_id", c.item.VmID, "error", err)
		return nil, fmt.Errorf("send handshake failed: %w", err)
	}

	var resp execResponse
	if err := readFrame(conn, &resp); err != nil {
		slog.Error("ft: read handshake response failed", "vm_id", c.item.VmID, "error", err)
		return nil, fmt.Errorf("read handshake response failed: %w", err)
	}
	if resp.Type != responseTypeFTReady {
		return nil, fmt.Errorf("unexpected handshake response type: %s", resp.Type)
	}

	// --- Switch to binary frames ---

	// Validate: multiple sources require explicit directory mode (trailing /).
	if !strings.HasSuffix(destPath, "/") && len(srcPaths) > 1 {
		return nil, fmt.Errorf("multiple sources require a directory destination (trailing /)")
	}

	// Single directory source: auto-detect and force directory mode on dest.
	// Without this, a command like "mvm cp ./kubernetes node-a:/root/k8s" sends
	// /root/k8s (no trailing /) and the agent treats it as a regular file path.
	if len(srcPaths) == 1 && !strings.HasSuffix(destPath, "/") {
		if fi, stErr := os.Stat(srcPaths[0]); stErr == nil && fi.IsDir() {
			destPath += "/"
		}
	}

	// Send PUSH frame with raw dest path. The agent will stat the path
	// and decide whether to treat it as directory or file mode.
	pushPayload, _ := json.Marshal(vsockagent.FtPushPayload{
		Paths:     srcPaths,
		Dest:      destPath,
		Overwrite: overwrite,
		NoSync:    noSync,
	})
	if err := vsockagent.WriteFTFrame(conn, vsockagent.FtPush, pushPayload); err != nil {
		slog.Error("ft: write push frame", "error", err)
		return nil, fmt.Errorf("write push frame: %w", err)
	}

	// Read MKDIR acknowledgement.
	frameType, _, err := vsockagent.ReadFTFrame(conn)
	if err != nil {
		slog.Error("ft: read mkdir ack", "error", err)
		return nil, fmt.Errorf("read mkdir ack: %w", err)
	}
	if frameType != vsockagent.FtMkdir {
		return nil, fmt.Errorf("expected mkdir ack, got frame type 0x%02x", frameType)
	}

	// Expand source paths — walk directories to collect all regular files.
	entries, err := expandSources(srcPaths)
	if err != nil {
		return nil, fmt.Errorf("expand sources: %w", err)
	}

	var totalBytes int64
	var fileErrors int

	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("push cancelled: %w", ctx.Err())
		default:
		}

		fi, err := os.Stat(entry.absPath)
		if err != nil {
			slog.Warn("ft: source vanished between walk and read, skipping", "path", entry.absPath, "error", err)
			fileErrors++
			continue
		}

		fileSize := fi.Size()
		fileMode := int(fi.Mode().Perm())

		metaPath := entry.relativePath

		// Compute SHA-256.
		hasher := sha256.New()
		f, err := os.Open(entry.absPath)
		if err != nil {
			slog.Error("ft: open source", "path", entry.absPath, "error", err)
			errPayload, _ := json.Marshal(vsockagent.FtErrorPayload{Code: "open_failed", Message: err.Error()})
			_ = vsockagent.WriteFTFrame(conn, vsockagent.FtError, errPayload)
			fileErrors++
			continue
		}

		if _, err := io.Copy(hasher, f); err != nil {
			f.Close()
			return nil, fmt.Errorf("read source %s: %w", entry.absPath, err)
		}
		f.Close()
		hashHex := hex.EncodeToString(hasher.Sum(nil))

		// Send META frame.
		slog.Debug("ft: sending meta", "path", metaPath, "size", fileSize, "mode", fileMode)
		meta := vsockagent.FtMetaPayload{
			Path:   metaPath,
			Size:   fileSize,
			Mode:   fileMode,
			SHA256: hashHex,
		}
		metaPayload, _ := json.Marshal(meta)
		if err := vsockagent.WriteFTFrame(conn, vsockagent.FtMeta, metaPayload); err != nil {
			slog.Error("ft: write meta", "error", err)
			return nil, fmt.Errorf("write meta frame: %w", err)
		}

		// Read acceptance from agent.
		slog.Debug("ft: reading accept", "path", metaPath)
		frameType, acceptPayload, err := vsockagent.ReadFTFrame(conn)
		if err != nil {
			slog.Error("ft: read accept", "error", err)
			return nil, fmt.Errorf("read accept frame: %w", err)
		}

		slog.Debug("ft: accept frame received", "path", metaPath, "type", fmt.Sprintf("0x%02x", frameType))
		if frameType == vsockagent.FtError {
			fileErrors++
			slog.Warn("ft: agent rejected file", "path", metaPath)
			continue
		}
		if frameType != vsockagent.FtMeta {
			return nil, fmt.Errorf("expected meta/accept frame, got 0x%02x", frameType)
		}

		var accept vsockagent.FtMetaPayload
		if err := json.Unmarshal(acceptPayload, &accept); err != nil {
			return nil, fmt.Errorf("parse accept: %w", err)
		}
		if !accept.Accepted {
			fileErrors++
			slog.Warn("ft: agent declined file", "path", metaPath)
			continue
		}

		// Re-open source for reading and stream.
		f, err = os.Open(entry.absPath)
		if err != nil {
			slog.Error("ft: re-open source", "path", entry.absPath, "error", err)
			errPayload, _ := json.Marshal(vsockagent.FtErrorPayload{Code: "open_failed", Message: err.Error()})
			_ = vsockagent.WriteFTFrame(conn, vsockagent.FtError, errPayload)
			fileErrors++
			continue
		}

		buf := make([]byte, ftBufferSize)
		var sentBytes int64
		for {
			n, readErr := f.Read(buf)
			if n > 0 {
				slog.Debug("ft: sending data chunk", "path", metaPath, "bytes", n)
				if err := vsockagent.WriteFTFrame(conn, vsockagent.FtData, buf[:n]); err != nil {
					f.Close()
					slog.Error("ft: write data", "error", err)
					return nil, fmt.Errorf("write data frame: %w", err)
				}
				sentBytes += int64(n)
				totalBytes += int64(n)

				if onProgress != nil {
					onProgress(sentBytes, fileSize)
				}
			}
			if readErr != nil {
				if readErr == io.EOF {
					break
				}
				f.Close()
				slog.Error("ft: read source", "error", readErr)
				return nil, fmt.Errorf("read source %s: %w", entry.absPath, readErr)
			}
		}
		_ = f.Close() // best-effort: file was written successfully, close error is non-fatal

		// Send end-of-stream signal (empty DATA frame).
		slog.Debug("ft: sending eos", "path", metaPath)
		if err := vsockagent.WriteFTFrame(conn, vsockagent.FtData, nil); err != nil {
			slog.Error("ft: write eos", "error", err)
			return nil, fmt.Errorf("write eos frame: %w", err)
		}

		// Read OK from agent.
		slog.Debug("ft: reading ok", "path", metaPath)
		frameType, okPayload, err := vsockagent.ReadFTFrame(conn)
		if err != nil {
			slog.Error("ft: read ok", "error", err)
			return nil, fmt.Errorf("read ok frame: %w", err)
		}
		slog.Debug("ft: ok frame received", "path", metaPath, "type", fmt.Sprintf("0x%02x", frameType))
		if frameType == vsockagent.FtError {
			fileErrors++
			var errPayload vsockagent.FtErrorPayload
			if json.Unmarshal(okPayload, &errPayload) == nil {
				slog.Warn("ft: agent error for file", "path", metaPath, "code", errPayload.Code)
			}
			continue
		}
		if frameType != vsockagent.FtOK {
			return nil, fmt.Errorf("expected ok frame, got 0x%02x", frameType)
		}

		// Verify agent's SHA-256 and byte count match what we sent.
		var okMeta vsockagent.FtMetaPayload
		if json.Unmarshal(okPayload, &okMeta) == nil {
			if okMeta.SHA256 != "" && okMeta.SHA256 != hashHex {
				return nil, fmt.Errorf("SHA-256 mismatch for %s: local=%s agent=%s",
					metaPath, hashHex, okMeta.SHA256)
			}
			if okMeta.Size > 0 && okMeta.Size != fileSize {
				slog.Warn("ft: agent byte count mismatch",
					"path", metaPath, "sent", fileSize, "agent_reported", okMeta.Size)
			}
		}
	}

	// Send DONE.
	slog.Debug("ft: sending done", "files", len(entries)-fileErrors, "bytes", totalBytes, "errors", fileErrors)
	donePayload, _ := json.Marshal(vsockagent.FtDonePayload{
		Files:  len(entries) - fileErrors,
		Bytes:  totalBytes,
		Errors: fileErrors,
	})
	if err := vsockagent.WriteFTFrame(conn, vsockagent.FtDone, donePayload); err != nil {
		slog.Error("ft: write done", "error", err)
		return nil, fmt.Errorf("write done frame: %w", err)
	}

	result := &FTResult{
		Files:  len(entries) - fileErrors,
		Bytes:  totalBytes,
		Errors: fileErrors,
	}
	slog.Debug("ft: processed entries", "count", len(entries))
	slog.Info("ft: push complete", "files", result.Files, "bytes", result.Bytes, "errors", result.Errors)
	return result, nil
}

// --- FTCopyFromVM (VM -> host pull) ---

// FTCopyFromVM copies a file or directory from the VM to the host using the
// binary frame protocol.
//
// Directory mode is triggered when destPath ends with "/" or is an existing
// directory, or when srcPath ends with "/" as an explicit hint. In directory
// mode the guest agent streams one or more files and they are placed under
// destPath using the relative path from the VM source.
//
// Single-file mode sends one META → accept → DATA/EOS → OK cycle, then reads
// DONE. Directory mode sends N cycles (one per file) followed by DONE.
func (c *Client) FTCopyFromVM(
	ctx context.Context,
	srcPath string,
	destPath string,
	overwrite bool,
	onProgress event.OnDownloadCallback,
) (*FTResult, error) {
	conn, err := c.ensureAgent(ctx)
	if err != nil {
		slog.Error("ft: vsock dial and handshake failed", "vm_id", c.item.VmID, "error", err)
		return nil, fmt.Errorf("vsock connection failed: %w", err)
	}
	defer conn.Close()

	// --- JSON handshake ---
	req := execRequest{
		ID:    "1",
		Type:  requestTypeFileTransfer,
		Token: c.item.Token,
	}
	if err := sendFrame(conn, req); err != nil {
		slog.Error("ft: send handshake failed", "vm_id", c.item.VmID, "error", err)
		return nil, fmt.Errorf("send handshake failed: %w", err)
	}

	var resp execResponse
	if err := readFrame(conn, &resp); err != nil {
		slog.Error("ft: read handshake response failed", "vm_id", c.item.VmID, "error", err)
		return nil, fmt.Errorf("read handshake response failed: %w", err)
	}
	if resp.Type != responseTypeFTReady {
		return nil, fmt.Errorf("unexpected handshake response type: %s", resp.Type)
	}

	// --- Determine destination directory mode ---
	// Directory mode: dest ends with "/" or is an existing directory.
	// File mode: dest has no trailing "/" and is not an existing directory.
	destIsDir := strings.HasSuffix(destPath, "/")
	if !destIsDir {
		if fi, stErr := os.Stat(destPath); stErr == nil && fi.IsDir() {
			destIsDir = true
		}
	}

	// Ask the agent to stream recursively when either the destination is a
	// directory (so one or more files will land inside it) or the caller
	// explicitly requested directory mode with a trailing slash on srcPath.
	// The agent still stat()s the source and decides whether to walk it.
	isRecursive := destIsDir || strings.HasSuffix(srcPath, "/")

	// --- Switch to binary frames ---

	// Send PULL frame.
	pullPayload, _ := json.Marshal(vsockagent.FtPullPayload{
		Path:      srcPath,
		Dest:      destPath,
		Overwrite: overwrite,
		Recursive: isRecursive,
	})
	if err := vsockagent.WriteFTFrame(conn, vsockagent.FtPull, pullPayload); err != nil {
		slog.Error("ft: write pull frame", "error", err)
		return nil, fmt.Errorf("write pull frame: %w", err)
	}

	var totalFiles, fileErrors int
	var totalBytes int64

mainLoop:
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("pull cancelled: %w", ctx.Err())
		default:
		}

		frameType, payload, err := vsockagent.ReadFTFrame(conn)
		if err != nil {
			slog.Error("ft: read frame", "error", err)
			return nil, fmt.Errorf("read frame: %w", err)
		}

		switch frameType {
		case vsockagent.FtDone:
			// All files processed — return host-side bookkeeping.
			break mainLoop

		case vsockagent.FtError:
			var errPayload vsockagent.FtErrorPayload
			if json.Unmarshal(payload, &errPayload) == nil {
				return nil, fmt.Errorf("agent error: %s: %s", errPayload.Code, errPayload.Message)
			}
			return nil, fmt.Errorf("agent error during pull")

		case vsockagent.FtMeta:
			// Process one file.
			fileResult, fileErr := c.receivePullFile(ctx, conn, payload, destPath, destIsDir, overwrite, onProgress)
			if fileErr != nil {
				return nil, fileErr
			}
			totalFiles += fileResult.Files
			totalBytes += fileResult.Bytes
			fileErrors += fileResult.Errors

		default:
			return nil, fmt.Errorf("unexpected frame type during pull: 0x%02x", frameType)
		}
	}

	result := &FTResult{
		Files:  totalFiles,
		Bytes:  totalBytes,
		Errors: fileErrors,
	}
	slog.Info("ft: pull complete", "path", srcPath,
		"files", result.Files, "bytes", result.Bytes, "errors", result.Errors)
	return result, nil
}

// receivePullFile handles one META frame during a pull: resolves the
// destination path, checks overwrite, sends accept, receives data frames,
// verifies SHA-256, and sends OK. Returns an FTResult for the single file
// (1 success or 1 error) or an error for connection-fatal failures.
func (c *Client) receivePullFile(
	ctx context.Context,
	conn net.Conn,
	metaPayload []byte,
	destPath string,
	destIsDir bool,
	overwrite bool,
	onProgress event.OnDownloadCallback,
) (*FTResult, error) {
	var meta vsockagent.FtMetaPayload
	if err := json.Unmarshal(metaPayload, &meta); err != nil {
		return nil, fmt.Errorf("parse meta: %w", err)
	}

	slog.Debug("ft: pull meta", "path", meta.Path, "size", meta.Size,
		"mode", meta.Mode, "sha256", meta.SHA256)

	// Resolve full destination path.
	var fullDestPath string
	if destIsDir {
		fullDestPath = filepath.Join(destPath, meta.Path)
	} else {
		fullDestPath = destPath
	}

	// Check overwrite.
	if !overwrite {
		if _, err := os.Stat(fullDestPath); err == nil {
			errPayload, _ := json.Marshal(vsockagent.FtErrorPayload{
				Code:    "exists",
				Message: fmt.Sprintf("file exists: %s", fullDestPath),
			})
			_ = vsockagent.WriteFTFrame(conn, vsockagent.FtError, errPayload)
			return &FTResult{Files: 0, Bytes: 0, Errors: 1}, nil
		}
	}

	// Send acceptance.
	acceptPayload, _ := json.Marshal(vsockagent.FtMetaPayload{Accepted: true})
	if err := vsockagent.WriteFTFrame(conn, vsockagent.FtMeta, acceptPayload); err != nil {
		slog.Error("ft: write accept", "error", err)
		return nil, fmt.Errorf("write accept frame: %w", err)
	}

	// Create destination directory if needed.
	if err := os.MkdirAll(filepath.Dir(fullDestPath), 0755); err != nil {
		slog.Error("ft: mkdir dest", "path", filepath.Dir(fullDestPath), "error", err)
		return nil, fmt.Errorf("create destination directory: %w", err)
	}

	// Open/create local file.
	f, err := os.OpenFile(fullDestPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(meta.Mode))
	if err != nil {
		slog.Error("ft: create dest file", "path", fullDestPath, "error", err)
		return nil, fmt.Errorf("create destination file: %w", err)
	}

	// Receive frames in loop.
	hasher := sha256.New()
	var fileBytes int64
	hasError := false
receiveLoop:
	for {
		select {
		case <-ctx.Done():
			f.Close()
			return nil, fmt.Errorf("pull cancelled: %w", ctx.Err())
		default:
		}
		frameType, chunk, err := vsockagent.ReadFTFrame(conn)
		if err != nil {
			f.Close()
			slog.Error("ft: read data frame", "error", err)
			return nil, fmt.Errorf("read data frame: %w", err)
		}

		switch frameType {
		case vsockagent.FtData:
			// Empty payload signals end of file.
			if len(chunk) == 0 {
				break receiveLoop
			}
			n, writeErr := f.Write(chunk)
			if writeErr != nil {
				f.Close()
				slog.Error("ft: write chunk", "error", writeErr)
				return nil, fmt.Errorf("write file chunk: %w", writeErr)
			}
			hasher.Write(chunk[:n])
			fileBytes += int64(n)

		case vsockagent.FtProgress:
			var prog vsockagent.FtProgressPayload
			if json.Unmarshal(chunk, &prog) == nil {
				if onProgress != nil {
					onProgress(prog.Bytes, prog.Total)
				}
			}

		case vsockagent.FtError:
			f.Close()
			_ = os.Remove(fullDestPath) // clean up partial file
			var errPayload vsockagent.FtErrorPayload
			if json.Unmarshal(chunk, &errPayload) == nil {
				slog.Warn("ft: agent error for file", "path", meta.Path,
					"code", errPayload.Code, "message", errPayload.Message)
			}
			hasError = true
			break receiveLoop

		default:
			f.Close()
			return nil, fmt.Errorf("unexpected frame type during pull: 0x%02x", frameType)
		}
	}

	f.Close()

	if hasError {
		return &FTResult{Files: 0, Bytes: 0, Errors: 1}, nil
	}

	// fsync not needed here because the file is already closed.
	// The guest agent's sync() handles the backing store.

	// Verify SHA-256 against file meta.
	localHash := hex.EncodeToString(hasher.Sum(nil))
	if meta.SHA256 != "" && localHash != meta.SHA256 {
		_ = os.Remove(fullDestPath) // clean up partial file
		return nil, fmt.Errorf("SHA-256 mismatch for %s: local=%s remote=%s",
			meta.Path, localHash, meta.SHA256)
	}

	// Send OK back to agent.
	okPayload, _ := json.Marshal(vsockagent.FtMetaPayload{
		Path:   meta.Path,
		Size:   fileBytes,
		SHA256: hex.EncodeToString(hasher.Sum(nil)),
	})
	if err := vsockagent.WriteFTFrame(conn, vsockagent.FtOK, okPayload); err != nil {
		slog.Error("ft: write ok", "error", err)
		return nil, fmt.Errorf("write ok frame: %w", err)
	}

	return &FTResult{Files: 1, Bytes: fileBytes, Errors: 0}, nil
}

// --- FTCopyVMToVM (VM -> VM relay) ---

// FTCopyVMToVM copies a file from one VM to another via the host as a relay.
// It connects to both VMs and relays binary frames between them.
//
// destPath determines the copy mode:
// - Trailing "/" or empty → directory mode: preserve source basename inside destPath
// - No trailing "/"       → file mode: use destPath as the exact destination filename
func (c *Client) FTCopyVMToVM(
	ctx context.Context,
	srcPath string,
	destPath string,
	overwrite bool,
	onProgress event.OnDownloadCallback,
	destClient *Client,
) (*FTResult, error) {
	// Connect to source VM.
	srcConn, err := c.ensureAgent(ctx)
	if err != nil {
		slog.Error("ft: source vsock dial failed", "vm_id", c.item.VmID, "error", err)
		return nil, fmt.Errorf("source vsock connection failed: %w", err)
	}
	defer srcConn.Close()

	// Connect to destination VM.
	dstConn, err := destClient.ensureAgent(ctx)
	if err != nil {
		slog.Error("ft: dest vsock dial failed", "vm_id", destClient.item.VmID, "error", err)
		return nil, fmt.Errorf("dest vsock connection failed: %w", err)
	}
	defer dstConn.Close()

	// --- JSON handshake for both ---
	req := execRequest{ID: "1", Type: requestTypeFileTransfer, Token: c.item.Token}
	if err := sendFrame(srcConn, req); err != nil {
		return nil, fmt.Errorf("source handshake failed: %w", err)
	}
	var srcResp execResponse
	if err := readFrame(srcConn, &srcResp); err != nil || srcResp.Type != responseTypeFTReady {
		return nil, fmt.Errorf("source handshake response failed: %w", err)
	}

	dstReq := execRequest{ID: "1", Type: requestTypeFileTransfer, Token: destClient.item.Token}
	if err := sendFrame(dstConn, dstReq); err != nil {
		return nil, fmt.Errorf("dest handshake failed: %w", err)
	}
	var dstResp execResponse
	if err := readFrame(dstConn, &dstResp); err != nil || dstResp.Type != responseTypeFTReady {
		return nil, fmt.Errorf("dest handshake response failed: %w", err)
	}

	// --- Binary: send PULL to source, PUSH to dest ---

	// Send PULL to source.
	pullPayload, _ := json.Marshal(vsockagent.FtPullPayload{Path: srcPath, Overwrite: overwrite})
	if err := vsockagent.WriteFTFrame(srcConn, vsockagent.FtPull, pullPayload); err != nil {
		return nil, fmt.Errorf("source pull frame: %w", err)
	}

	// Send PUSH to dest with empty paths (we'll forward meta from source).
	// Send raw destPath — the dest agent will stat the path and decide mode.
	pushPayload, _ := json.Marshal(vsockagent.FtPushPayload{
		Paths:     []string{},
		Dest:      destPath,
		Overwrite: overwrite,
	})
	if err := vsockagent.WriteFTFrame(dstConn, vsockagent.FtPush, pushPayload); err != nil {
		return nil, fmt.Errorf("dest push frame: %w", err)
	}

	// Read MKDIR from dest.
	frameType, _, err := vsockagent.ReadFTFrame(dstConn)
	if err != nil {
		return nil, fmt.Errorf("read dest mkdir: %w", err)
	}
	if frameType != vsockagent.FtMkdir {
		return nil, fmt.Errorf("expected dest mkdir, got 0x%02x", frameType)
	}

	// Read META from source.
	frameType, srcMetaPayload, err := vsockagent.ReadFTFrame(srcConn)
	if err != nil {
		return nil, fmt.Errorf("read source meta: %w", err)
	}
	if frameType == vsockagent.FtError {
		return nil, fmt.Errorf("source error during pull")
	}
	if frameType != vsockagent.FtMeta {
		return nil, fmt.Errorf("expected source meta, got 0x%02x", frameType)
	}

	// Forward source META to dest as-is. The dest agent stats the dest
	// path it received in the push payload and decides mode.
	fwdMetaPayload := srcMetaPayload

	// Forward META to dest.
	if err := vsockagent.WriteFTFrame(dstConn, vsockagent.FtMeta, fwdMetaPayload); err != nil {
		return nil, fmt.Errorf("forward meta to dest: %w", err)
	}

	// Read acceptance from dest.
	frameType, dstAcceptPayload, err := vsockagent.ReadFTFrame(dstConn)
	if err != nil {
		return nil, fmt.Errorf("read dest accept: %w", err)
	}
	if frameType == vsockagent.FtError {
		errPayload, _ := json.Marshal(vsockagent.FtErrorPayload{
			Code:    "dest_rejected",
			Message: "destination rejected file",
		})
		_ = vsockagent.WriteFTFrame(srcConn, vsockagent.FtError, errPayload)
		return nil, fmt.Errorf("destination rejected file")
	}
	if frameType != vsockagent.FtMeta {
		return nil, fmt.Errorf("expected dest accept meta, got 0x%02x", frameType)
	}

	// Forward acceptance to source.
	if err := vsockagent.WriteFTFrame(srcConn, vsockagent.FtMeta, dstAcceptPayload); err != nil {
		return nil, fmt.Errorf("forward accept to source: %w", err)
	}

	// Relay loop: read from source, forward to dest.
	var totalBytes int64
relayLoop:
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("relay cancelled: %w", ctx.Err())
		default:
		}
		frameType, chunk, err := vsockagent.ReadFTFrame(srcConn)
		if err != nil {
			return nil, fmt.Errorf("relay read from source: %w", err)
		}

		switch frameType {
		case vsockagent.FtData:
			if err := vsockagent.WriteFTFrame(dstConn, vsockagent.FtData, chunk); err != nil {
				return nil, fmt.Errorf("relay write data to dest: %w", err)
			}
			// Empty payload signals end of file — complete relay.
			if len(chunk) == 0 {
				// Read OK from dest.
				ft, okPayload, err := vsockagent.ReadFTFrame(dstConn)
				if err != nil {
					return nil, fmt.Errorf("relay read dest ok: %w", err)
				}
				if ft == vsockagent.FtError {
					return nil, fmt.Errorf("dest error during relay")
				}
				if ft != vsockagent.FtOK {
					return nil, fmt.Errorf("expected dest ok, got 0x%02x", ft)
				}
				// Forward OK to source.
				if err := vsockagent.WriteFTFrame(srcConn, vsockagent.FtOK, okPayload); err != nil {
					return nil, fmt.Errorf("relay forward ok to source: %w", err)
				}
				break relayLoop
			}
			totalBytes += int64(len(chunk))

		case vsockagent.FtProgress:
			var prog vsockagent.FtProgressPayload
			if json.Unmarshal(chunk, &prog) == nil {
				if onProgress != nil {
					onProgress(prog.Bytes, prog.Total)
				}
			}

		case vsockagent.FtError:
			// Forward error to dest.
			_ = vsockagent.WriteFTFrame(dstConn, vsockagent.FtError, chunk)
			var errPayload vsockagent.FtErrorPayload
			if json.Unmarshal(chunk, &errPayload) == nil {
				return nil, fmt.Errorf("source error: %s: %s", errPayload.Code, errPayload.Message)
			}
			return nil, fmt.Errorf("source error during relay")

		default:
			return nil, fmt.Errorf("unexpected frame type in relay: 0x%02x", frameType)
		}
	}

	// Read DONE from source, send DONE to dest, read dest's echoed DONE.
	_, _, err = vsockagent.ReadFTFrame(srcConn)
	if err != nil {
		return nil, fmt.Errorf("read source done: %w", err)
	}
	donePayload, _ := json.Marshal(vsockagent.FtDonePayload{Files: 1, Bytes: totalBytes, Errors: 0})
	if err := vsockagent.WriteFTFrame(dstConn, vsockagent.FtDone, donePayload); err != nil {
		return nil, fmt.Errorf("write dest done: %w", err)
	}
	// Dest agent echoes DONE back — read and discard.
	_, _, _ = vsockagent.ReadFTFrame(dstConn)

	result := &FTResult{
		Files:  1,
		Bytes:  totalBytes,
		Errors: 0,
	}

	slog.Info("ft: relay complete", "src", srcPath, "dest", destPath,
		"bytes", result.Bytes)
	return result, nil
}

// TestExport — exposed for testing.
var ExpandSources = expandSources
