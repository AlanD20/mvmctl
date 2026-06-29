// Package vsockagent — guest agent inside the Firecracker microVM.
// File transfer via binary frame protocol over the same vsock connection
// after the initial JSON handshake.
package vsockagent

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
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

	"golang.org/x/sys/unix"
)

// --- Binary frame type constants ---
// Single source of truth. Host side (internal/core/vsock) imports these as
// vsockagent.FtPush, vsockagent.FtPull, etc.
const (
	FtPush     byte = 0x10 // Push request (host→VM)
	FtPull     byte = 0x11 // Pull request (VM→host)
	FtMeta     byte = 0x20 // File metadata / acceptance
	FtData     byte = 0x21 // Raw binary data chunk
	FtOK       byte = 0x22 // File transfer OK / verification
	FtMkdir    byte = 0x30 // Directory creation
	FtSymlink  byte = 0x31 // Symlink creation
	FtError    byte = 0x40 // Error
	FtProgress byte = 0x50 // Progress update
	FtDone     byte = 0x60 // Transfer complete
)

// --- Binary frame helpers ---

// readFTFrame reads one binary frame from r. Returns frame type and payload.
// Frame format: [4 bytes big-endian total_length][1 byte type][N bytes payload].
// total_length includes the type byte (payload_len + 1).
func ReadFTFrame(r io.Reader) (byte, []byte, error) {
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return 0, nil, fmt.Errorf("read frame length: %w", err)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return 0, nil, fmt.Errorf("read frame body: %w", err)
	}
	return buf[0], buf[1:], nil
}

// writeFTFrame writes one binary frame to w.
func WriteFTFrame(w io.Writer, frameType byte, payload []byte) error {
	length := uint32(len(payload) + 1) // +1 for the frame type byte
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], length)
	if _, err := w.Write(header[:]); err != nil {
		return fmt.Errorf("write frame header: %w", err)
	}
	if _, err := w.Write([]byte{frameType}); err != nil {
		return fmt.Errorf("write frame type: %w", err)
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return fmt.Errorf("write frame payload: %w", err)
		}
	}
	return nil
}

// --- Push/Pull JSON payload types ---

// FtPushPayload is the JSON payload for an FtPush frame from the host.
type FtPushPayload struct {
	Paths     []string `json:"paths"`
	Dest      string   `json:"dest"`
	Overwrite bool     `json:"overwrite"`
	NoSync    bool     `json:"no_sync,omitempty"`
}

// FtPullPayload is the JSON payload for an FtPull frame from the host.
type FtPullPayload struct {
	Path      string `json:"path"`
	Dest      string `json:"dest"`
	Overwrite bool   `json:"overwrite"`
	Recursive bool   `json:"recursive,omitempty"`
}

// FtMetaPayload is the file metadata sent in an FtMeta frame.
type FtMetaPayload struct {
	Path   string `json:"path,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Mode   int    `json:"mode,omitempty"`
	SHA256 string `json:"sha256,omitempty"`

	// Acceptance response
	Accepted bool `json:"accepted,omitempty"`
}

// FtProgressPayload is the payload for FtProgress frames.
type FtProgressPayload struct {
	Path  string `json:"path,omitempty"`
	Bytes int64  `json:"bytes"`
	Total int64  `json:"total"`
}

// FtErrorPayload is the payload for FtError frames.
type FtErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// FtDonePayload is the payload for FtDone frames.
type FtDonePayload struct {
	Files  int   `json:"files"`
	Bytes  int64 `json:"bytes"`
	Errors int   `json:"errors"`
}

// --- File transfer handler ---

// handleFileTransfer dispatches the first binary frame to push or pull handler.
func handleFileTransfer(ctx context.Context, conn net.Conn, req *execRequest) {
	frameType, payload, err := ReadFTFrame(conn)
	if err != nil {
		slog.Error("ft: read first frame", "id", req.ID, "error", err)
		return
	}

	switch frameType {
	case FtPush:
		handleFTPush(ctx, conn, payload)
	case FtPull:
		handleFTPull(ctx, conn, payload)
	default:
		slog.Error("ft: unknown frame type", "id", req.ID, "type", fmt.Sprintf("0x%02x", frameType))
		errPayload, _ := json.Marshal(FtErrorPayload{
			Code:    "invalid",
			Message: fmt.Sprintf("unknown frame type: 0x%02x", frameType),
		})
		_ = WriteFTFrame(conn, FtError, errPayload)
	}
}

// --- Push handler (host uploads files to VM) ---

func handleFTPush(ctx context.Context, conn net.Conn, pushPayload []byte) {
	var push FtPushPayload
	if err := json.Unmarshal(pushPayload, &push); err != nil {
		slog.Error("ft: parse push payload", "error", err)
		errPayload, _ := json.Marshal(FtErrorPayload{Code: "invalid", Message: "invalid push payload"})
		_ = WriteFTFrame(conn, FtError, errPayload)
		return
	}

	slog.Info("ft: push start", "paths", push.Paths, "dest", push.Dest, "overwrite", push.Overwrite)

	// --- Determine mode via stat ---
	// Directory mode: dest ends with "/" or is an existing directory.
	// File mode:      dest has no trailing "/", does not exist, is not a directory.

	userWantsDir := strings.HasSuffix(push.Dest, "/")
	dest := strings.TrimSuffix(push.Dest, "/")

	var (
		fileLoopDir string // base directory to create via MkdirAll
		destIsDir   bool
	)

	if userWantsDir {
		// User explicitly requested directory mode (trailing /).
		destIsDir = true
		fileLoopDir = dest
	} else {
		// No trailing / — stat to determine if it's an existing directory.
		fi, err := os.Stat(dest)
		if err == nil && fi.IsDir() {
			destIsDir = true
			fileLoopDir = dest
		} else if len(push.Paths) > 1 {
			// Multiple source files but dest isn't a directory — error.
			errPayload, _ := json.Marshal(FtErrorPayload{
				Code:    "not_a_directory",
				Message: fmt.Sprintf("'%s' is not a directory", push.Dest),
			})
			_ = WriteFTFrame(conn, FtError, errPayload)
			return
		} else {
			// File mode: write to exact path, create parent dirs only.
			destIsDir = false
			fileLoopDir = filepath.Dir(dest) // parent for MkdirAll
		}
	}

	// Create destination base directory (or parent in file mode).
	if err := os.MkdirAll(fileLoopDir, 0755); err != nil {
		slog.Error("ft: mkdir dest", "dir", fileLoopDir, "error", err)
		errPayload, _ := json.Marshal(FtErrorPayload{Code: "mkdir_failed", Message: err.Error()})
		_ = WriteFTFrame(conn, FtError, errPayload)
		return
	}

	// Send MKDIR acknowledgement.
	mkdirPayload, _ := json.Marshal(map[string]string{
		"path": fileLoopDir,
	})
	if err := WriteFTFrame(conn, FtMkdir, mkdirPayload); err != nil {
		slog.Error("ft: write mkdir ack", "error", err)
		return
	}

	var totalBytes int64
	var fileErrors int
	var fileSuccess int

mainLoop:
	for {
		select {
		case <-ctx.Done():
			slog.Warn("ft: push cancelled mid-transfer", "error", ctx.Err())
			return
		default:
		}

		// Read META frame from host.
		frameType, metaPayload, err := ReadFTFrame(conn)
		if err != nil {
			slog.Error("ft: read meta frame", "error", err)
			return
		}
		slog.Debug("ft: mainLoop read frame", "type", fmt.Sprintf("0x%02x", frameType))
		if frameType == FtDone {
			slog.Debug("ft: received done, breaking mainLoop")
			break mainLoop
		}
		if frameType == FtError {
			fileErrors++
			continue mainLoop
		}
		if frameType != FtMeta {
			slog.Error("ft: expected meta frame", "got", fmt.Sprintf("0x%02x", frameType))
			return
		}

		var meta FtMetaPayload
		if err := json.Unmarshal(metaPayload, &meta); err != nil {
			slog.Error("ft: parse meta", "error", err)
			return
		}

		slog.Debug("ft: meta received", "path", meta.Path, "size", meta.Size, "mode", meta.Mode)

		var destPath string
		if destIsDir {
			destPath = filepath.Join(dest, meta.Path)
		} else {
			destPath = dest // exact path, ignore meta.Path
		}

		// Check overwrite.
		if !push.Overwrite {
			if _, err := os.Stat(destPath); err == nil {
				slog.Warn("ft: file exists, skipping", "path", destPath)
				errPayload, _ := json.Marshal(FtErrorPayload{
					Code:    "exists",
					Message: fmt.Sprintf("file exists: %s", destPath),
				})
				_ = WriteFTFrame(conn, FtError, errPayload)
				fileErrors++
				continue mainLoop
			}
		}

		// Create parent directories if needed.
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			slog.Error("ft: mkdir parent", "path", filepath.Dir(destPath), "error", err)
			errPayload, _ := json.Marshal(FtErrorPayload{Code: "mkdir_failed", Message: err.Error()})
			_ = WriteFTFrame(conn, FtError, errPayload)
			fileErrors++
			continue mainLoop
		}

		// Open destination file.
		// Try without O_CREAT first to avoid fs.protected_regular=2 EACCES on
		// Ubuntu 24.04 (kernel blocks O_CREAT for files owned by another user
		// in sticky world-writable directories). Fall back to O_CREATE if the
		// file does not exist.
		f, err := os.OpenFile(destPath, os.O_WRONLY|os.O_TRUNC, 0)
		if err != nil {
			if os.IsNotExist(err) {
				f, err = os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(meta.Mode))
			}
			if err != nil {
				slog.Error("ft: create file", "path", destPath, "error", err)
				errPayload, _ := json.Marshal(FtErrorPayload{Code: "create_failed", Message: err.Error()})
				_ = WriteFTFrame(conn, FtError, errPayload)
				fileErrors++
				continue mainLoop
			}
		}

		// Send acceptance.
		slog.Debug("ft: sending accept", "path", meta.Path, "destPath", destPath)
		acceptPayload, _ := json.Marshal(FtMetaPayload{Accepted: true})
		if err := WriteFTFrame(conn, FtMeta, acceptPayload); err != nil {
			f.Close()
			slog.Error("ft: write accept", "error", err)
			return
		}

		// Stream data.
		hasher := sha256.New()
		var fileBytes int64
		for {
			frameType, chunk, err := ReadFTFrame(conn)
			if err != nil {
				f.Close()
				slog.Error("ft: read data frame", "error", err)
				return
			}
			slog.Debug("ft: dataLoop read frame", "type", fmt.Sprintf("0x%02x", frameType), "chunkLen", len(chunk))

			switch frameType {
			case FtData:
				// Empty payload signals end of file.
				if len(chunk) == 0 {
					// File complete — verify SHA-256 and send OK.
					hashHex := hex.EncodeToString(hasher.Sum(nil))
					if meta.SHA256 != "" && hashHex != meta.SHA256 {
						f.Close()
						_ = os.Remove(destPath) // clean up partial file
						slog.Error("ft: sha256 mismatch",
							"path", destPath, "expected", meta.SHA256, "got", hashHex)
						errPayload, _ := json.Marshal(FtErrorPayload{
							Code:    "hash_mismatch",
							Message: fmt.Sprintf("SHA-256 mismatch for %s", destPath),
						})
						_ = WriteFTFrame(conn, FtError, errPayload)
						fileErrors++
						continue mainLoop
					}
					okPayload, _ := json.Marshal(FtMetaPayload{
						Path:   meta.Path,
						Size:   fileBytes,
						SHA256: hashHex,
					})
					if writeErr := WriteFTFrame(conn, FtOK, okPayload); writeErr != nil {
						f.Close()
						slog.Error("ft: write ok", "error", writeErr)
						return
					}
					fileSuccess++
					// fsync before close to ensure data is on storage, not just page cache.
					// Without this, data can be lost if Firecracker is killed before the kernel flushes.
					if syncErr := f.Sync(); syncErr != nil {
						f.Close()
						slog.Error("ft: fsync failed", "path", destPath, "error", syncErr)
						return
					}
					_ = f.Close()
					slog.Debug("ft: eos received, sending ok, continuing mainLoop", "path", meta.Path, "fileBytes", fileBytes)
					continue mainLoop
				}
				n, writeErr := f.Write(chunk)
				if writeErr != nil {
					f.Close()
					slog.Error("ft: write chunk", "error", writeErr)
					return
				}
				hasher.Write(chunk[:n])
				fileBytes += int64(n)
				totalBytes += int64(n)

			case FtProgress:
				// Progress frames from host are informational — skip on agent side.
				continue

			case FtError:
				f.Close()
				var errPayload FtErrorPayload
				if json.Unmarshal(chunk, &errPayload) == nil {
					slog.Error("ft: host error during push", "code", errPayload.Code, "message", errPayload.Message)
				}
				fileErrors++
				continue mainLoop

			default:
				f.Close()
				slog.Error("ft: unexpected frame in data stream", "type", fmt.Sprintf("0x%02x", frameType))
				return
			}
		}
	}

	// Sync filesystem to flush Firecracker's internal writeback cache.
	// The per-file fsync only flushes through guest kernel → virtio-blk device,
	// but does NOT flush Firecracker's host-side writeback cache. This sync()
	// syscall triggers VIRTIO_BLK_T_FLUSH on the virtio-blk device, ensuring
	// data reaches the backing file before we send DONE.
	if !push.NoSync {
		slog.Debug("ft: syncing filesystem before done")
		unix.Sync()
		slog.Debug("ft: sync complete")
	}

	// Send DONE back.
	slog.Debug("ft: mainLoop done, sending done back",
		"files", fileSuccess, "bytes", totalBytes, "errors", fileErrors)
	donePayload, _ := json.Marshal(FtDonePayload{
		Files:  fileSuccess,
		Bytes:  totalBytes,
		Errors: fileErrors,
	})
	if err := WriteFTFrame(conn, FtDone, donePayload); err != nil {
		slog.Error("ft: write done", "error", err)
		return
	}

	slog.Info("ft: push complete", "files", fileSuccess,
		"bytes", totalBytes, "errors", fileErrors)
}

// --- Pull handler (host downloads files from VM) ---

func handleFTPull(ctx context.Context, conn net.Conn, pullPayload []byte) {
	var pull FtPullPayload
	if err := json.Unmarshal(pullPayload, &pull); err != nil {
		slog.Error("ft: parse pull payload", "error", err)
		errPayload, _ := json.Marshal(FtErrorPayload{Code: "invalid", Message: "invalid pull payload"})
		_ = WriteFTFrame(conn, FtError, errPayload)
		return
	}

	slog.Info("ft: pull start", "path", pull.Path, "dest", pull.Dest,
		"overwrite", pull.Overwrite, "recursive", pull.Recursive)

	// Stat source.
	fi, err := os.Stat(pull.Path)
	if err != nil {
		slog.Error("ft: stat source", "path", pull.Path, "error", err)
		errPayload, _ := json.Marshal(FtErrorPayload{Code: "not_found", Message: err.Error()})
		_ = WriteFTFrame(conn, FtError, errPayload)
		return
	}

	// Directory handling.
	if fi.IsDir() {
		if !pull.Recursive {
			errPayload, _ := json.Marshal(FtErrorPayload{
				Code:    "is_directory",
				Message: fmt.Sprintf("'%s' is a directory; use recursive=true to pull", pull.Path),
			})
			_ = WriteFTFrame(conn, FtError, errPayload)
			return
		}
		handleFTPullRecursive(ctx, conn, pull.Path)
		return
	}

	// --- Single file pull (unchanged). ---

	size := fi.Size()
	mode := int(fi.Mode().Perm())
	baseName := filepath.Base(pull.Path)

	// Compute SHA-256 of the entire file.
	hasher := sha256.New()
	hashFile, err := os.Open(pull.Path)
	if err != nil {
		slog.Error("ft: open source", "path", pull.Path, "error", err)
		errPayload, _ := json.Marshal(FtErrorPayload{Code: "open_failed", Message: err.Error()})
		_ = WriteFTFrame(conn, FtError, errPayload)
		return
	}
	defer hashFile.Close()

	if _, err := io.Copy(hasher, hashFile); err != nil {
		slog.Error("ft: hash source", "path", pull.Path, "error", err)
		return
	}
	hashFile.Close()
	hashHex := hex.EncodeToString(hasher.Sum(nil))

	// Send META frame with file info.
	meta := FtMetaPayload{
		Path:   baseName,
		Size:   size,
		Mode:   mode,
		SHA256: hashHex,
	}
	metaPayload, _ := json.Marshal(meta)
	if err := WriteFTFrame(conn, FtMeta, metaPayload); err != nil {
		slog.Error("ft: write meta", "error", err)
		return
	}

	// Read acceptance from host.
	frameType, acceptPayload, err := ReadFTFrame(conn)
	if err != nil {
		slog.Error("ft: read accept", "error", err)
		return
	}
	if frameType == FtError {
		var errPayload FtErrorPayload
		if json.Unmarshal(acceptPayload, &errPayload) == nil {
			slog.Error("ft: host rejected pull", "code", errPayload.Code, "message", errPayload.Message)
		}
		return
	}
	if frameType != FtMeta {
		slog.Error("ft: expected meta/accept frame", "got", fmt.Sprintf("0x%02x", frameType))
		return
	}

	// Open file for streaming.
	f, err := os.Open(pull.Path)
	if err != nil {
		slog.Error("ft: re-open source", "path", pull.Path, "error", err)
		return
	}
	defer f.Close()

	// Stream data in 256 KB chunks.
	chunkSize := ftBufferSize
	buf := make([]byte, chunkSize)
	var sentBytes int64
	for {
		select {
		case <-ctx.Done():
			slog.Warn("ft: pull cancelled mid-transfer", "error", ctx.Err())
			return
		default:
		}
		n, readErr := f.Read(buf)
		if n > 0 {
			if err := WriteFTFrame(conn, FtData, buf[:n]); err != nil {
				slog.Error("ft: write data", "error", err)
				return
			}
			sentBytes += int64(n)

			// Send progress update.
			progPayload, _ := json.Marshal(FtProgressPayload{
				Path:  baseName,
				Bytes: sentBytes,
				Total: size,
			})
			if err := WriteFTFrame(conn, FtProgress, progPayload); err != nil {
				slog.Error("ft: write progress", "error", err)
				return
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			slog.Error("ft: read source", "error", readErr)
			return
		}
	}

	// Send end-of-stream signal (empty DATA frame).
	if err := WriteFTFrame(conn, FtData, nil); err != nil {
		slog.Error("ft: write eos", "error", err)
		return
	}

	// Read OK from host (verification).
	frameType, okPayload, err := ReadFTFrame(conn)
	if err != nil {
		slog.Error("ft: read ok", "error", err)
		return
	}
	if frameType == FtError {
		var errPayload FtErrorPayload
		if json.Unmarshal(okPayload, &errPayload) == nil {
			slog.Error("ft: host reported error", "code", errPayload.Code, "message", errPayload.Message)
		}
		return
	}
	if frameType != FtOK {
		slog.Error("ft: expected ok frame", "got", fmt.Sprintf("0x%02x", frameType))
		return
	}

	// Verify host's returned SHA-256 matches what we sent.
	var okMeta FtMetaPayload
	if json.Unmarshal(okPayload, &okMeta) == nil {
		if okMeta.SHA256 != "" && okMeta.SHA256 != hashHex {
			slog.Error("ft: host sha256 mismatch",
				"expected", hashHex, "got", okMeta.SHA256)
			return
		}
		if okMeta.Size > 0 && okMeta.Size != sentBytes {
			slog.Warn("ft: host byte count mismatch",
				"sent", sentBytes, "host_reported", okMeta.Size)
		}
	}

	// Send DONE.
	donePayload, _ := json.Marshal(FtDonePayload{
		Files:  1,
		Bytes:  sentBytes,
		Errors: 0,
	})
	if err := WriteFTFrame(conn, FtDone, donePayload); err != nil {
		slog.Error("ft: write done", "error", err)
		return
	}

	slog.Info("ft: pull complete", "path", pull.Path, "bytes", sentBytes)
}

// handleFTPullRecursive streams every regular file under srcDir to the host.
// For each file it sends META → waits for accept → streams DATA+EOS → reads
// OK. After all files it sends a single DONE with the summary.
//
// Symlinks are followed: symlinks to regular files stream the target content
// under the symlink's logical name; symlinks to directories recursively walk
// the target contents. Broken symlinks and non-regular files (sockets, FIFOs,
// devices) are skipped with a warning. Cycles are detected via a per-branch
// stack of resolved physical directory paths.
func handleFTPullRecursive(ctx context.Context, conn net.Conn, srcDir string) {
	srcDir = filepath.Clean(srcDir)
	var totalFiles, totalErrors int
	var totalBytes int64

	var walkFn func(string, []string)
	walkFn = func(currentDir string, stack []string) {
		// Resolve to physical path for cycle detection.
		realPath, err := filepath.EvalSymlinks(currentDir)
		if err != nil {
			slog.Warn("ft: cannot resolve directory, skipping", "path", currentDir, "error", err)
			return
		}
		if slices.Contains(stack, realPath) {
			slog.Warn("ft: symlink cycle detected, skipping", "path", currentDir)
			return
		}

		// Build child stack (copy-on-write to preserve parent's slice).
		childStack := make([]string, len(stack), len(stack)+1)
		copy(childStack, stack)
		childStack = append(childStack, realPath)

		df, openErr := os.Open(currentDir)
		if openErr != nil {
			slog.Error("ft: open directory for pull", "path", currentDir, "error", openErr)
			return
		}
		defer df.Close()

		names, readErr := df.Readdirnames(-1)
		if readErr != nil {
			slog.Error("ft: read directory for pull", "path", currentDir, "error", readErr)
			return
		}

		for _, name := range names {
			path := filepath.Join(currentDir, name)

			lfi, lErr := os.Lstat(path)
			if lErr != nil {
				slog.Warn("ft: cannot lstat entry, skipping", "path", path, "error", lErr)
				continue
			}

			// Handle symlinks explicitly.
			if lfi.Mode()&os.ModeSymlink != 0 {
				tfi, stErr := os.Stat(path)
				if stErr != nil {
					slog.Warn("ft: skipping broken symlink in pull", "path", path)
					continue
				}
				if tfi.IsDir() {
					walkFn(path, childStack) // recurse through symlink
					continue
				} else if !tfi.Mode().IsRegular() {
					slog.Warn("ft: skipping non-regular symlink target in pull", "path", path, "mode", tfi.Mode())
					continue
				}
				// Fall through to process as file (symlink to regular file).
			} else if lfi.IsDir() {
				walkFn(path, childStack)
				continue
			} else if !lfi.Mode().IsRegular() {
				slog.Warn("ft: skipping non-regular file in pull", "path", path, "mode", lfi.Mode())
				continue
			}

			// --- Process one regular file ---

			select {
			case <-ctx.Done():
				return
			default:
			}

			relPath, relErr := filepath.Rel(srcDir, path)
			if relErr != nil {
				slog.Error("ft: relative path", "path", path, "error", relErr)
				totalErrors++
				continue
			}

			// Stat the resolved target for size and mode.
			fi, stErr := os.Stat(path)
			if stErr != nil {
				slog.Warn("ft: file vanished before read, skipping", "path", path, "error", stErr)
				totalErrors++
				continue
			}
			size := fi.Size()
			mode := int(fi.Mode().Perm())

			// Compute SHA-256.
			hasher := sha256.New()
			hashFile, hErr := os.Open(path)
			if hErr != nil {
				slog.Error("ft: open source for hash", "path", path, "error", hErr)
				totalErrors++
				continue
			}
			_, copyErr := io.Copy(hasher, hashFile)
			hashFile.Close()
			if copyErr != nil {
				slog.Error("ft: hash source", "path", path, "error", copyErr)
				totalErrors++
				continue
			}
			hashHex := hex.EncodeToString(hasher.Sum(nil))

			// Send META frame.
			meta := FtMetaPayload{
				Path:   relPath,
				Size:   size,
				Mode:   mode,
				SHA256: hashHex,
			}
			metaPayload, _ := json.Marshal(meta)
			if err := WriteFTFrame(conn, FtMeta, metaPayload); err != nil {
				slog.Error("ft: write meta during pull", "error", err)
				return
			}

			// Read acceptance from host.
			frameType, _, aErr := ReadFTFrame(conn)
			if aErr != nil {
				slog.Error("ft: read accept during pull", "error", aErr)
				return
			}
			if frameType == FtError {
				slog.Warn("ft: host rejected file in recursive pull", "path", relPath)
				totalErrors++
				continue
			}
			if frameType != FtMeta {
				slog.Error("ft: expected meta/accept frame", "got", fmt.Sprintf("0x%02x", frameType))
				return
			}

			// Open file for streaming.
			f, oErr := os.Open(path)
			if oErr != nil {
				slog.Error("ft: open source for streaming", "path", path, "error", oErr)
				totalErrors++
				continue
			}

			// Stream data in 256 KB chunks.
			buf := make([]byte, ftBufferSize)
			var sentBytes int64
			for {
				n, readErr := f.Read(buf)
				if n > 0 {
					if wErr := WriteFTFrame(conn, FtData, buf[:n]); wErr != nil {
						f.Close()
						slog.Error("ft: write data during pull", "error", wErr)
						return
					}
					sentBytes += int64(n)

					progPayload, _ := json.Marshal(FtProgressPayload{
						Path:  relPath,
						Bytes: sentBytes,
						Total: size,
					})
					if pErr := WriteFTFrame(conn, FtProgress, progPayload); pErr != nil {
						f.Close()
						slog.Error("ft: write progress during pull", "error", pErr)
						return
					}
				}
				if readErr != nil {
					f.Close()
					if readErr == io.EOF {
						break
					}
					slog.Error("ft: read source during pull", "path", path, "error", readErr)
					totalErrors++
					break
				}
			}

			// Send end-of-stream signal (empty DATA frame).
			if eosErr := WriteFTFrame(conn, FtData, nil); eosErr != nil {
				slog.Error("ft: write eos during pull", "error", eosErr)
				return
			}

			// Read OK from host.
			frameType, okPayload, okErr := ReadFTFrame(conn)
			if okErr != nil {
				slog.Error("ft: read ok during pull", "error", okErr)
				return
			}
			if frameType == FtError {
				slog.Warn("ft: host reported error for file", "path", relPath)
				totalErrors++
				continue
			}
			if frameType != FtOK {
				slog.Error("ft: expected ok frame", "got", fmt.Sprintf("0x%02x", frameType))
				return
			}

			// Verify host's returned SHA-256 matches what we sent.
			var okMeta FtMetaPayload
			if json.Unmarshal(okPayload, &okMeta) == nil {
				if okMeta.SHA256 != "" && okMeta.SHA256 != hashHex {
					slog.Error("ft: host sha256 mismatch",
						"path", relPath, "expected", hashHex, "got", okMeta.SHA256)
					totalErrors++
					continue
				}
			}

			totalFiles++
			totalBytes += sentBytes
		}
	}

	walkFn(srcDir, nil)

	// Send DONE.
	donePayload, _ := json.Marshal(FtDonePayload{
		Files:  totalFiles,
		Bytes:  totalBytes,
		Errors: totalErrors,
	})
	if err := WriteFTFrame(conn, FtDone, donePayload); err != nil {
		slog.Error("ft: write done after recursive pull", "error", err)
		return
	}

	slog.Info("ft: pull recursive complete", "dir", srcDir,
		"files", totalFiles, "bytes", totalBytes, "errors", totalErrors)
}
