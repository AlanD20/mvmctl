package jailer

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"mvmctl/pkg/errs"
)

const (
	vmExecCapacityRecordSchemaVersion = uint32(1)
	vmExecCapacityKind                = "vm_exec"
	maxVMExecCapacityRecordBytes      = 4096
	vmExecReservationIDLength         = 32
)

var vmExecReservationIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

type vmExecReservationID struct {
	value string
}

type vmExecCapacityCaller struct {
	ownerUID uint32
}

type vmExecCapacityIntent struct {
	caller                      vmExecCapacityCaller
	sourceVMID                  string
	sourceLaunchGeneration      uint64
	destinationVMID             string
	destinationLaunchGeneration uint64
}

type vmExecCapacityRecord struct {
	schemaVersion               uint32
	kind                        string
	reservationID               vmExecReservationID
	ownerUID                    uint32
	hostBootID                  string
	sourceVMID                  string
	sourceLaunchGeneration      uint64
	destinationVMID             string
	destinationLaunchGeneration uint64
	createdBoottimeNS           uint64
	expiresBoottimeNS           uint64
}

func newVMExecReservationID(value string) (vmExecReservationID, error) {
	if !vmExecReservationIDPattern.MatchString(value) {
		return vmExecReservationID{}, vmExecCapacityValidationError(
			fmt.Sprintf(
				"VM-exec reservation ID must be %d lowercase hexadecimal characters",
				vmExecReservationIDLength,
			),
		)
	}
	return vmExecReservationID{value: value}, nil
}

func newVMExecCapacityRecord(
	intent vmExecCapacityIntent,
	reservationID vmExecReservationID,
	hostBootID string,
	createdBoottimeNS uint64,
	expiresBoottimeNS uint64,
) (vmExecCapacityRecord, error) {
	record := vmExecCapacityRecord{
		schemaVersion:               vmExecCapacityRecordSchemaVersion,
		kind:                        vmExecCapacityKind,
		reservationID:               reservationID,
		ownerUID:                    intent.caller.ownerUID,
		hostBootID:                  hostBootID,
		sourceVMID:                  intent.sourceVMID,
		sourceLaunchGeneration:      intent.sourceLaunchGeneration,
		destinationVMID:             intent.destinationVMID,
		destinationLaunchGeneration: intent.destinationLaunchGeneration,
		createdBoottimeNS:           createdBoottimeNS,
		expiresBoottimeNS:           expiresBoottimeNS,
	}
	if err := validateVMExecCapacityRecord(record); err != nil {
		return vmExecCapacityRecord{}, err
	}
	return record, nil
}

func validateVMExecCapacityIntent(intent vmExecCapacityIntent) error {
	if intent.caller.ownerUID == 0 {
		return vmExecCapacityValidationError("VM-exec capacity caller UID must be non-zero")
	}
	if !vmIDPattern.MatchString(intent.sourceVMID) || !vmIDPattern.MatchString(intent.destinationVMID) {
		return vmExecCapacityValidationError("VM-exec capacity VM identity is invalid")
	}
	if intent.sourceVMID == intent.destinationVMID {
		return vmExecCapacityValidationError("VM-exec capacity requires distinct source and destination VMs")
	}
	if intent.sourceLaunchGeneration == 0 || intent.destinationLaunchGeneration == 0 {
		return vmExecCapacityValidationError("VM-exec capacity launch generation must be positive")
	}
	return nil
}

func validateVMExecCapacityRecord(record vmExecCapacityRecord) error {
	if record.schemaVersion != vmExecCapacityRecordSchemaVersion || record.kind != vmExecCapacityKind {
		return vmExecCapacityCorruptError("unsupported VM-exec capacity record schema", nil)
	}
	if !vmExecReservationIDPattern.MatchString(record.reservationID.value) {
		return vmExecCapacityCorruptError("VM-exec capacity reservation identity is invalid", nil)
	}
	intent := vmExecCapacityIntent{
		caller:                      vmExecCapacityCaller{ownerUID: record.ownerUID},
		sourceVMID:                  record.sourceVMID,
		sourceLaunchGeneration:      record.sourceLaunchGeneration,
		destinationVMID:             record.destinationVMID,
		destinationLaunchGeneration: record.destinationLaunchGeneration,
	}
	if err := validateVMExecCapacityIntent(intent); err != nil {
		return vmExecCapacityCorruptError("VM-exec capacity record identity is invalid", err)
	}
	if !hostBootIDPattern.MatchString(record.hostBootID) {
		return vmExecCapacityCorruptError("VM-exec capacity host boot identity is invalid", nil)
	}
	if record.expiresBoottimeNS <= record.createdBoottimeNS {
		return vmExecCapacityCorruptError("VM-exec capacity lifetime is invalid", nil)
	}
	return nil
}

func vmExecCapacityValidationError(message string) *errs.DomainError {
	return errs.New(errs.CodeValidationFailed, message, errs.WithClass(errs.ClassValidation))
}

func vmExecCapacityCorruptError(message string, cause error) *errs.DomainError {
	if cause == nil {
		return errs.New(errs.CodeVMAtomicFailed, message, errs.WithClass(errs.ClassInternal))
	}
	return errs.WrapMsg(errs.CodeVMAtomicFailed, message, cause, errs.WithClass(errs.ClassInternal))
}

func vmExecCapacityEffectError(message string, cause error) *errs.DomainError {
	class := errs.ClassInternal
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		class = errs.ClassRetryable
	}
	return errs.WrapMsg(errs.CodeVMAtomicFailed, message, cause, errs.WithClass(class))
}

func vmExecCapacityUnauthorizedError() *errs.DomainError {
	return errs.New(errs.CodeUnauthorized, "VM-exec reservation is not available to the invoking user")
}

func vmExecCapacityExhaustedError() *errs.DomainError {
	return errs.New(
		errs.CodeVMResourceExhausted,
		"VM-exec global capacity is exhausted",
		errs.WithClass(errs.ClassConflict),
	)
}
