package vm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// --- Constants ---
const (
	constSocketTimeoutSeconds = 5.0
)

// FirecrackerClient provides HTTP access to the Firecracker API over a Unix socket.
type FirecrackerClient struct {
	socketPath string
	httpClient *http.Client
}

// NewFirecrackerClient creates a new client connected to the given Unix socket path.
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

// Close shuts down the Firecracker API client connection.
func (fc *FirecrackerClient) Close() {
	fc.httpClient.CloseIdleConnections()
}

// --- Low-level HTTP request with retry ---

// request makes an HTTP request to the Firecracker API with retry on connection refused.
// - 5 retries with exponential backoff (0.1s, 0.2s, 0.4s, 0.8s, 1.6s)
// - DomainError with CodeFirecrackerSocketNotFound when socket doesn't exist
// - On ECONNREFUSED: retry with reconnect
// - Returns (status, body, error) where body is the raw response body bytes.
func (fc *FirecrackerClient) request(
	ctx context.Context,
	method, path string,
	body map[string]any,
) (int, []byte, error) {
	var bodyStr string
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyStr = string(data)
	}

	// Closure returning a fresh reader for each retry attempt.
	// strings.Reader is consumed after Do() reads the body, so we
	// must create a new reader on every retry.
	var makeBody func() io.Reader
	if bodyStr != "" {
		bodyStr := bodyStr // pin
		makeBody = func() io.Reader { return strings.NewReader(bodyStr) }
	}

	const maxRetries = 5
	delay := 100 * time.Millisecond
	var lastErr error

	for attempt := range maxRetries {
		var bodyReader io.Reader
		if makeBody != nil {
			bodyReader = makeBody()
		}

		req, err := http.NewRequestWithContext(ctx, method,
			fmt.Sprintf("http://localhost%s", path), bodyReader)
		if err != nil {
			return 0, nil, fmt.Errorf("failed to create request: %w", err)
		}
		if makeBody != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := fc.httpClient.Do(req)
		if err != nil {
			lastErr = err

			if isConnRefused(err) && attempt < maxRetries-1 {
				time.Sleep(delay)
				delay *= 2
				fc.Close()
				continue
			}

			return 0, nil, errs.New(errs.CodeFirecrackerClientError,
				fmt.Sprintf("api request failed: %v", err))
		}

		status := resp.StatusCode

		if status == http.StatusNoContent {
			resp.Body.Close()
			return status, nil, nil
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		return status, bodyBytes, nil
	}

	return 0, nil, errs.New(errs.CodeFirecrackerClientError,
		fmt.Sprintf("api request failed after %d retries: %v", maxRetries, lastErr))
}

func isConnRefused(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == syscall.ECONNREFUSED
}

// --- Snapshot Operations ---

// CreateSnapshot creates a VM snapshot via PUT /snapshot/create.
func (fc *FirecrackerClient) CreateSnapshot(ctx context.Context, memPath, snapshotPath string) (bool, error) {
	slog.Debug("Creating snapshot...")
	body := map[string]any{
		"mem_file_path": memPath,
		"snapshot_path": snapshotPath,
	}
	status, raw, err := fc.request(ctx, "PUT", "/snapshot/create", body)
	if err != nil {
		return false, err
	}
	if status == http.StatusNoContent {
		slog.Debug("Snapshot created", "mem", memPath, "state", snapshotPath)
		return true, nil
	}
	msg := fmt.Sprintf("failed to create snapshot: %d", status)
	if len(raw) > 0 {
		msg += fmt.Sprintf(" response: %s", string(raw))
	}
	return false, errs.New(errs.CodeFirecrackerClientError, msg)
}

// LoadSnapshot loads a VM from snapshot via PUT /snapshot/load.
func (fc *FirecrackerClient) LoadSnapshot(
	ctx context.Context,
	memPath, snapshotPath string,
	resume bool,
) (bool, error) {
	slog.Debug("Loading snapshot...")
	body := map[string]any{
		"mem_file_path": memPath,
		"snapshot_path": snapshotPath,
		"resume_vm":     resume,
	}
	status, raw, err := fc.request(ctx, "PUT", "/snapshot/load", body)
	if err != nil {
		return false, err
	}
	if status == http.StatusNoContent {
		slog.Debug("Snapshot loaded")
		return true, nil
	}
	msg := fmt.Sprintf("failed to load snapshot: %d", status)
	if len(raw) > 0 {
		msg += fmt.Sprintf(" response: %s", string(raw))
	}
	return false, errs.New(errs.CodeFirecrackerClientError, msg)
}

// --- Instance Info Operations ---

// GetInstanceInfo returns VM instance information via GET /.
func (fc *FirecrackerClient) GetInstanceInfo(ctx context.Context) (*model.InstanceInfo, error) {
	status, body, err := fc.request(ctx, "GET", "/", nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusOK && len(body) > 0 {
		var info model.InstanceInfo
		if err := json.Unmarshal(body, &info); err != nil {
			return nil, err
		}
		return &info, nil
	}
	return nil, nil
}

// DescribeInstance returns a VM description via GET /vm.
func (fc *FirecrackerClient) DescribeInstance(ctx context.Context) (*model.InstanceDescription, error) {
	status, body, err := fc.request(ctx, "GET", "/vm", nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusOK && len(body) > 0 {
		var desc model.InstanceDescription
		if err := json.Unmarshal(body, &desc); err != nil {
			return nil, err
		}
		return &desc, nil
	}
	return nil, nil
}

// --- VM Lifecycle Operations ---

// StartInstance starts the VM instance via PUT /actions with action_type InstanceStart.
func (fc *FirecrackerClient) StartInstance(ctx context.Context) (bool, error) {
	slog.Debug("Starting VM...")
	status, _, err := fc.request(ctx, "PUT", "/actions", map[string]any{"action_type": "InstanceStart"})
	if err != nil {
		return false, err
	}
	if status == http.StatusNoContent {
		slog.Debug("VM started")
		return true, nil
	}
	return false, errs.New(errs.CodeFirecrackerClientError,
		fmt.Sprintf("failed to start VM: %d", status))
}

// SendCtrlAltDel sends Ctrl+Alt+Del to the VM via PUT /actions.
// DomainError (from Firecracker client) and socket-not-found errors are
// absorbed (return false, nil). All other errors propagate (return false, err).
func (fc *FirecrackerClient) SendCtrlAltDel(ctx context.Context) (bool, error) {
	status, _, err := fc.request(ctx, "PUT", "/actions", map[string]any{"action_type": "SendCtrlAltDel"})
	if err != nil {
		var de *errs.DomainError
		if errors.As(err, &de) {
			slog.Error("Failed to send Ctrl+Alt+Del")
			return false, nil
		}
		return false, err
	}
	if status == http.StatusNoContent {
		slog.Debug("Ctrl+Alt+Del sent")
		return true, nil
	}
	slog.Error("Failed to send Ctrl+Alt+Del", "status", status)
	return false, nil
}

// PauseVM pauses the microVM via PATCH /vm with state: "Paused".
func (fc *FirecrackerClient) PauseVM(ctx context.Context) error {
	slog.Debug("Pausing VM...")
	status, _, err := fc.request(ctx, "PATCH", "/vm", map[string]any{"state": "Paused"})
	if err != nil {
		return err
	}
	if status == http.StatusNoContent {
		slog.Debug("VM paused")
		return nil
	}
	return errs.New(errs.CodeFirecrackerClientError,
		fmt.Sprintf("failed to pause VM: %d", status))
}

// ResumeVM resumes a paused microVM via PATCH /vm with state: "Resumed".
func (fc *FirecrackerClient) ResumeVM(ctx context.Context) error {
	slog.Debug("Resuming VM...")
	status, _, err := fc.request(ctx, "PATCH", "/vm", map[string]any{"state": "Resumed"})
	if err != nil {
		return err
	}
	if status == http.StatusNoContent {
		slog.Debug("VM resumed")
		return nil
	}
	return errs.New(errs.CodeFirecrackerClientError,
		fmt.Sprintf("failed to resume VM: %d", status))
}

// --- Drive Operations ---

// PutDrive attaches or updates a drive via PUT /drives/{drive_id}.
func (fc *FirecrackerClient) PutDrive(ctx context.Context, driveConfig model.DriveConfig) error {
	body := map[string]any{
		"drive_id":       driveConfig.DriveID,
		"path_on_host":   driveConfig.PathOnHost,
		"is_root_device": driveConfig.IsRootDevice,
		"is_read_only":   driveConfig.IsReadOnly,
		"cache_type":     driveConfig.CacheType,
		"io_engine":      driveConfig.IOEngine,
	}
	status, raw, err := fc.request(ctx, "PUT", "/drives/"+driveConfig.DriveID, body)
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusNoContent {
		return nil
	}
	msg := fmt.Sprintf("failed to attach drive: %d", status)
	if len(raw) > 0 {
		msg += fmt.Sprintf(" response: %s", string(raw))
	}
	return errs.New(errs.CodeFirecrackerClientError, msg)
}

// PatchDrive removes a drive from a running VM via PATCH /drives/{drive_id}.
func (fc *FirecrackerClient) PatchDrive(ctx context.Context, driveID string) error {
	body := map[string]any{"drive_id": driveID}
	status, raw, err := fc.request(ctx, "PATCH", "/drives/"+driveID, body)
	if err != nil {
		return err
	}
	if status == http.StatusOK || status == http.StatusNoContent {
		return nil
	}
	msg := fmt.Sprintf("failed to detach drive: %d", status)
	if len(raw) > 0 {
		msg += fmt.Sprintf(" response: %s", string(raw))
	}
	return errs.New(errs.CodeFirecrackerClientError, msg)
}

// DeleteDrive removes a drive from a running VM via DELETE /drives/{drive_id}.
func (fc *FirecrackerClient) DeleteDrive(ctx context.Context, driveID string) error {
	status, raw, err := fc.request(ctx, "DELETE", "/drives/"+driveID, nil)
	if err != nil {
		return err
	}
	if status == http.StatusNoContent {
		return nil
	}
	msg := fmt.Sprintf("failed to delete drive: %d", status)
	if len(raw) > 0 {
		msg += fmt.Sprintf(" response: %s", string(raw))
	}
	return errs.New(errs.CodeFirecrackerClientError, msg)
}
