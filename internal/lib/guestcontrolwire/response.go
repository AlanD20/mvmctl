package guestcontrolwire

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/netip"
	"time"
)

// Ownership describes the resource relative to the authenticated guest session.
// Other guests' identities and host launch records are never sent to the caller.
type Ownership string

const (
	Unassociated Ownership = "unassociated"
	OwnedBySelf  Ownership = "self"
	OwnedByOther Ownership = "other"
)

// FloatingIPResult is an observation, not permission to retain an address indefinitely.
// Remaining lease time starts at the host observation, not at response receipt.
type FloatingIPResult struct {
	ResourceID           string    `json:"resource_id"`
	IPv4                 string    `json:"ipv4"`
	Generation           uint64    `json:"generation"`
	Ownership            Ownership `json:"ownership"`
	LeaseRemainingMillis uint64    `json:"lease_remaining_ms"`
}

// FailureCode is the closed public projection of a failed host operation.
// The host selects a code explicitly; wrapped errors and details never cross the wire.
type FailureCode string

const (
	FailureInvalid        FailureCode = "invalid"
	FailureDenied         FailureCode = "denied"
	FailureConflict       FailureCode = "conflict"
	FailureUnavailable    FailureCode = "unavailable"
	FailureInternal       FailureCode = "internal"
	FailureOutcomeUnknown FailureCode = "outcome_unknown"
)

// Failure contains no free-form host error message, path, token, or wrapped cause.
// It is a wire value, not a second Go error type.
type Failure struct {
	Code FailureCode `json:"code"`
}

// Response carries exactly one result or failure for one request.
type Response struct {
	RequestID string            `json:"request_id"`
	Operation Operation         `json:"operation"`
	Result    *FloatingIPResult `json:"result,omitempty"`
	Failure   *Failure          `json:"error,omitempty"`
}

type responseEnvelope struct {
	SchemaVersion uint16            `json:"schema_version"`
	RequestID     string            `json:"request_id"`
	Feature       string            `json:"feature"`
	Operation     Operation         `json:"operation"`
	Result        *FloatingIPResult `json:"result,omitempty"`
	Failure       *Failure          `json:"error,omitempty"`
}

// ValidateFor binds a response to the outstanding request before the agent uses it.
func (response Response) ValidateFor(request Request) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if response.RequestID != request.RequestID || response.Operation != request.Operation {
		return invalid("Guest Control response does not match the outstanding request")
	}
	if (response.Result == nil) == (response.Failure == nil) {
		return invalid("Guest Control response requires exactly one result or error")
	}
	if response.Failure != nil {
		switch response.Failure.Code {
		case FailureInvalid, FailureDenied, FailureConflict, FailureUnavailable, FailureInternal, FailureOutcomeUnknown:
			return nil
		default:
			return invalid("unsupported Guest Control failure code")
		}
	}
	result := response.Result
	if result.ResourceID != request.Input.ResourceID || result.Generation == 0 {
		return invalid("floating-IP response resource or generation is invalid")
	}
	if result.LeaseRemainingMillis > uint64(math.MaxInt64/int64(time.Millisecond)) {
		return invalid("floating-IP lease time exceeds the supported duration")
	}
	address, err := netip.ParseAddr(result.IPv4)
	if err != nil || !address.Is4() || !address.IsPrivate() || address.String() != result.IPv4 {
		return invalid("floating-IP response requires a canonical private IPv4 address")
	}
	switch result.Ownership {
	case Unassociated:
		if result.LeaseRemainingMillis != 0 {
			return invalid("unassociated floating IP must not report an active lease")
		}
	case OwnedBySelf, OwnedByOther:
		if result.LeaseRemainingMillis == 0 {
			return invalid("associated floating IP requires remaining lease time")
		}
	default:
		return invalid("unsupported floating-IP ownership state")
	}
	if request.Operation != Inspect && result.Generation < request.Input.ExpectedGeneration {
		return invalid("floating-IP response regressed the observed ownership generation")
	}
	switch request.Operation {
	case Associate, Renew:
		if result.Ownership != OwnedBySelf {
			return invalid("successful floating-IP association or renewal requires self ownership")
		}
		if request.Operation == Renew && result.Generation != request.Input.ExpectedGeneration {
			return invalid("floating-IP renewal changed the ownership generation")
		}
	case Release:
		if result.Ownership != Unassociated {
			return invalid("successful floating-IP release requires unassociated state")
		}
	}
	return nil
}

// EncodeResponse writes a bounded response after binding it to its request.
func EncodeResponse(ctx context.Context, writer io.Writer, request Request, response Response) error {
	if err := response.ValidateFor(request); err != nil {
		return err
	}
	body, err := json.Marshal(responseEnvelope{
		SchemaVersion: 1, RequestID: response.RequestID, Feature: "floating_ip", Operation: response.Operation,
		Result: response.Result, Failure: response.Failure,
	})
	if err != nil {
		return invalid("cannot encode Guest Control response")
	}
	return writeFrame(ctx, writer, body)
}

// DecodeResponse consumes one frame and refuses a result for any other outstanding request.
func DecodeResponse(ctx context.Context, reader io.Reader, request Request) (Response, error) {
	if err := request.Validate(); err != nil {
		return Response{}, err
	}
	body, err := readFrame(ctx, reader)
	if err != nil {
		return Response{}, err
	}
	// Determine the union arm without trusting json.Unmarshal's case folding or duplicate handling.
	var envelope responseEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return Response{}, invalid("cannot decode Guest Control response")
	}
	arm := "result"
	nested := []string{"resource_id", "ipv4", "generation", "ownership", "lease_remaining_ms"}
	if envelope.Failure != nil {
		arm, nested = "error", []string{"code"}
	}
	fields, err := closedObject(body, []string{"schema_version", "request_id", "feature", "operation", arm})
	if err != nil {
		return Response{}, err
	}
	if _, err := closedObject(fields[arm], nested); err != nil {
		return Response{}, err
	}
	if envelope.SchemaVersion != 1 || envelope.Feature != "floating_ip" {
		return Response{}, invalid("unsupported Guest Control response schema or feature")
	}
	response := Response{
		RequestID: envelope.RequestID,
		Operation: envelope.Operation,
		Result:    envelope.Result,
		Failure:   envelope.Failure,
	}
	if err := response.ValidateFor(request); err != nil {
		return Response{}, err
	}
	return response, nil
}
