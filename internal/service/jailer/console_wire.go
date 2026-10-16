package jailer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strconv"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const (
	ConsoleObserveAction = "jailer-console-observe"
	ConsoleStopAction    = "jailer-console-stop"
)

type instanceRelayLifecycle string

const (
	instanceRelayRunning  instanceRelayLifecycle = "running"
	instanceRelayStopping instanceRelayLifecycle = "stopping"
	instanceRelayStopped  instanceRelayLifecycle = "stopped"
)

// ConsoleState is a root observation of the relay, independent of Firecracker state.
type ConsoleState string

const (
	ConsoleAbsent   ConsoleState = "absent"
	ConsoleRunning  ConsoleState = "running"
	ConsoleStopping ConsoleState = "stopping"
	ConsoleStopped  ConsoleState = "stopped"
)

// ConsoleObserveRequest selects only an existing VM; caller identity comes from transport.
type ConsoleObserveRequest struct {
	VMID string `json:"vm_id"`
}

// ConsoleStopRequest carries no process, path, or signal selection.
type ConsoleStopRequest struct {
	VMID string `json:"vm_id"`
}

// ConsoleObserveResult contains display metadata only. Root never consumes its PID as signal authority.
type ConsoleObserveResult struct {
	VMID       string       `json:"vm_id"`
	State      ConsoleState `json:"state"`
	PID        *int         `json:"pid,omitempty"`
	SocketPath string       `json:"socket_path"`
}

// ConsoleStopResult reports whether this request completed an intentional relay stop.
type ConsoleStopResult struct {
	VMID    string `json:"vm_id"`
	Stopped bool   `json:"stopped"`
}

// UnmarshalJSON rejects omitted/null terminal outcome instead of interpreting it
// as a successful inactive relay response.
func (result *ConsoleStopResult) UnmarshalJSON(raw []byte) error {
	type stopResult ConsoleStopResult
	var value stopResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("console stop result has trailing JSON")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, name := range []string{"vm_id", "stopped"} {
		field, ok := fields[name]
		if !ok || bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
			return fmt.Errorf("console stop result %s is required", name)
		}
	}
	*result = ConsoleStopResult(value)
	return nil
}

func (request ConsoleObserveRequest) Validate() error { return validateConsoleVMID(request.VMID) }
func (request ConsoleStopRequest) Validate() error    { return validateConsoleVMID(request.VMID) }

func validateConsoleVMID(vmID string) error {
	if !vmIDPattern.MatchString(vmID) {
		return errs.New(errs.CodeValidationFailed, "console VM ID is not canonical")
	}
	return nil
}

func (result ConsoleObserveResult) Validate(request ConsoleObserveRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if result.VMID != request.VMID {
		return consoleAuthorityError(request.VMID, "console response VM does not match request", nil)
	}
	switch result.State {
	case ConsoleRunning:
		uidText := filepath.Base(filepath.Dir(filepath.Dir(result.SocketPath)))
		uid, uidErr := strconv.ParseUint(uidText, 10, 32)
		if result.PID == nil || validateInstanceProcessLocator(instanceProcessLocator{
			pid: *result.PID, startTimeTicks: 1,
		}) != nil || uidErr != nil || uid == 0 || uid == math.MaxUint32 ||
			result.SocketPath != consoleRuntimeSocketPath(uint32(uid), request.VMID) {
			return consoleAuthorityError(request.VMID, "running console result is incomplete", nil)
		}
	case ConsoleAbsent, ConsoleStopping, ConsoleStopped:
		if result.PID != nil || result.SocketPath != "" {
			return consoleAuthorityError(request.VMID, "inactive console result contains an endpoint", nil)
		}
	default:
		return consoleAuthorityError(request.VMID, "console state is invalid", nil)
	}
	return nil
}

func (result ConsoleStopResult) Validate(request ConsoleStopRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if result.VMID != request.VMID {
		return consoleAuthorityError(request.VMID, "console stop response VM does not match request", nil)
	}
	return nil
}

func consoleRuntimeSocketPath(ownerUID uint32, vmID string) string {
	return fmt.Sprintf("/run/mvmctl/runtime/%d/%s/%s", ownerUID, vmID, infra.VMConsoleSocketFilename)
}

func consoleAuthorityError(vmID, message string, cause error) *errs.DomainError {
	if cause == nil {
		return errs.New(errs.CodeConsoleRelayFailed, message, errs.WithEntity(vmID))
	}
	return errs.WrapMsg(errs.CodeConsoleRelayFailed, message, cause, errs.WithEntity(vmID))
}

func instanceRelayNeedsProcess(record instanceRecord) bool {
	return record.launch != nil && record.launch.relay.kind == instanceRelayPresent &&
		(record.active == nil || record.active.relay.lifecycle != instanceRelayStopped)
}
