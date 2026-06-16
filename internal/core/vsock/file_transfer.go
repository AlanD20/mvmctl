// Package vsock provides the vsock domain for guest agent communication.
// File transfer implements a binary frame protocol for copying files
// between the host and VM (and between VMs) over the vsock connection.
package vsock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
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

// ── FTCopyToVM (host → VM push) ─────────────────────────────────────────────

// FTCopyToVM copies files from the host to the VM using the binary frame protocol.
// It connects to the guest agent, performs a JSON handshake, then switches to
// binary frames to push each file.
//
// destPath determines the copy mode:
//   - Trailing "/" or empty → directory mode: preserve source basename inside destPath
//   - No trailing "/"       → file mode: use destPath as the exact destination filename.
//     Multi-source with file mode returns an error.
func (c *Client) FTCopyToVM(
	ctx context.Context,
	srcPaths []string,
	destPath string,
	overwrite bool,
	onProgress event.OnDownloadCallback,
) (*FTResult, error) {
	conn, err := c.waitForAgent(ctx)
	if err != nil {
		slog.Error("ft: vsock dial and handshake failed", "vm_id", c.item.VmID, "error", err)
		return nil, fmt.Errorf("vsock connection failed: %w", err)
	}
	defer conn.Close()

	// ── JSON handshake ──
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

	// ── Switch to binary frames ──

	// Validate: multiple sources require explicit directory mode (trailing /).
	if !strings.HasSuffix(destPath, "/") && len(srcPaths) > 1 {
		return nil, fmt.Errorf("multiple sources require a directory destination (trailing /)")
	}

	// Send PUSH frame with raw dest path. The agent will stat the path
	// and decide whether to treat it as directory or file mode.
	pushPayload, _ := json.Marshal(vsockagent.FtPushPayload{
		Paths:     srcPaths,
		Dest:      destPath,
		Overwrite: overwrite,
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

	var totalBytes int64
	var fileErrors int

	for _, srcPath := range srcPaths {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("push cancelled: %w", ctx.Err())
		default:
		}

		// Stat the local file.
		fi, err := os.Stat(srcPath)
		if err != nil {
			slog.Error("ft: stat source", "path", srcPath, "error", err)
			errPayload, _ := json.Marshal(vsockagent.FtErrorPayload{Code: "not_found", Message: err.Error()})
			_ = vsockagent.WriteFTFrame(conn, vsockagent.FtError, errPayload)
			fileErrors++
			continue
		}

		fileSize := fi.Size()
		fileMode := int(fi.Mode().Perm())

		// Always use source basename as the meta path. The agent decides
		// whether to use it (dir mode) or ignore it (file mode) based on
		// stat of the destination path.
		metaPath := filepath.Base(srcPath)

		// Compute SHA-256.
		hasher := sha256.New()
		f, err := os.Open(srcPath)
		if err != nil {
			slog.Error("ft: open source", "path", srcPath, "error", err)
			errPayload, _ := json.Marshal(vsockagent.FtErrorPayload{Code: "open_failed", Message: err.Error()})
			_ = vsockagent.WriteFTFrame(conn, vsockagent.FtError, errPayload)
			fileErrors++
			continue
		}

		if _, err := io.Copy(hasher, f); err != nil {
			f.Close()
			slog.Error("ft: hash source", "path", srcPath, "error", err)
			return nil, fmt.Errorf("hash source %s: %w", srcPath, err)
		}
		f.Close()
		hashHex := hex.EncodeToString(hasher.Sum(nil))

		// Send META frame.
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
		frameType, acceptPayload, err := vsockagent.ReadFTFrame(conn)
		if err != nil {
			slog.Error("ft: read accept", "error", err)
			return nil, fmt.Errorf("read accept frame: %w", err)
		}

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
		f, err = os.Open(srcPath)
		if err != nil {
			slog.Error("ft: re-open source", "path", srcPath, "error", err)
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
				return nil, fmt.Errorf("read source %s: %w", srcPath, readErr)
			}
		}
		_ = f.Close() // best-effort: file was written successfully, close error is non-fatal

		// Send end-of-stream signal (empty DATA frame).
		if err := vsockagent.WriteFTFrame(conn, vsockagent.FtData, nil); err != nil {
			slog.Error("ft: write eos", "error", err)
			return nil, fmt.Errorf("write eos frame: %w", err)
		}

		// Read OK from agent.
		frameType, okPayload, err := vsockagent.ReadFTFrame(conn)
		if err != nil {
			slog.Error("ft: read ok", "error", err)
			return nil, fmt.Errorf("read ok frame: %w", err)
		}
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
	donePayload, _ := json.Marshal(vsockagent.FtDonePayload{
		Files:  len(srcPaths) - fileErrors,
		Bytes:  totalBytes,
		Errors: fileErrors,
	})
	if err := vsockagent.WriteFTFrame(conn, vsockagent.FtDone, donePayload); err != nil {
		slog.Error("ft: write done", "error", err)
		return nil, fmt.Errorf("write done frame: %w", err)
	}

	result := &FTResult{
		Files:  len(srcPaths) - fileErrors,
		Bytes:  totalBytes,
		Errors: fileErrors,
	}
	slog.Info("ft: push complete", "files", result.Files, "bytes", result.Bytes, "errors", result.Errors)
	return result, nil
}

// ── FTCopyFromVM (VM → host pull) ───────────────────────────────────────────

// FTCopyFromVM copies a file from the VM to the host using the binary frame protocol.
//
// destPath determines the copy mode:
//   - Trailing "/" or existing directory → directory mode: write to destPath/<source basename>
//   - Otherwise                         → file mode: write to exact destPath
func (c *Client) FTCopyFromVM(
	ctx context.Context,
	srcPath string,
	destPath string,
	overwrite bool,
	onProgress event.OnDownloadCallback,
) (*FTResult, error) {
	conn, err := c.waitForAgent(ctx)
	if err != nil {
		slog.Error("ft: vsock dial and handshake failed", "vm_id", c.item.VmID, "error", err)
		return nil, fmt.Errorf("vsock connection failed: %w", err)
	}
	defer conn.Close()

	// ── JSON handshake ──
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

	// ── Switch to binary frames ──

	// Send PULL frame.
	pullPayload, _ := json.Marshal(vsockagent.FtPullPayload{
		Path:      srcPath,
		Dest:      destPath,
		Overwrite: overwrite,
	})
	if err := vsockagent.WriteFTFrame(conn, vsockagent.FtPull, pullPayload); err != nil {
		slog.Error("ft: write pull frame", "error", err)
		return nil, fmt.Errorf("write pull frame: %w", err)
	}

	// Read META frame from agent.
	frameType, metaPayload, err := vsockagent.ReadFTFrame(conn)
	if err != nil {
		slog.Error("ft: read meta", "error", err)
		return nil, fmt.Errorf("read meta frame: %w", err)
	}
	if frameType == vsockagent.FtError {
		var errPayload vsockagent.FtErrorPayload
		if json.Unmarshal(metaPayload, &errPayload) == nil {
			return nil, fmt.Errorf("agent error: %s: %s", errPayload.Code, errPayload.Message)
		}
		return nil, fmt.Errorf("agent error response")
	}
	if frameType != vsockagent.FtMeta {
		return nil, fmt.Errorf("expected meta frame, got 0x%02x", frameType)
	}

	var meta vsockagent.FtMetaPayload
	if err := json.Unmarshal(metaPayload, &meta); err != nil {
		return nil, fmt.Errorf("parse meta: %w", err)
	}

	slog.Debug("ft: pull meta", "path", meta.Path, "size", meta.Size, "mode", meta.Mode, "sha256", meta.SHA256)

	// ── Determine copy mode from destination path ──
	// Directory mode: dest ends with "/" or is an existing directory.
	// File mode:      dest has no trailing "/" and is not an existing directory.
	destIsDir := strings.HasSuffix(destPath, "/")
	if !destIsDir {
		if fi, stErr := os.Stat(destPath); stErr == nil && fi.IsDir() {
			destIsDir = true
		}
	}

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
			return nil, fmt.Errorf("destination exists: %s", fullDestPath)
		}
	}

	// Send acceptance.
	acceptPayload, _ := json.Marshal(vsockagent.FtMetaPayload{Accepted: true})
	if err := vsockagent.WriteFTFrame(conn, vsockagent.FtMeta, acceptPayload); err != nil {
		slog.Error("ft: write accept", "error", err)
		return nil, fmt.Errorf("write accept frame: %w", err)
	}

	// Create destination directory if needed.
	destDirPath := filepath.Dir(fullDestPath)
	if err := os.MkdirAll(destDirPath, 0755); err != nil {
		slog.Error("ft: mkdir dest", "path", destDirPath, "error", err)
		return nil, fmt.Errorf("create destination directory: %w", err)
	}

	// Open/create local file.
	f, err := os.OpenFile(fullDestPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(meta.Mode))
	if err != nil {
		slog.Error("ft: create dest file", "path", fullDestPath, "error", err)
		return nil, fmt.Errorf("create destination file: %w", err)
	}
	defer f.Close()

	// Receive frames in loop.
	hasher := sha256.New()
	var totalBytes int64
receiveLoop:
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("pull cancelled: %w", ctx.Err())
		default:
		}
		frameType, chunk, err := vsockagent.ReadFTFrame(conn)
		if err != nil {
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
				slog.Error("ft: write chunk", "error", writeErr)
				return nil, fmt.Errorf("write file chunk: %w", writeErr)
			}
			hasher.Write(chunk[:n])
			totalBytes += int64(n)

		case vsockagent.FtProgress:
			var prog vsockagent.FtProgressPayload
			if json.Unmarshal(chunk, &prog) == nil {
				if onProgress != nil {
					onProgress(prog.Bytes, prog.Total)
				}
			}

		case vsockagent.FtError:
			var errPayload vsockagent.FtErrorPayload
			if json.Unmarshal(chunk, &errPayload) == nil {
				return nil, fmt.Errorf("agent error: %s: %s", errPayload.Code, errPayload.Message)
			}
			return nil, fmt.Errorf("agent error during pull")

		default:
			return nil, fmt.Errorf("unexpected frame type during pull: 0x%02x", frameType)
		}
	}

	// Verify SHA-256 against file meta.
	localHash := hex.EncodeToString(hasher.Sum(nil))
	if meta.SHA256 != "" && localHash != meta.SHA256 {
		_ = os.Remove(fullDestPath) // clean up partial file
		return nil, fmt.Errorf("SHA-256 mismatch: local=%s remote=%s", localHash, meta.SHA256)
	}
	// Send OK back to agent.
	okPayload, _ := json.Marshal(vsockagent.FtMetaPayload{
		Path:   meta.Path,
		Size:   totalBytes,
		SHA256: hex.EncodeToString(hasher.Sum(nil)),
	})
	if err := vsockagent.WriteFTFrame(conn, vsockagent.FtOK, okPayload); err != nil {
		slog.Error("ft: write ok", "error", err)
		return nil, fmt.Errorf("write ok frame: %w", err)
	}

	// Read DONE from agent.
	_, _, err = vsockagent.ReadFTFrame(conn)
	if err != nil {
		slog.Error("ft: read done", "error", err)
		return nil, fmt.Errorf("read done frame: %w", err)
	}

	result := &FTResult{
		Files:  1,
		Bytes:  totalBytes,
		Errors: 0,
	}
	slog.Info("ft: pull complete", "path", srcPath, "bytes", result.Bytes)
	return result, nil
}

// ── FTCopyVMToVM (VM → VM relay) ────────────────────────────────────────────

// FTCopyVMToVM copies a file from one VM to another via the host as a relay.
// It connects to both VMs and relays binary frames between them.
//
// destPath determines the copy mode:
//   - Trailing "/" or empty → directory mode: preserve source basename inside destPath
//   - No trailing "/"       → file mode: use destPath as the exact destination filename
func (c *Client) FTCopyVMToVM(
	ctx context.Context,
	srcPath string,
	destPath string,
	overwrite bool,
	onProgress event.OnDownloadCallback,
	destClient *Client,
) (*FTResult, error) {
	// Connect to source VM.
	srcConn, err := c.waitForAgent(ctx)
	if err != nil {
		slog.Error("ft: source vsock dial failed", "vm_id", c.item.VmID, "error", err)
		return nil, fmt.Errorf("source vsock connection failed: %w", err)
	}
	defer srcConn.Close()

	// Connect to destination VM.
	dstConn, err := destClient.waitForAgent(ctx)
	if err != nil {
		slog.Error("ft: dest vsock dial failed", "vm_id", destClient.item.VmID, "error", err)
		return nil, fmt.Errorf("dest vsock connection failed: %w", err)
	}
	defer dstConn.Close()

	// ── JSON handshake for both ──
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

	// ── Binary: send PULL to source, PUSH to dest ──

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
