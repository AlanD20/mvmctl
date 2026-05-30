package errs

import (
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
)

type Class int

const (
	ClassUnknown Class = iota
	ClassValidation
	ClassConflict
	ClassRetryable
	ClassInternal
	ClassNeedsInteraction
)

type DomainError struct {
	Code    Code
	Message string
	Op      string
	Entity  string
	Class   Class
	Err     error
	Details map[string]any
}

// Error returns just the user-facing message. Matches Python's str(exception),
// which returns the message passed to MVMError.__init__(). If no message was
// given, Python returns "" — not a fallback chain.
// For the detailed format (code, op, entity, cause), use String().
func (e *DomainError) Error() string {
	return e.Message
}

// String returns the detailed error format: "code (op): entity: message: cause".
// Example: "vm.not_found (domain): vm-1: The VM was not found"
func (e *DomainError) String() string {
	var b strings.Builder
	b.WriteString(string(e.Code))
	if e.Op != "" {
		b.WriteString(" (")
		b.WriteString(e.Op)
		b.WriteString(")")
	}
	if e.Entity != "" {
		b.WriteString(": ")
		b.WriteString(e.Entity)
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *DomainError) Unwrap() error { return e.Err }

// IsMVMError marks DomainError as an MVMError subclass for the enricher's
// soft-fail interface check. Any type that implements IsMVMError() bool is
// treated as an MVMError subclass and soft-failed rather than propagated
// during enrichment. Matches Python's "except MVMError" catch blocks.
func (e *DomainError) IsMVMError() bool { return true }

type Op string

// NotFound creates a "not found" domain error. The msg should be a full
// user-facing message (e.g., "VM not found: my-vm"), matching Python's
// pattern of MVMError("VM not found: my-vm").
func NotFound(code Code, msg string) *DomainError {
	return &DomainError{Code: code, Op: "domain", Message: msg, Class: ClassValidation}
}

// AlreadyExists creates an "already exists" domain error. The msg should
// include the identifier for the user-facing message.
func AlreadyExists(code Code, msg string) *DomainError {
	return &DomainError{Code: code, Op: "domain", Message: msg, Class: ClassValidation}
}

func ValidationFailed(code Code, msg string) *DomainError {
	return &DomainError{Code: code, Message: msg, Class: ClassValidation}
}

func Wrap(code Code, err error) *DomainError {
	return &DomainError{Code: code, Err: err, Class: classFrom(err)}
}

// WrapMsg is like Wrap but also sets a Message. This fills a gap when you need
// both a wrapped error and a custom message, matching Python patterns like
// MVMError("message") raised from a caught exception.
func WrapMsg(code Code, message string, err error) *DomainError {
	return &DomainError{Code: code, Message: message, Err: err, Class: classFrom(err)}
}

func classFrom(err error) Class {
	var de *DomainError
	if errors.As(err, &de) {
		return de.Class
	}
	return ClassUnknown
}

// IsNotFound checks if an error is a "not found" domain error, including all
// not-found-related error codes from every domain. Matches Python's pattern
// of catching specific not-found exceptions (VMNotFoundError, etc.) or using
// isinstance checks.
func IsNotFound(err error) bool {
	var de *DomainError
	if errors.As(err, &de) {
		switch de.Code {
		case CodeVMNotFound, CodeNetworkNotFound, CodeImageNotFound,
			CodeKernelNotFound, CodeBinaryNotFound, CodeVolumeNotFound,
			CodeKeyNotFound, CodeCPSourceNotFound, CodeBundledAssetNotFound,
			CodeFirecrackerSocketNotFound, CodeLoopMountBinaryNotFound:
			return true
		}
	}
	return false
}

func IsRetryable(err error) bool {
	var de *DomainError
	if errors.As(err, &de) {
		return de.Class == ClassRetryable
	}
	return false
}

func IsNeedsInteraction(err error) bool {
	var de *DomainError
	if errors.As(err, &de) {
		return de.Class == ClassNeedsInteraction
	}
	return false
}

// ── Factory helpers for Python exception classes ─────────────────────────

// VMRequestError creates a VM request error matching Python's VMRequestError.
func VMRequestError(msg string) *DomainError {
	return &DomainError{Code: CodeVMResolveFailed, Op: "vm", Message: msg, Class: ClassValidation}
}

// VMBuilderError creates a VM builder error matching Python's VMBuilderError.
// Python semantics: resources partially created, cleanup needed — NOT retryable.
func VMBuilderError(msg string) *DomainError {
	return &DomainError{Code: CodeVMCreateFailed, Op: "vm", Message: msg, Class: ClassInternal}
}

// VMCreateError creates a VM create error matching Python's VMCreateError.
func VMCreateError(msg string) *DomainError {
	return &DomainError{Code: CodeVMCreateFailed, Op: "vm", Message: msg, Class: ClassInternal}
}

// VMStateError creates a VM state error matching Python's VMStateError.
func VMStateError(msg string) *DomainError {
	return &DomainError{Code: CodeVMStateInvalid, Op: "vm", Message: msg, Class: ClassValidation}
}

// ImageValidationError creates an image validation error matching Python's ImageValidationError.
func ImageValidationError(msg string) *DomainError {
	return &DomainError{Code: CodeImageFormatInvalid, Op: "image", Message: msg, Class: ClassValidation}
}

// IPTablesTrackerError creates an IP tables tracker error matching Python's IPTablesTrackerError.
func IPTablesTrackerError(msg string) *DomainError {
	return &DomainError{Code: CodeNetworkFirewallFailed, Op: "network", Message: msg, Class: ClassInternal}
}

// KeyFileError creates a key file error matching Python's KeyFileError.
// Uses CodeInternal as the generic fallback — Python's KeyFileError does not
// set a specific error code (MVMError.code defaults to None).
func KeyFileError(msg string) *DomainError {
	return &DomainError{Code: CodeInternal, Op: "key", Message: msg, Class: ClassInternal}
}

// KeyExportError creates a key export error matching Python's KeyExportError.
func KeyExportError(msg string) *DomainError {
	return &DomainError{Code: CodeKeyExportFailed, Op: "key", Message: msg, Class: ClassValidation}
}

// KeyDependencyError creates a key dependency error matching Python's KeyDependencyError.
func KeyDependencyError(msg string) *DomainError {
	return &DomainError{Code: CodeKeyDependencyMissing, Op: "key", Message: msg, Class: ClassValidation}
}

// MVMKeyError creates a general key management error matching Python's MVMKeyError.
// Uses CodeInternal as the generic fallback — Python's MVMKeyError does not
// set a specific error code.
func MVMKeyError(msg string) *DomainError {
	return &DomainError{Code: CodeInternal, Op: "key", Message: msg, Class: ClassInternal}
}

// BinaryAlreadyExistsError creates a binary already exists error matching Python's BinaryAlreadyExistsError.
func BinaryAlreadyExistsError(msg string) *DomainError {
	return &DomainError{Code: CodeBinaryAlreadyExists, Op: "binary", Message: msg, Class: ClassConflict}
}

// VersionGateError creates a version gate error matching Python's VersionGateError.
func VersionGateError(msg string) *DomainError {
	return &DomainError{Code: CodeBinaryVersionGate, Op: "version", Message: msg, Class: ClassValidation}
}

// VersionError creates a version resolution error matching Python's VersionError.
// Uses CodeInternal as the generic fallback — Python's VersionError does not
// set a specific error code (MVMError.code defaults to None).
func VersionError(msg string) *DomainError {
	return &DomainError{Code: CodeInternal, Op: "version", Message: msg, Class: ClassInternal}
}

// GuestfsNotAvailableError creates a guestfs not available error matching Python's GuestfsNotAvailableError.
func GuestfsNotAvailableError(msg string) *DomainError {
	return &DomainError{Code: CodeGuestfsNotAvailable, Op: "guestfs", Message: msg, Class: ClassInternal}
}

// GuestfsWriteError creates a guestfs write error matching Python's GuestfsWriteError.
func GuestfsWriteError(msg string) *DomainError {
	return &DomainError{Code: CodeGuestfsWriteError, Op: "guestfs", Message: msg, Class: ClassInternal}
}

// RootPartitionDetectionError creates a root partition detection error matching
// Python's RootPartitionDetectionError. Carries structured partition data in Details.
// Uses CodeInternal as the generic fallback — Python's RootPartitionDetectionError
// does not set a specific error code (MVMError.code defaults to None).
//
// Python: RootPartitionDetectionError.__init__(self, message="No root partition candidate found", partitions=None)
// The default message is "No root partition candidate found".
func RootPartitionDetectionError(partitions []map[string]any, message string) *DomainError {
	if message == "" {
		message = "No root partition candidate found"
	}
	details := map[string]any{}
	if partitions != nil {
		details["partitions"] = partitions
	}
	return &DomainError{
		Code:    CodeInternal,
		Class:   ClassInternal,
		Message: message,
		Details: details,
	}
}

// TieDetectedError creates a tie detection error matching Python's TieDetectedError.
// Carries tied partition identifiers and optional partitions list in Details.
// Uses CodeInternal as the generic fallback.
//
// Python: TieDetectedError.__init__(self, tied_partitions, message="Tie detected between partitions", partitions=None)
// Python __str__: f"{self.message}: {', '.join(self.tied_partitions)}"
func TieDetectedError(tiedPartitions []string, message string, partitions []map[string]any) *DomainError {
	if message == "" {
		message = "Tie detected between partitions"
	}
	details := map[string]any{
		"tied_partitions": tiedPartitions,
	}
	if partitions != nil {
		details["partitions"] = partitions
	}
	return &DomainError{
		Code:    CodeInternal,
		Class:   ClassInternal,
		Message: fmt.Sprintf("%s: %s", message, strings.Join(tiedPartitions, ", ")),
		Details: details,
	}
}

// LoopMountError creates a loopmount error matching Python's LoopMountError.
func LoopMountError(msg string) *DomainError {
	return &DomainError{Code: CodeLoopMountError, Op: "loopmount", Message: msg, Class: ClassInternal}
}

// LoopMountBinaryNotFoundError creates a loopmount binary not found error matching Python's LoopMountBinaryNotFoundError.
func LoopMountBinaryNotFoundError(msg string) *DomainError {
	return &DomainError{Code: CodeLoopMountBinaryNotFound, Op: "loopmount", Message: msg, Class: ClassValidation}
}

// LoopMountTimeoutError creates a loopmount timeout error matching Python's LoopMountTimeoutError.
func LoopMountTimeoutError(msg string) *DomainError {
	return &DomainError{Code: CodeLoopMountTimeout, Op: "loopmount", Message: msg, Class: ClassRetryable}
}

// ProcessError creates a process error matching Python's ProcessError(MVMError).
// Used for subprocess command failures (not found, timeout, non-zero exit).
func ProcessError(msg string) *DomainError {
	return &DomainError{Code: CodeProcessError, Op: "process", Message: msg, Class: ClassInternal}
}

// ProcessErrorWrapped creates a process error wrapping a cause, matching
// Python's raise ProcessError(...) from e pattern.
func ProcessErrorWrapped(msg string, cause error) *DomainError {
	return &DomainError{Code: CodeProcessError, Op: "process", Message: msg, Err: cause, Class: ClassInternal}
}

// BundledAssetNotFoundError creates a bundled asset not found error matching
// Python's BundledAssetNotFoundError(BundledAssetError).
func BundledAssetNotFoundError(entity string) *DomainError {
	return &DomainError{Code: CodeBundledAssetNotFound, Op: "asset", Message: entity, Class: ClassValidation}
}

// DownloadError creates a download error matching Python's HttpDownloadError.
func DownloadError(msg string) *DomainError {
	return &DomainError{Code: CodeDownloadFailed, Op: "download", Message: msg, Class: ClassInternal}
}

// HTTPError creates an HTTP error matching Python's HttpDownloadError.
func HTTPError(msg string) *DomainError {
	return &DomainError{Code: CodeHTTPError, Op: "http", Message: msg, Class: ClassInternal}
}

// DatabaseError creates a database error matching Python's DatabaseError.
// Python: DatabaseError.__init__(self, message="Database operation failed")
// The default message is "Database operation failed".
func DatabaseError(msg string) *DomainError {
	if msg == "" {
		msg = "Database operation failed"
	}
	return &DomainError{Code: CodeDatabaseError, Op: "db", Message: msg, Class: ClassInternal}
}

// MigrationError creates a migration error matching Python's MigrationError.
// Python: MigrationError.__init__(self, message="Migration failed")
// The default message is "Migration failed".
func MigrationError(msg string) *DomainError {
	if msg == "" {
		msg = "Migration failed"
	}
	return &DomainError{Code: CodeMigrationFailed, Op: "db", Message: msg, Class: ClassInternal}
}

// PrivilegeError creates a privilege error matching Python's PrivilegeError.
// Python: PrivilegeError.__init__(self, message="Insufficient privileges", details=None)
// The default message is "Insufficient privileges".
func PrivilegeError(msg string, details map[string]any) *DomainError {
	if msg == "" {
		msg = "Insufficient privileges"
	}
	return &DomainError{Code: CodePrivilegeRequired, Op: "host", Message: msg, Class: ClassNeedsInteraction, Details: details}
}

// FormatExceptionDebug formats an error for debug output, including a full
// stack trace when includeStack is true. Mirrors Python's format_exception_debug().
// Python: f"{exc.__class__.__name__}: {exc}\n{traceback.format_exc()}"
func FormatExceptionDebug(err error, includeStack bool) string {
	if includeStack {
		return fmt.Sprintf("%s: %v\n%s", typeName(err), err, debug.Stack())
	}
	return err.Error()
}

// NetworkError creates a general network error.
// Uses CodeInternal as the generic fallback — Python's NetworkError does not
// set a specific error code (MVMError.code defaults to None).
func NetworkError(msg string) *DomainError {
	return &DomainError{Code: CodeInternal, Op: "network", Message: msg, Class: ClassInternal}
}

// ── Firecracker error factories ──────────────────────────────────────────

// FirecrackerError wraps Firecracker API errors.
func FirecrackerError(msg string) *DomainError {
	return &DomainError{Code: CodeFirecrackerError, Op: "vm", Message: msg, Class: ClassInternal}
}

// FirecrackerClientError wraps Firecracker client connection errors.
func FirecrackerClientError(msg string) *DomainError {
	return &DomainError{Code: CodeFirecrackerClientError, Op: "vm", Message: msg, Class: ClassInternal}
}

// FirecrackerSpawnError wraps Firecracker process spawn failures.
func FirecrackerSpawnError(msg string) *DomainError {
	return &DomainError{Code: CodeFirecrackerSpawnError, Op: "vm", Message: msg, Class: ClassInternal}
}

// FirecrackerConfigError wraps Firecracker configuration errors.
func FirecrackerConfigError(msg string) *DomainError {
	return &DomainError{Code: CodeFirecrackerConfigError, Op: "vm", Message: msg, Class: ClassValidation}
}

// SocketNotFoundError indicates the Firecracker API socket was not found.
func SocketNotFoundError(path string) *DomainError {
	return &DomainError{Code: CodeFirecrackerSocketNotFound, Op: "vm", Message: path, Class: ClassValidation}
}

// ── Cloud-init error factories ───────────────────────────────────────────

// CloudInitModeError indicates cloud-init mode resolution failure.
// Python: CloudInitModeError(CloudInitError)
func CloudInitModeError(msg string) *DomainError {
	return &DomainError{Code: CodeCloudInitModeError, Op: "cloudinit", Message: msg, Class: ClassValidation}
}

// CloudInitOffModeError indicates OFF mode guestfs failure.
// Python: CloudInitOffModeError(CloudInitError)
func CloudInitOffModeError(msg string) *DomainError {
	return &DomainError{Code: CodeCloudInitOffModeError, Op: "cloudinit", Message: msg, Class: ClassInternal}
}

// ── Config / Host error factories ────────────────────────────────────────

// ConfigError indicates a configuration error.
func ConfigError(msg string) *DomainError {
	return &DomainError{Code: CodeConfigError, Op: "config", Message: msg, Class: ClassValidation}
}

// HostError indicates a host configuration error.
func HostError(msg string) *DomainError {
	return &DomainError{Code: CodeHostInitFailed, Op: "host", Message: msg, Class: ClassInternal}
}

// HostSudoersError indicates sudoers configuration failed.
func HostSudoersError(msg string) *DomainError {
	return &DomainError{Code: CodePrivilegeSudoers, Op: "host", Message: msg, Class: ClassValidation}
}

// ── SSH / CP error factories ─────────────────────────────────────────────

// SSHError indicates an SSH error.
func SSHError(msg string) *DomainError {
	return &DomainError{Code: CodeSSHError, Op: "ssh", Message: msg, Class: ClassInternal}
}

// CPError indicates a general copy error.
func CPError(msg string) *DomainError {
	return &DomainError{Code: CodeCPError, Op: "cp", Message: msg, Class: ClassInternal}
}

// CPSourceNotFoundError indicates the copy source was not found.
func CPSourceNotFoundError(msg string) *DomainError {
	return &DomainError{Code: CodeCPSourceNotFound, Op: "cp", Message: msg, Class: ClassValidation}
}

// CPDestinationExistsError indicates the copy destination already exists.
func CPDestinationExistsError(msg string) *DomainError {
	return &DomainError{Code: CodeCPDestinationExists, Op: "cp", Message: msg, Class: ClassValidation}
}

// CPDestinationNotDirError indicates the copy destination is not a directory.
func CPDestinationNotDirError(msg string) *DomainError {
	return &DomainError{Code: CodeCPDestinationNotDir, Op: "cp", Message: msg, Class: ClassValidation}
}

// ── Image / Binary / Kernel / Network / Volume error factories ───────────

// ImageAcquireError creates an image acquire error matching Python's
// ImageAcquireError (fetch/import failure). Uses CodeImageError as the generic
// image.error code — Python's ImageAcquireError does not set a specific code
// (MVMError.code defaults to None).
func ImageAcquireError(msg string) *DomainError {
	return &DomainError{Code: CodeImageError, Op: "image", Message: msg, Class: ClassInternal}
}

// ImageCorruptError indicates the image is corrupted.
func ImageCorruptError(msg string) *DomainError {
	return &DomainError{Code: CodeImageCorrupt, Op: "image", Message: msg, Class: ClassValidation}
}

// ImageEmptyError indicates the image is empty.
func ImageEmptyError(msg string) *DomainError {
	return &DomainError{Code: CodeImageEmpty, Op: "image", Message: msg, Class: ClassValidation}
}

// ChecksumMismatchError indicates a checksum verification failure.
func ChecksumMismatchError(msg string) *DomainError {
	return &DomainError{Code: CodeImageChecksumMismatch, Op: "image", Message: msg, Class: ClassValidation}
}

// ImageCompressionError indicates image compression failed.
// Python: ImageCompressionError(ImageError)
func ImageCompressionError(msg string) *DomainError {
	return &DomainError{Code: CodeImageCompressionError, Op: "image", Message: msg, Class: ClassInternal}
}

// ImageDecompressionError indicates image decompression failed.
// Python: ImageDecompressionError(ImageError)
func ImageDecompressionError(msg string) *DomainError {
	return &DomainError{Code: CodeImageDecompressionError, Op: "image", Message: msg, Class: ClassInternal}
}

// BinaryError indicates a general binary management failure.
// Python: BinaryError(MVMError) — used extensively in binary service.
func BinaryError(msg string) *DomainError {
	return &DomainError{Code: CodeBinaryError, Op: "binary", Message: msg, Class: ClassInternal}
}

// BinaryNotFoundError indicates the binary was not found.
func BinaryNotFoundError(msg string) *DomainError {
	return &DomainError{Code: CodeBinaryNotFound, Op: "binary", Message: msg, Class: ClassValidation}
}

// LogsError indicates a log file read or tail operation failure.
// Python: LogsError(MVMError)
func LogsError(msg string) *DomainError {
	return &DomainError{Code: CodeLogsError, Op: "logs", Message: msg, Class: ClassInternal}
}

// ── GuestFS error factories ──────────────────────────────────────────────

// GuestfsError indicates a general guestfs error.
func GuestfsError(msg string) *DomainError {
	return &DomainError{Code: CodeGuestfsError, Op: "guestfs", Message: msg, Class: ClassInternal}
}

// typeName extracts the underlying type name from an error value, matching
// Python's exc.__class__.__name__. Returns just the name without any package
// prefix or pointer indicator.
func typeName(err error) string {
	s := fmt.Sprintf("%T", err)
	// Strip pointer prefix
	s = strings.TrimLeft(s, "*")
	// Strip package prefix (everything before the last dot)
	if idx := strings.LastIndex(s, "."); idx >= 0 {
		s = s[idx+1:]
	}
	return s
}
