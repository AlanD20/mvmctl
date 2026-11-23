package jailer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"mvmctl/internal/infra"
	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// volumeHotplugAPI opens only the fixed API socket beneath the pinned process
// root and proves the actual connected peer before writing any HTTP bytes.
func (authority *volumeHotplugAuthority) volumeHotplugAPI(
	ctx context.Context,
	method, endpoint string,
	body []byte,
) (_ []byte, returnErr error) {
	if err := authority.verify(ctx); err != nil {
		return nil, err
	}
	path := fmt.Sprintf(
		"/proc/self/fd/%d%s/%s",
		authority.root,
		managedVMPrivateRuntimeMountPath,
		infra.VMFirecrackerAPISocketFilename,
	)
	connection, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, connection.Close()) }()
	socket, ok := connection.(*net.UnixConn)
	if !ok {
		return nil, instanceStateError(authority.locked.record.vmID, "volume API peer is not a Unix socket")
	}
	raw, err := socket.SyscallConn()
	if err != nil {
		return nil, err
	}
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		peerErr = admitStopAPIPeer(ctx, int(fd), authority.locked.record, authority.pidfd)
	}); err != nil {
		return nil, err
	}
	if peerErr != nil {
		return nil, peerErr
	}
	deadline := time.Now().Add(5 * time.Second)
	if caller, ok := ctx.Deadline(); ok && caller.Before(deadline) {
		deadline = caller
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://firecracker"+endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Close = true
	if err := request.Write(connection); err != nil {
		return nil, volumeHotplugUnknown(authority.locked.record.vmID, err)
	}
	response, err := http.ReadResponse(bufio.NewReader(io.LimitReader(connection, 1<<20)), request)
	if err != nil {
		return nil, volumeHotplugUnknown(authority.locked.record.vmID, err)
	}
	defer func() { returnErr = errors.Join(returnErr, response.Body.Close()) }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(payload) > 1<<20 {
		return nil, volumeHotplugUnknown(authority.locked.record.vmID, err)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusNoContent {
		return nil, errs.New(errs.CodeFirecrackerClientError,
			fmt.Sprintf("volume API request failed with HTTP status %d", response.StatusCode),
			errs.WithEntity(authority.locked.record.vmID))
	}
	if err := authority.verify(ctx); err != nil {
		return nil, volumeHotplugUnknown(authority.locked.record.vmID, err)
	}
	return payload, nil
}

func (authority *volumeHotplugAuthority) observeVolumeDrive(
	ctx context.Context,
	id string,
) (*model.DriveConfig, error) {
	drives, err := authority.observeVolumeDrives(ctx)
	if err != nil {
		return nil, err
	}
	var found *model.DriveConfig
	for index := range drives {
		if drives[index].DriveID == id {
			found = &drives[index]
		}
	}
	return found, nil
}

func (authority *volumeHotplugAuthority) observeVolumeDrives(ctx context.Context) ([]model.DriveConfig, error) {
	raw, err := authority.volumeHotplugAPI(ctx, http.MethodGet, "/vm/config", nil)
	if err != nil {
		return nil, err
	}
	var config struct {
		Drives []model.DriveConfig `json:"drives"`
	}
	if err := json.Unmarshal(raw, &config); err != nil || config.Drives == nil {
		return nil, volumeHotplugUnknown(authority.locked.record.vmID, err)
	}
	seen := make(map[string]bool, len(config.Drives))
	for index := range config.Drives {
		id := config.Drives[index].DriveID
		if id == "" || seen[id] {
			return nil, instanceStateError(
				authority.locked.record.vmID,
				"volume API inventory contains duplicate drive IDs",
			)
		}
		seen[id] = true
	}
	return config.Drives, nil
}

func volumeHotplugUnknown(vmID string, cause error) error {
	return errs.WrapMsg(
		errs.CodeVMStateInvalid,
		"volume effect outcome is unknown; retry the same operation",
		cause,
		errs.WithEntity(
			vmID,
		),
		errs.WithDetails(map[string]any{"outcome_unknown": true, "volume_cleanup_pending": true}),
	)
}
