package network

import (
	"context"
	"errors"
	"maps"
	"regexp"

	"mvmctl/pkg/errs"
)

const (
	networkAuthorityRecordSchemaVersion = uint32(1)
	maxNetworkAuthorityRecordBytes      = 4096
)

var bootIDPattern = regexp.MustCompile(
	`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`,
)

type networkCaller struct {
	uid uint32
}

type networkAuthorityRecord struct {
	schemaVersion uint32
	ownerUID      uint32
	networkID     NetworkID
	bootID        string
	managedBridge linkIdentity
}

func newNetworkAuthorityRecord(
	caller networkCaller,
	networkID NetworkID,
	bootID string,
	bridge linkIdentity,
) (networkAuthorityRecord, error) {
	if err := validateNetworkRegistrationInput(caller, networkID, bridge); err != nil {
		return networkAuthorityRecord{}, err
	}
	record := networkAuthorityRecord{
		schemaVersion: networkAuthorityRecordSchemaVersion,
		ownerUID:      caller.uid,
		networkID:     networkID,
		bootID:        bootID,
		managedBridge: bridge,
	}
	if err := validateNetworkAuthorityRecord(record); err != nil {
		return networkAuthorityRecord{}, err
	}
	return record, nil
}

func validateNetworkRegistrationInput(caller networkCaller, networkID NetworkID, bridge linkIdentity) error {
	key := networkKey{ownerUID: OwnerUID(caller.uid), networkID: networkID}
	if err := validateNetworkKey(key); err != nil {
		return err
	}
	expectedName := deriveNetworkLinkName("mvb", key, "managed-bridge")
	if bridge.name != expectedName || bridge.ifIndex <= 0 || !validNamespaceIdentity(bridge.namespace) {
		return networkAuthorityValidationError("managed bridge identity is invalid")
	}
	return nil
}

func validateNetworkAuthorityRecord(record networkAuthorityRecord) error {
	if record.schemaVersion != networkAuthorityRecordSchemaVersion {
		return networkAuthorityCorruptError("unsupported network authority schema", nil)
	}
	key := networkKey{ownerUID: OwnerUID(record.ownerUID), networkID: record.networkID}
	if err := validateNetworkKey(key); err != nil {
		return networkAuthorityCorruptError("network authority identity is invalid", err)
	}
	if !bootIDPattern.MatchString(record.bootID) {
		return networkAuthorityCorruptError("network authority boot identity is invalid", nil)
	}
	expectedName := deriveNetworkLinkName("mvb", key, "managed-bridge")
	if record.managedBridge.name != expectedName ||
		record.managedBridge.ifIndex <= 0 ||
		!validNamespaceIdentity(record.managedBridge.namespace) {
		return networkAuthorityCorruptError("network authority managed bridge identity is invalid", nil)
	}
	return nil
}

func authorizeNetworkOwner(caller networkCaller, record networkAuthorityRecord) error {
	if caller.uid == 0 {
		return networkAuthorityValidationError("network authority caller UID must be non-zero")
	}
	if record.ownerUID != caller.uid {
		return unavailableNetworkAuthorityError(record.networkID)
	}
	return nil
}

func networkAuthorityValidationError(message string) *errs.DomainError {
	return errs.New(errs.CodeValidationFailed, message, errs.WithClass(errs.ClassValidation))
}

func networkAuthorityCorruptError(message string, cause error) *errs.DomainError {
	if cause == nil {
		return errs.New(errs.CodeNetworkError, message, errs.WithClass(errs.ClassInternal))
	}
	return errs.WrapMsg(errs.CodeNetworkError, message, cause, errs.WithClass(errs.ClassInternal))
}

func networkAuthorityEffectError(message string, cause error) *errs.DomainError {
	class := errs.ClassInternal
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		class = errs.ClassRetryable
	}
	return errs.WrapMsg(errs.CodeNetworkError, message, cause, errs.WithClass(class))
}

func unavailableNetworkAuthorityError(networkID NetworkID) *errs.DomainError {
	return errs.New(
		errs.CodeUnauthorized,
		"network identity is not available to the invoking user",
		errs.WithEntity(string(networkID)),
	)
}

func staleNetworkAuthorityError(networkID NetworkID) *errs.DomainError {
	return errs.New(
		errs.CodeNetworkError,
		"network authority does not belong to the current host boot",
		errs.WithClass(errs.ClassConflict),
		errs.WithEntity(string(networkID)),
	)
}

func appendNetworkAuthorityError(primary error, message string, cause error) error {
	if cause == nil {
		return primary
	}
	if primary == nil {
		result := networkAuthorityEffectError(message, cause)
		result.Details = networkAuthorityCleanupDetails(nil, message)
		return result
	}
	domainErr := errs.AsDomainError(primary)
	if domainErr == nil {
		result := networkAuthorityEffectError(primary.Error(), errors.Join(primary, cause))
		result.Details = networkAuthorityCleanupDetails(nil, message)
		return result
	}
	result := *domainErr
	result.Details = networkAuthorityCleanupDetails(domainErr.Details, message)
	result.Err = errors.Join(primary, cause)
	return &result
}

func networkAuthorityCleanupDetails(details map[string]any, diagnostic string) map[string]any {
	result := maps.Clone(details)
	if result == nil {
		result = make(map[string]any, 2)
	}
	result["cleanup_required"] = true
	if _, exists := result["cleanup_diagnostic"]; !exists {
		// The diagnostic is one fixed call-site string; cleanup cause text remains only in the error chain.
		result["cleanup_diagnostic"] = diagnostic
	}
	return result
}
