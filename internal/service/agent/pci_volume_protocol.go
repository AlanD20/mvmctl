package agent

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"time"

	"mvmctl/internal/lib/model"
	"mvmctl/pkg/errs"
)

// PCIVolumeMagic selects the closed authenticated guest volume-release protocol.
const PCIVolumeMagic = "MVMPCI01"

// PCIVolumeReadyMagic selects the closed authenticated guest volume-readiness protocol.
const PCIVolumeReadyMagic = "MVMPCIR1"

// PCIVolumeReadyRequest selects only root-derived serial and capacity.
type PCIVolumeReadyRequest struct {
	Token  string                  `json:"token"`
	Volume model.PCIVolumeIdentity `json:"volume"`
}

// PCIVolumeReadyResult acknowledges an exact discovered and driver-bound disk.
type PCIVolumeReadyResult struct {
	Volume model.PCIVolumeIdentity `json:"volume"`
	Ready  bool                    `json:"ready"`
	Error  string                  `json:"error"`
}

// WritePCIVolumeReadyRequest writes one closed selected-volume readiness request.
func WritePCIVolumeReadyRequest(ctx context.Context, writer io.Writer, request PCIVolumeReadyRequest) error {
	return writePCIVolumeFrame(ctx, writer, request)
}

// WritePCIVolumeReadyResult writes one closed selected-volume readiness result.
func WritePCIVolumeReadyResult(ctx context.Context, writer io.Writer, result PCIVolumeReadyResult) error {
	return writePCIVolumeFrame(ctx, writer, result)
}

// ReadPCIVolumeReadyRequest reads one closed selected-volume readiness request.
func ReadPCIVolumeReadyRequest(ctx context.Context, reader io.Reader) (PCIVolumeReadyRequest, error) {
	var request PCIVolumeReadyRequest
	err := readPCIVolumeFrame(ctx, reader, &request)
	return request, err
}

// ReadPCIVolumeReadyResult reads one closed selected-volume readiness result.
func ReadPCIVolumeReadyResult(ctx context.Context, reader io.Reader) (PCIVolumeReadyResult, error) {
	var result PCIVolumeReadyResult
	err := readPCIVolumeFrame(ctx, reader, &result)
	return result, err
}

// PCIVolumeRemoveRequest has no command, device name, path, or PCI address.
type PCIVolumeRemoveRequest struct {
	Token  string                  `json:"token"`
	Volume model.PCIVolumeIdentity `json:"volume"`
}

// PCIVolumeRemoveResult acknowledges only verified selected-device absence.
type PCIVolumeRemoveResult struct {
	Volume  model.PCIVolumeIdentity `json:"volume"`
	Removed bool                    `json:"removed"`
	Error   string                  `json:"error"`
}

// WritePCIVolumeRemoveRequest writes one closed selected-volume request.
func WritePCIVolumeRemoveRequest(ctx context.Context, writer io.Writer, request PCIVolumeRemoveRequest) error {
	return writePCIVolumeFrame(ctx, writer, request)
}

// WritePCIVolumeRemoveResult writes one closed selected-volume result.
func WritePCIVolumeRemoveResult(ctx context.Context, writer io.Writer, result PCIVolumeRemoveResult) error {
	return writePCIVolumeFrame(ctx, writer, result)
}

// ReadPCIVolumeRemoveRequest reads one closed selected-volume request.
func ReadPCIVolumeRemoveRequest(ctx context.Context, reader io.Reader) (PCIVolumeRemoveRequest, error) {
	var request PCIVolumeRemoveRequest
	err := readPCIVolumeFrame(ctx, reader, &request)
	return request, err
}

// ReadPCIVolumeRemoveResult reads one closed selected-volume result.
func ReadPCIVolumeRemoveResult(ctx context.Context, reader io.Reader) (PCIVolumeRemoveResult, error) {
	var result PCIVolumeRemoveResult
	err := readPCIVolumeFrame(ctx, reader, &result)
	return result, err
}

func writePCIVolumeFrame(ctx context.Context, writer io.Writer, value any) error {
	// JSON framing accepts only the named PCI request/result structs used by its callers.
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(raw) > 1024 {
		return errs.New(errs.CodeVMStateInvalid, "guest volume frame exceeds its bound")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(raw)))
	_, err = io.Copy(writer, bytes.NewReader(append(header[:], raw...)))
	return err
}

func (a *Agent) handlePCIVolumeReadyConnection(ctx context.Context, conn net.Conn) {
	operationCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	deadline, _ := operationCtx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return
	}
	request, err := ReadPCIVolumeReadyRequest(operationCtx, conn)
	if err != nil {
		return
	}
	result := PCIVolumeReadyResult{Volume: request.Volume}
	if a.token == "" || subtle.ConstantTimeCompare([]byte(a.token), []byte(request.Token)) != 1 {
		result.Error = "guest volume authentication failed"
	} else if err := request.Volume.Validate(); err != nil {
		result.Error = "guest volume identity is invalid"
	} else if err := ensurePCIVolumeReady(operationCtx, request.Volume, productionPCIVolumeFS()); err != nil {
		result.Error = err.Error()
	} else {
		result.Ready = true
	}
	if err := WritePCIVolumeReadyResult(operationCtx, conn, result); err != nil {
		return
	}
}

func readPCIVolumeFrame(ctx context.Context, reader io.Reader, value any) error {
	// encoding/json requires a concrete request/result pointer through any.
	if err := ctx.Err(); err != nil {
		return err
	}
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > 1024 {
		return errs.New(errs.CodeVMStateInvalid, "guest volume frame exceeds its bound")
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errs.New(errs.CodeVMStateInvalid, "guest volume frame contains trailing JSON")
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, raw) {
		return errs.New(errs.CodeVMStateInvalid, "guest volume frame is not canonical closed JSON")
	}
	return nil
}

func (a *Agent) handlePCIVolumeConnection(ctx context.Context, conn net.Conn) {
	operationCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := conn.SetDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return
	}
	request, err := ReadPCIVolumeRemoveRequest(operationCtx, conn)
	if err != nil {
		return
	}
	result := PCIVolumeRemoveResult{Volume: request.Volume}
	if a.token == "" || subtle.ConstantTimeCompare([]byte(a.token), []byte(request.Token)) != 1 {
		result.Error = "guest volume authentication failed"
	} else if err := request.Volume.Validate(); err != nil {
		result.Error = "guest volume identity is invalid"
	} else if err := removePCIVolume(operationCtx, request.Volume, productionPCIVolumeFS()); err != nil {
		result.Error = err.Error()
	} else {
		result.Removed = true
	}
	// Delivery failure preserves an unknown outcome for the caller's next observation.
	if err := WritePCIVolumeRemoveResult(operationCtx, conn, result); err != nil {
		return
	}
}
