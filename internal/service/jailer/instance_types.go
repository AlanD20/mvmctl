package jailer

import (
	"crypto/sha256"
	"fmt"
	"math"
	"regexp"
	"strings"

	"mvmctl/internal/infra"
	"mvmctl/pkg/errs"
)

const (
	instanceRecordSchemaVersion = uint32(1)
	maxInstanceRecordBytes      = 16 * 1024
)

var authorityHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type instanceLifecycle string

const (
	instanceLifecycleRegistered instanceLifecycle = "registered"
	instanceLifecycleCleaning   instanceLifecycle = "cleaning"
	instanceLifecycleCleaned    instanceLifecycle = "cleaned"
)

type instanceCaller struct {
	uid uint32
}

type releaseSlot struct {
	version      string
	architecture string
}

type releaseIdentity struct {
	version           string
	architecture      string
	firecrackerSHA256 string
	jailerSHA256      string
}

type processIdentity struct {
	pid            int
	startTimeTicks uint64
	cgroupPath     string
}

type launchRegistration struct {
	vmID    string
	release releaseIdentity
	process processIdentity
}

type instanceRecord struct {
	schemaVersion     uint32
	ownerUID          uint32
	vmID              string
	lifecycle         instanceLifecycle
	release           releaseIdentity
	process           processIdentity
	cleanupGeneration uint64
}

func registerInstanceRecord(
	caller instanceCaller,
	registration launchRegistration,
	existing *instanceRecord,
) (instanceRecord, error) {
	if err := validateInstanceCaller(caller); err != nil {
		return instanceRecord{}, err
	}
	if err := validateLaunchRegistration(registration); err != nil {
		return instanceRecord{}, err
	}
	cleanupGeneration := uint64(0)
	if existing != nil {
		if err := validateInstanceRecord(*existing); err != nil {
			return instanceRecord{}, err
		}
		if err := authorizeInstanceRecord(caller, *existing); err != nil {
			return instanceRecord{}, err
		}
		if existing.vmID != registration.vmID {
			return instanceRecord{}, instanceValidationError("registered VM identity does not match existing authority")
		}
		if existing.lifecycle != instanceLifecycleCleaned {
			return instanceRecord{}, errs.AlreadyExists(
				errs.CodeVMAlreadyExists,
				"vm already has active privileged authority",
				errs.WithEntity(registration.vmID),
			)
		}
		cleanupGeneration = existing.cleanupGeneration
	}
	record := instanceRecord{
		schemaVersion:     instanceRecordSchemaVersion,
		ownerUID:          caller.uid,
		vmID:              registration.vmID,
		lifecycle:         instanceLifecycleRegistered,
		release:           registration.release,
		process:           registration.process,
		cleanupGeneration: cleanupGeneration,
	}
	if err := validateInstanceRecord(record); err != nil {
		return instanceRecord{}, err
	}
	return record, nil
}

func beginInstanceCleanup(caller instanceCaller, record instanceRecord) (instanceRecord, error) {
	if err := validateInstanceRecord(record); err != nil {
		return instanceRecord{}, err
	}
	if err := authorizeInstanceRecord(caller, record); err != nil {
		return instanceRecord{}, err
	}
	switch record.lifecycle {
	case instanceLifecycleRegistered:
		if record.cleanupGeneration == ^uint64(0) {
			return instanceRecord{}, instanceStateError(record.vmID, "cleanup generation is exhausted")
		}
		record.lifecycle = instanceLifecycleCleaning
		record.cleanupGeneration++
	case instanceLifecycleCleaning:
		return record, nil
	case instanceLifecycleCleaned:
		return instanceRecord{}, errs.New(
			errs.CodeVMNotRunning,
			"vm has no registered privileged runtime",
			errs.WithEntity(record.vmID),
		)
	default:
		return instanceRecord{}, instanceStateError(record.vmID, "vm authority lifecycle is invalid")
	}
	return record, nil
}

func completeInstanceCleanup(record instanceRecord) (instanceRecord, error) {
	if err := validateInstanceRecord(record); err != nil {
		return instanceRecord{}, err
	}
	if record.lifecycle != instanceLifecycleCleaning {
		return instanceRecord{}, instanceStateError(record.vmID, "vm cleanup is not in progress")
	}
	record.lifecycle = instanceLifecycleCleaned
	return record, nil
}

func authorizeInstanceRecord(caller instanceCaller, record instanceRecord) error {
	if err := validateInstanceCaller(caller); err != nil {
		return err
	}
	if caller.uid != record.ownerUID {
		return errs.New(
			errs.CodeUnauthorized,
			"vm privileged authority belongs to another user",
			errs.WithEntity(record.vmID),
		)
	}
	return nil
}

func validateInstanceCaller(caller instanceCaller) error {
	if caller.uid == 0 {
		return instanceValidationError("invoking user UID must be non-zero")
	}
	return nil
}

func validateLaunchRegistration(registration launchRegistration) error {
	if !vmIDPattern.MatchString(registration.vmID) {
		return instanceValidationError("vm ID is invalid")
	}
	if err := validateReleaseIdentityValue(registration.release); err != nil {
		return instanceValidationError(err.Error())
	}
	if err := validateProcessIdentityValue(registration.vmID, registration.process); err != nil {
		return instanceValidationError(err.Error())
	}
	return nil
}

func validateInstanceRecord(record instanceRecord) error {
	if record.schemaVersion != instanceRecordSchemaVersion {
		return instanceAtomicError("unsupported instance authority schema", nil)
	}
	if record.ownerUID == 0 {
		return instanceAtomicError("instance authority owner UID must be non-zero", nil)
	}
	if !vmIDPattern.MatchString(record.vmID) {
		return instanceAtomicError("instance authority VM ID is invalid", nil)
	}
	switch record.lifecycle {
	case instanceLifecycleRegistered, instanceLifecycleCleaning, instanceLifecycleCleaned:
	default:
		return instanceAtomicError("instance authority lifecycle is invalid", nil)
	}
	if err := validateReleaseIdentityValue(record.release); err != nil {
		return instanceAtomicError(err.Error(), nil)
	}
	if err := validateProcessIdentityValue(record.vmID, record.process); err != nil {
		return instanceAtomicError(err.Error(), nil)
	}
	return nil
}

func validateReleaseIdentityValue(release releaseIdentity) error {
	if err := validateReleaseSlotValue(releaseSlotForIdentity(release)); err != nil {
		return err
	}
	if !authorityHashPattern.MatchString(release.firecrackerSHA256) ||
		!authorityHashPattern.MatchString(release.jailerSHA256) {
		return fmt.Errorf(
			"instance authority release hashes must be %d lowercase hexadecimal characters",
			sha256.Size*2,
		)
	}
	return nil
}

func validateReleaseSlotValue(slot releaseSlot) error {
	if !versionPattern.MatchString(slot.version) || strings.Contains(slot.version, "..") {
		return fmt.Errorf("instance authority release version is invalid")
	}
	if slot.architecture != "x86_64" && slot.architecture != "aarch64" {
		return fmt.Errorf("instance authority release architecture is invalid")
	}
	return nil
}

func releaseSlotForIdentity(release releaseIdentity) releaseSlot {
	return releaseSlot{version: release.version, architecture: release.architecture}
}

func validateProcessIdentityValue(vmID string, process processIdentity) error {
	if process.pid <= 0 || process.pid > math.MaxInt32 || process.startTimeTicks == 0 {
		return fmt.Errorf("instance authority process identity is invalid")
	}
	expectedCgroup := infra.CgroupV2Root + "/" + infra.JailerCgroupParent + "/" + vmID
	if process.cgroupPath != expectedCgroup {
		return fmt.Errorf("instance authority cgroup identity is invalid")
	}
	return nil
}

func instanceValidationError(message string) *errs.DomainError {
	return errs.New(errs.CodeValidationFailed, message)
}

func instanceStateError(vmID, message string) *errs.DomainError {
	return errs.New(errs.CodeVMStateInvalid, message, errs.WithEntity(vmID))
}

func instanceAtomicError(message string, cause error) *errs.DomainError {
	if cause == nil {
		return errs.New(errs.CodeVMAtomicFailed, message, errs.WithClass(errs.ClassInternal))
	}
	return errs.WrapMsg(errs.CodeVMAtomicFailed, message, cause, errs.WithClass(errs.ClassInternal))
}
