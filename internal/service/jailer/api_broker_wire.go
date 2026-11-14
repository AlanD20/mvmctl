package jailer

import (
	"bytes"
	"encoding/json"
	"io"
)

const (
	// VMAPIReadyAction checks the protected API peer without changing VM state.
	VMAPIReadyAction = "jailer-vm-api-ready"
	// VMPauseAction selects the fixed Firecracker pause operation.
	VMPauseAction = "jailer-vm-pause"
	// VMResumeAction selects the fixed Firecracker resume operation.
	VMResumeAction = "jailer-vm-resume"
	// VMStartInstanceAction selects the fixed Firecracker instance-start operation.
	VMStartInstanceAction = "jailer-vm-start-instance"
)

// VMAPIRequest selects an owned VM without accepting any API path, method, body, or socket.
type VMAPIRequest struct {
	VMID string `json:"vm_id"`
}

// VMAPIReadyResult confirms that the protected socket accepted a connection
// from the admitted Firecracker process. It does not assert guest readiness.
type VMAPIReadyResult struct {
	VMID  string `json:"vm_id"`
	Ready bool   `json:"ready"`
}

// Validate rejects an unconfirmed or mismatched readiness observation.
func (result VMAPIReadyResult) Validate(request VMAPIRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if result.VMID != request.VMID || !result.Ready {
		return instanceValidationError("VM API readiness returned an unconfirmed result")
	}
	return nil
}

// Validate requires one canonical VM identity.
func (request VMAPIRequest) Validate() error {
	if !vmIDPattern.MatchString(request.VMID) {
		return instanceValidationError("VM API operation requires a canonical VM ID")
	}
	return nil
}

// UnmarshalJSON keeps the broker request closed even outside the dispatcher decoder.
func (request *VMAPIRequest) UnmarshalJSON(raw []byte) error {
	type wire VMAPIRequest
	var value wire
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return instanceValidationError("VM API operation has trailing JSON")
	}
	*request = VMAPIRequest(value)
	return request.Validate()
}

// VMAPIResult confirms an observed successful response from the authenticated API peer.
type VMAPIResult struct {
	VMID    string `json:"vm_id"`
	Applied bool   `json:"applied"`
}

// Validate rejects a mismatched or unconfirmed result.
func (result VMAPIResult) Validate(request VMAPIRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if result.VMID != request.VMID || !result.Applied {
		return instanceValidationError("VM API operation returned an unconfirmed result")
	}
	return nil
}
