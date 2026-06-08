// Package errs provides the single error type for the entire mvmctl project.
//
// ── Error flow ──────────────────────────────────────────────────────────
//
//  1. Core domain/services create errors via errs.New / Wrap / WrapMsg
//  2. API layer orchestrates multiple domains, returning errors to CLI
//  3. CLI renders errors for the user
//  4. Enricher uses errors.As + IsMVMError() for soft-fail during enrichment
//
// ── Creating errors ────────────────────────────────────────────────────
//
//	// Simple error — Class and Op auto-derived from Code
//	errs.New(errs.CodeVMNotFound, "VM not found: my-vm")
//
//	// Wrapping a cause
//	errs.Wrap(errs.CodeNetworkBridgeFailed, err)
//
//	// Wrapping with a user-facing message
//	errs.WrapMsg(errs.CodeDownloadFailed, "Failed to fetch image", err)
//
//	// With structured details
//	errs.New(errs.CodeRootPartitionDetection, "no candidates",
//	    errs.WithDetails(map[string]any{"partitions": parts}))
//
//	// Override auto-derived Class
//	errs.New(errs.CodePrivilegeRequired, "need sudo",
//	    errs.WithClass(errs.ClassNeedsInteraction))
//
// ── Checking errors ────────────────────────────────────────────────────
//
//	var de *DomainError
//	if errors.As(err, &de) {
//	    switch de.Code {
//	    case errs.CodeVMNotFound:
//	        // handle not found
//	    }
//	}
//
//	if errs.IsNotFound(err) { ... }
//	if errs.IsRetryable(err) { ... }
//
// ── Rules ──────────────────────────────────────────────────────────────
//
//   - Do NOT create new error types. Always use DomainError.
//   - Do NOT create new factory functions. Use New / Wrap / WrapMsg.
//   - Use struct literal &DomainError{...} only when you need to set
//     fields that aren't covered by the helpers (rare edge cases).
package errs

import (
	"errors"
	"fmt"
	"maps"
	"runtime/debug"
)

// ── Error classification ────────────────────────────────────────────────

// Class categorises an error semantically, independent of its Code.
type Class int

const (
	ClassUnknown          Class = iota // Unclassified / default
	ClassValidation                    // Invalid input, not found, already exists
	ClassConflict                      // Resource conflict (e.g. already exists with different owner)
	ClassRetryable                     // Temporary failure, may succeed if retried
	ClassInternal                      // Internal/system error
	ClassNeedsInteraction              // Needs user action (sudo, confirmation, etc.)
)

// ── Code → Class / Op lookup tables ────────────────────────────────────

// classForCode returns the canonical Class for a given Code.
// Every Code has a fixed Class to ensure consistency.
func classForCode(code Code) Class {
	if c, ok := codeClassMap[code]; ok {
		return c
	}
	return ClassUnknown
}

// opForCode returns the canonical Op (operation name) for a given Code.
func opForCode(code Code) string {
	if op, ok := codeOpMap[code]; ok {
		return op
	}
	return ""
}

var codeClassMap = map[Code]Class{
	// ── VM domain ──
	CodeVMNotFound:           ClassValidation,
	CodeVMAlreadyExists:      ClassConflict,
	CodeVMStateInvalid:       ClassValidation,
	CodeVMCreateFailed:       ClassInternal,
	CodeVMBuilderFailed:      ClassInternal,
	CodeVMResolveFailed:      ClassValidation,
	CodeVMResourceExhausted:  ClassValidation,
	CodeVMBinaryNotFound:     ClassValidation,
	CodeVMImageNotFound:      ClassValidation,
	CodeVMKernelNotFound:     ClassValidation,
	CodeVMNetworkNotFound:    ClassValidation,
	CodeVMSSHKeyNotFound:     ClassValidation,
	CodeVMNameCollision:      ClassValidation,
	CodeVMAtomicFailed:       ClassInternal,
	CodeVMCreateFailure:      ClassInternal,
	CodeVMSnapshotFailed:     ClassInternal,
	CodeVMLoadSnapshotFailed: ClassInternal,
	CodeVMImportFailed:       ClassInternal,

	// ── Network domain ──
	CodeNetworkSubnetOverlap:       ClassValidation,
	CodeNetworkNotFound:            ClassValidation,
	CodeNetworkAlreadyExists:       ClassConflict,
	CodeNetworkBridgeFailed:        ClassInternal,
	CodeNetworkNATFailed:           ClassInternal,
	CodeNetworkLeaseFailed:         ClassInternal,
	CodeNetworkLeaseExhausted:      ClassRetryable,
	CodeNetworkFirewallFailed:      ClassInternal,
	CodeNetworkCreateFailed:        ClassInternal,
	CodeNetworkRemoveFailed:        ClassInternal,
	CodeNetworkDefaultSetFailed:    ClassInternal,
	CodeNetworkDefaultCreateFailed: ClassInternal,

	// ── Image domain ──
	CodeImageNotFound:           ClassValidation,
	CodeImageAlreadyExists:      ClassConflict,
	CodeImagePullFailed:         ClassInternal,
	CodeImageImportFailed:       ClassInternal,
	CodeImageChecksumMismatch:   ClassValidation,
	CodeImageCorrupt:            ClassValidation,
	CodeImageEmpty:              ClassValidation,
	CodeImageFormatInvalid:      ClassValidation,
	CodeImageError:              ClassInternal,
	CodeImageCompressionError:   ClassInternal,
	CodeImageDecompressionError: ClassInternal,
	CodeRootPartitionDetection:  ClassInternal,
	CodeTieDetected:             ClassInternal,
	CodeImageAcquireFailed:      ClassInternal,
	CodeImageWarmFailed:         ClassInternal,

	// ── Kernel domain ──
	CodeKernelNotFound:         ClassValidation,
	CodeKernelBuildFailed:      ClassRetryable,
	CodeKernelConfigFailed:     ClassInternal,
	CodeKernelPullFailed:       ClassInternal,
	CodeKernelImportFailed:     ClassInternal,
	CodeKernelDefaultSetFailed: ClassInternal,

	// ── Binary domain ──
	CodeBinaryNotFound:            ClassValidation,
	CodeBinaryAlreadyExists:       ClassConflict,
	CodeBinaryVersionGate:         ClassValidation,
	CodeBinaryError:               ClassInternal,
	CodeBinaryPullFailed:          ClassInternal,
	CodeBinaryRemoveFailed:        ClassInternal,
	CodeBinaryDefaultSetFailed:    ClassInternal,
	CodeBinaryEnsureDefaultFailed: ClassInternal,
	CodeBinaryNoCIVersion:         ClassValidation,

	// ── Volume domain ──
	CodeVolumeNotFound:      ClassValidation,
	CodeVolumeAlreadyExists: ClassConflict,
	CodeVolumeError:         ClassInternal,
	CodeVolumeResizeFailed:  ClassInternal,

	// ── Key domain ──
	CodeKeyNotFound:            ClassValidation,
	CodeKeyAlreadyExists:       ClassConflict,
	CodeKeyExportFailed:        ClassValidation,
	CodeKeyDependencyMissing:   ClassValidation,
	CodeKeyCreateFailed:        ClassInternal,
	CodeKeyAddFailed:           ClassInternal,
	CodeKeyDefaultSetFailed:    ClassInternal,
	CodeKeyDefaultsClearFailed: ClassInternal,

	// ── Host domain ──
	CodeHostInitFailed:     ClassInternal,
	CodeHostCleanFailed:    ClassInternal,
	CodeHostResetFailed:    ClassInternal,
	CodePrivilegeRequired:  ClassNeedsInteraction,
	CodePrivilegeSudoers:   ClassValidation,
	CodeHostInfoFailed:     ClassInternal,
	CodeHostCapacityFailed: ClassInternal,

	// ── Cloud-init domain ──
	CodeCloudInitProvisionFailed: ClassInternal,
	CodeCloudInitNetModeFailed:   ClassInternal,
	CodeCloudInitISOModeFailed:   ClassInternal,
	CodeCloudInitInjectFailed:    ClassInternal,
	CodeCloudInitModeError:       ClassValidation,
	CodeCloudInitOffModeError:    ClassInternal,

	// ── Console domain ──
	CodeConsoleRelayFailed: ClassInternal,
	CodeConsoleNotRunning:  ClassValidation,
	CodeConsoleKillFailed:  ClassInternal,

	// ── Logs domain ──
	CodeLogsError: ClassInternal,

	// ── Firecracker domain ──
	CodeFirecrackerError:          ClassInternal,
	CodeFirecrackerClientError:    ClassInternal,
	CodeFirecrackerSpawnError:     ClassInternal,
	CodeFirecrackerConfigError:    ClassValidation,
	CodeFirecrackerSocketNotFound: ClassValidation,

	// ── GuestFS domain ──
	CodeGuestfsError:        ClassInternal,
	CodeGuestfsNotAvailable: ClassInternal,
	CodeGuestfsWriteError:   ClassInternal,

	// ── LoopMount domain ──
	CodeLoopMountError:          ClassInternal,
	CodeLoopMountBinaryNotFound: ClassValidation,
	CodeLoopMountTimeout:        ClassRetryable,

	// ── SSH / CP domain ──
	CodeSSHError:              ClassInternal,
	CodeCPError:               ClassInternal,
	CodeCPSourceNotFound:      ClassValidation,
	CodeCPSourceFailed:        ClassInternal,
	CodeCPCopyFailed:          ClassInternal,
	CodeCPDestinationExists:   ClassValidation,
	CodeCPDestinationFailed:   ClassInternal,
	CodeCPDestinationNotDir:   ClassValidation,
	CodeCPMultiSourceNoVMDest: ClassValidation,
	CodeCPResolveFailed:       ClassInternal,
	CodeCPNoVMSpecified:       ClassValidation,
	CodeCPVMNoIP:              ClassInternal,
	CodeCPVMNotFound:          ClassValidation,

	// ── BundledAsset domain ──
	CodeBundledAssetError:    ClassInternal,
	CodeBundledAssetNotFound: ClassValidation,

	// ── Cache ──
	CodeCacheCleanFailed: ClassInternal,

	// ── Common ──
	CodeNetworkError:         ClassInternal,
	CodeKeyError:             ClassInternal,
	CodeVersionResolveFailed: ClassInternal,
	CodeInternal:             ClassInternal,
	CodeNotImplemented:       ClassInternal,
	CodeValidationFailed:     ClassValidation,
	CodeDatabaseError:        ClassInternal,
	CodeMigrationFailed:      ClassInternal,
	CodeProcessError:         ClassInternal,
	CodeDownloadFailed:       ClassInternal,
	CodeHTTPError:            ClassInternal,
	CodeConfigError:          ClassValidation,
}

var codeOpMap = map[Code]string{
	CodeVMNotFound:           "vm",
	CodeVMAlreadyExists:      "vm",
	CodeVMStateInvalid:       "vm",
	CodeVMCreateFailed:       "vm",
	CodeVMBuilderFailed:      "vm",
	CodeVMResolveFailed:      "vm",
	CodeVMResourceExhausted:  "vm",
	CodeVMBinaryNotFound:     "vm",
	CodeVMImageNotFound:      "vm",
	CodeVMKernelNotFound:     "vm",
	CodeVMNetworkNotFound:    "vm",
	CodeVMSSHKeyNotFound:     "vm",
	CodeVMNameCollision:      "vm",
	CodeVMAtomicFailed:       "vm",
	CodeVMCreateFailure:      "vm",
	CodeVMSnapshotFailed:     "vm",
	CodeVMLoadSnapshotFailed: "vm",
	CodeVMImportFailed:       "vm",

	CodeNetworkSubnetOverlap:       "network",
	CodeNetworkNotFound:            "network",
	CodeNetworkAlreadyExists:       "network",
	CodeNetworkBridgeFailed:        "network",
	CodeNetworkNATFailed:           "network",
	CodeNetworkLeaseFailed:         "network",
	CodeNetworkLeaseExhausted:      "network",
	CodeNetworkFirewallFailed:      "network",
	CodeNetworkCreateFailed:        "network",
	CodeNetworkRemoveFailed:        "network",
	CodeNetworkDefaultSetFailed:    "network",
	CodeNetworkDefaultCreateFailed: "network",

	CodeImageNotFound:           "image",
	CodeImageAlreadyExists:      "image",
	CodeImagePullFailed:         "image",
	CodeImageImportFailed:       "image",
	CodeImageChecksumMismatch:   "image",
	CodeImageCorrupt:            "image",
	CodeImageEmpty:              "image",
	CodeImageFormatInvalid:      "image",
	CodeImageError:              "image",
	CodeImageCompressionError:   "image",
	CodeImageDecompressionError: "image",
	CodeRootPartitionDetection:  "image",
	CodeTieDetected:             "image",
	CodeImageAcquireFailed:      "image",
	CodeImageWarmFailed:         "image",

	CodeKernelNotFound:         "kernel",
	CodeKernelBuildFailed:      "kernel",
	CodeKernelConfigFailed:     "kernel",
	CodeKernelPullFailed:       "kernel",
	CodeKernelImportFailed:     "kernel",
	CodeKernelDefaultSetFailed: "kernel",

	CodeBinaryNotFound:            "binary",
	CodeBinaryAlreadyExists:       "binary",
	CodeBinaryVersionGate:         "binary",
	CodeBinaryError:               "binary",
	CodeBinaryPullFailed:          "binary",
	CodeBinaryRemoveFailed:        "binary",
	CodeBinaryDefaultSetFailed:    "binary",
	CodeBinaryEnsureDefaultFailed: "binary",
	CodeBinaryNoCIVersion:         "binary",

	CodeVolumeNotFound:      "volume",
	CodeVolumeAlreadyExists: "volume",
	CodeVolumeError:         "volume",
	CodeVolumeResizeFailed:  "volume",

	CodeKeyNotFound:            "key",
	CodeKeyAlreadyExists:       "key",
	CodeKeyExportFailed:        "key",
	CodeKeyDependencyMissing:   "key",
	CodeKeyCreateFailed:        "key",
	CodeKeyAddFailed:           "key",
	CodeKeyDefaultSetFailed:    "key",
	CodeKeyDefaultsClearFailed: "key",

	CodeHostInitFailed:     "host",
	CodeHostCleanFailed:    "host",
	CodeHostResetFailed:    "host",
	CodePrivilegeRequired:  "host",
	CodePrivilegeSudoers:   "host",
	CodeHostInfoFailed:     "host",
	CodeHostCapacityFailed: "host",

	CodeCloudInitProvisionFailed: "cloudinit",
	CodeCloudInitNetModeFailed:   "cloudinit",
	CodeCloudInitISOModeFailed:   "cloudinit",
	CodeCloudInitInjectFailed:    "cloudinit",
	CodeCloudInitModeError:       "cloudinit",
	CodeCloudInitOffModeError:    "cloudinit",

	CodeConsoleRelayFailed: "console",
	CodeConsoleNotRunning:  "console",
	CodeConsoleKillFailed:  "console",

	CodeLogsError: "logs",

	CodeFirecrackerError:          "vm",
	CodeFirecrackerClientError:    "vm",
	CodeFirecrackerSpawnError:     "vm",
	CodeFirecrackerConfigError:    "vm",
	CodeFirecrackerSocketNotFound: "vm",

	CodeGuestfsError:        "guestfs",
	CodeGuestfsNotAvailable: "guestfs",
	CodeGuestfsWriteError:   "guestfs",

	CodeLoopMountError:          "loopmount",
	CodeLoopMountBinaryNotFound: "loopmount",
	CodeLoopMountTimeout:        "loopmount",

	CodeSSHError: "ssh",

	CodeCPError:               "cp",
	CodeCPSourceNotFound:      "cp",
	CodeCPSourceFailed:        "cp",
	CodeCPCopyFailed:          "cp",
	CodeCPDestinationExists:   "cp",
	CodeCPDestinationFailed:   "cp",
	CodeCPDestinationNotDir:   "cp",
	CodeCPMultiSourceNoVMDest: "cp",
	CodeCPResolveFailed:       "cp",
	CodeCPNoVMSpecified:       "cp",
	CodeCPVMNoIP:              "cp",
	CodeCPVMNotFound:          "cp",

	CodeBundledAssetError:    "asset",
	CodeBundledAssetNotFound: "asset",

	CodeCacheCleanFailed: "cache",

	CodeDatabaseError:        "db",
	CodeMigrationFailed:      "db",
	CodeProcessError:         "process",
	CodeDownloadFailed:       "download",
	CodeHTTPError:            "http",
	CodeConfigError:          "config",
	CodeNetworkError:         "network",
	CodeKeyError:             "key",
	CodeVersionResolveFailed: "version",
	CodeValidationFailed:     "",
	CodeInternal:             "",
	CodeNotImplemented:       "",
}

// ── DomainError ─────────────────────────────────────────────────────────

// DomainError is the single error type for the entire project.
// Every error in the system is a *DomainError.
type DomainError struct {
	Code    Code
	Message string
	Op      string
	Entity  string
	Class   Class
	Err     error
	Details map[string]any
}

// Error returns just the user-facing message.
// This is the standard error interface method.
func (e *DomainError) Error() string {
	return e.Message
}

// Unwrap returns the wrapped error, enabling errors.Is/As chain walking.
func (e *DomainError) Unwrap() error { return e.Err }

// ── Options ──────────────────────────────────────────────────────────────

// ErrorOption configures a DomainError during construction.
type ErrorOption func(*DomainError)

// WithClass overrides the auto-derived Class for the error.
func WithClass(c Class) ErrorOption {
	return func(e *DomainError) { e.Class = c }
}

// WithEntity sets the entity name on the error (e.g. the resource ID).
func WithEntity(entity string) ErrorOption {
	return func(e *DomainError) { e.Entity = entity }
}

// WithDetails sets the structured Details map on the error.
func WithDetails(details map[string]any) ErrorOption {
	return func(e *DomainError) {
		if e.Details == nil {
			e.Details = details
		} else {
			maps.Copy(e.Details, details)
		}
	}
}

// ── Constructors ────────────────────────────────────────────────────────

// New creates a DomainError with the given code and message.
// Class and Op are auto-derived from Code via lookup tables.
// Use options to override auto-derived values or set Entity/Details.
func New(code Code, msg string, opts ...ErrorOption) *DomainError {
	e := &DomainError{
		Code:    code,
		Message: msg,
		Class:   classForCode(code),
		Op:      opForCode(code),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Wrap wraps an existing error. If the wrapped error is a *DomainError,
// its Class is inherited. Otherwise ClassUnknown is used.
func Wrap(code Code, err error, opts ...ErrorOption) *DomainError {
	e := &DomainError{
		Code:  code,
		Err:   err,
		Class: classFrom(err),
		Op:    opForCode(code),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// WrapMsg wraps an error with a user-facing message.
// If the wrapped error is a *DomainError, its Class is inherited.
func WrapMsg(code Code, msg string, err error, opts ...ErrorOption) *DomainError {
	e := &DomainError{
		Code:    code,
		Message: msg,
		Err:     err,
		Class:   classFrom(err),
		Op:      opForCode(code),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// NotFound creates a "not found" validation error.
// Sets Class to ClassValidation regardless of the code's default Class.
func NotFound(code Code, msg string, opts ...ErrorOption) *DomainError {
	e := &DomainError{
		Code:    code,
		Message: msg,
		Class:   ClassValidation,
		Op:      opForCode(code),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// AlreadyExists creates an "already exists" conflict error.
// Sets Class to ClassConflict regardless of the code's default Class.
func AlreadyExists(code Code, msg string, opts ...ErrorOption) *DomainError {
	e := &DomainError{
		Code:    code,
		Message: msg,
		Class:   ClassConflict,
		Op:      opForCode(code),
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// AsType extracts and type-asserts an error from the chain using generics.
// Returns the typed error and true if found, or zero value and false otherwise.
// Usage:
//
//	if de, ok := errs.AsType[*errs.DomainError](err); ok {
//	    switch de.Code { ... }
//	}
func AsType[T error](err error) (T, bool) {
	var target T
	if errors.As(err, &target) {
		return target, true
	}
	return target, false
}

// ── Internal helpers ────────────────────────────────────────────────────

// AsDomainError extracts a *DomainError from an error chain.
// Returns nil if err is nil or doesn't unwrap to a DomainError.
func AsDomainError(err error) *DomainError {
	var de *DomainError
	if errors.As(err, &de) {
		return de
	}
	return nil
}

// classFrom returns the Class of a DomainError if err unwraps to one.
func classFrom(err error) Class {
	if de := AsDomainError(err); de != nil {
		return de.Class
	}
	return ClassUnknown
}

// ── Check helpers ───────────────────────────────────────────────────────

// IsNotFound checks if an error is a "not found" domain error.
func IsNotFound(err error) bool {
	if de := AsDomainError(err); de != nil {
		switch de.Code {
		case CodeVMNotFound, CodeNetworkNotFound, CodeImageNotFound,
			CodeKernelNotFound, CodeBinaryNotFound, CodeVolumeNotFound,
			CodeKeyNotFound, CodeCPSourceNotFound, CodeBundledAssetNotFound,
			CodeFirecrackerSocketNotFound, CodeLoopMountBinaryNotFound,
			CodeCPVMNotFound:
			return true
		}
	}
	return false
}

// IsRetryable returns true if the error has ClassRetryable.
func IsRetryable(err error) bool {
	if de := AsDomainError(err); de != nil {
		return de.Class == ClassRetryable
	}
	return false
}

// IsNeedsInteraction returns true if the error has ClassNeedsInteraction.
func IsNeedsInteraction(err error) bool {
	if de := AsDomainError(err); de != nil {
		return de.Class == ClassNeedsInteraction
	}
	return false
}

// ── Formatting ──────────────────────────────────────────────────────────

// FormatExceptionDebug formats an error for debug output, optionally
// including a full stack trace.
func FormatExceptionDebug(err error, includeStack bool) string {
	if includeStack {
		return fmt.Sprintf("%v\n%s", err, debug.Stack())
	}
	return err.Error()
}
