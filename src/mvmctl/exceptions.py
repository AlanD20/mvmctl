"""MVM exception hierarchy."""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

if TYPE_CHECKING:
    pass


class MVMError(Exception):
    """Base exception for all MVM errors."""


class VMNotFoundError(MVMError):
    """VM does not exist in state."""


class BinaryNotFoundError(MVMError):
    """Binary does not exist in registry."""


class KernelNotFoundError(MVMError):
    """Kernel does not exist in registry."""


class NetworkNotFoundError(MVMError):
    """Network does not exist in registry."""


class KeyNotFoundError(MVMError):
    """SSH key does not exist in registry."""


class ImageNotFoundError(MVMError):
    """Image does not exist in registry."""


class ImageAcquireError(MVMError):
    """Image fetch/import failure."""


class NetworkError(MVMError):
    """Network setup/teardown failure."""


class IPTablesTrackerError(MVMError):
    """IPTables action failure."""


class ImageError(MVMError):
    """Image download or conversion failure."""


class ImageCompressionError(ImageError):
    """
    Image compression failure.

    Common messages:
    - Cannot compress: source file does not exist: {path}
    - Cannot compress: source file is empty: {path}
    - Compression failed: output not created: {path}
    - Compression failed: output is empty (source was {size} bytes)
    """


class ImageDecompressionError(ImageError):
    """
    Image decompression failure.

    Common messages:
    - Compressed file not found: {path}
    - Decompression failed: {details}
    """


class ImageCorruptError(ImageError):
    """
    Image file appears corrupted.

    Common messages:
    - Source file appears to be all zeros: {path}. File may be corrupted.
    """


class ImageEmptyError(ImageError):
    """
    Image file is empty.

    Common messages:
    - Cannot compress: source file is empty: {path}
    - Downloaded file is empty
    """


class ImageValidationError(ImageError):
    """
    Downloaded image file failed format validation.

    Common messages:
    - Invalid {format} file: {reason}
    - Unknown format for validation: {format}
    """


class ChecksumMismatchError(ImageError):
    """Downloaded file checksum does not match expected."""


class KernelError(MVMError):
    """Kernel build or configuration failure."""


class FirecrackerClientError(MVMError):
    """Firecracker process or API failure."""


class FirecrackerSpawnError(MVMError):
    """Firecracker spawn failure."""


class FirecrackerConfigError(MVMError):
    """Firecracker config generation failure."""


class ConfigError(MVMError):
    """Configuration loading or validation failure."""


class DatabaseError(MVMError):
    """
    Database operation failure.

    Common messages:
    - Database not migrated. Run 'mvm init' first.
    - no such table: {table_name}
    """

    def __init__(self, message: str = "Database operation failed") -> None:
        self.message = message
        super().__init__(message)

    def __str__(self) -> str:
        return self.message


class MigrationError(DatabaseError):
    """
    Database migration failure.

    Common messages:
    - Migration {version} failed: {details}
    - Missing migration versions: {versions}
    - Invalid migration filename: {filename}
    """

    def __init__(self, message: str = "Migration failed") -> None:
        self.message = message
        super().__init__(message)

    def __str__(self) -> str:
        return self.message


class SocketNotFoundError(FirecrackerClientError):
    """Unix socket for VM API not found."""


class HostError(MVMError):
    """Host configuration or prerequisite failure."""


class ConsoleError(MVMError):
    """
    Console or PTY operation failure.

    Common messages:
    - PTY allocation failed: {details}
    - Console relay failed to start: {details}
    - Failed to attach to console: {details}
    """


class PrivilegeError(HostError):
    """Insufficient privileges for an operation."""

    def __init__(
        self,
        message: str = "Insufficient privileges",
        details: dict[str, Any] | None = None,
    ) -> None:
        self.message = message
        self.details = details
        super().__init__(message)

    def __str__(self) -> str:
        return self.message


class ProcessError(MVMError):
    """Subprocess execution failure."""


class AssetNotFoundError(MVMError):
    """Requested asset (binary, kernel, image) not found locally or remotely."""


class BundledAssetError(MVMError):
    """Bundled package asset (templates, configs) access failure."""


class BundledAssetNotFoundError(BundledAssetError):
    """Requested bundled asset file not found in package."""


class BinaryError(MVMError):
    """Firecracker/jailer binary management failure."""


class BinaryAlreadyExistsError(BinaryError):
    """Raised when a binary version already exists and re-download was not requested."""


class SSHError(MVMError):
    """SSH connection or configuration failure."""


class MVMKeyError(MVMError):
    """SSH key management failure."""


class KeyExportError(MVMKeyError):
    """SSH key export failure."""


class KeyDependencyError(MVMKeyError):
    """SSH key dependency (e.g. ssh-keygen) missing."""


class KeyFileError(MVMKeyError):
    """SSH key file read/write failure."""


class CloudInitError(MVMError):
    """
    Cloud-init ISO creation failure.

    Common messages:
    - cloud-localds not found. Install cloud-image-utils or cloud-utils package
    - Failed to create cloud-init ISO: {details}
    """


class CloudInitProvisionError(CloudInitError):
    """
    Cloud-init provisioning failure.

    Common messages:
    - Invalid custom user data
    """


class CloudInitModeError(CloudInitError):
    """
    Cloud-init mode failure.

    Common messages:
    - Failed to resolve cloud-init mode
    - Custom ISO file not found
    """


class CloudInitOffModeError(CloudInitError):
    """OFF mode guestfs provisioning failure."""


class CloudInitIsoModeError(CloudInitError):
    """ISO creation failure."""


class CloudInitNetModeError(CloudInitError):
    """Nocloud-net server or iptables rule failure."""


class CloudInitInjectModeError(CloudInitError):
    """Rootfs cloud-init injection failure."""


class VMCreateError(MVMError):
    """
    VM creation failed - resources may have been partially created.

    This error is raised when VM creation fails mid-way. The exception
    handler performs best-effort cleanup of any resources that were
    created before the failure (VM directory, TAP device, network IP,
    firewall rules, nocloud server, console relay).
    """


class VMRequestError(MVMError):
    """Error during VM request resolution or validation."""

    pass


class VMBuilderError(MVMError):
    """
    VM builder failed - resources may have been partially created.

    This error is raised when VM creation fails mid-way. The exception
    handler performs best-effort cleanup of any resources that were
    created before the failure (VM directory, TAP device, network IP,
    firewall rules, nocloud server, console relay).
    """


class GuestfsError(MVMError):
    """Base exception for libguestfs-related errors."""

    pass


class GuestfsNotAvailableError(GuestfsError):
    """Raised when libguestfs Python bindings are not available."""

    pass


class GuestfsLaunchError(GuestfsError):
    """Raised when guestfs appliance fails to launch."""

    pass


class GuestfsMountError(GuestfsError):
    """Raised when unable to mount rootfs in guestfs."""

    pass


class GuestfsWriteError(GuestfsError):
    """Raised when writing files to guestfs fails."""

    pass


class GuestfsApplianceError(GuestfsError):
    """Raised when the libguestfs fixed appliance build fails."""

    pass


class RootPartitionDetectionError(MVMError):
    """Root partition could not be detected."""

    def __init__(
        self,
        message: str = "No root partition candidate found",
        partitions: list[dict[str, object]] | None = None,
    ) -> None:
        self.message = message
        self.partitions = partitions or []
        super().__init__(message)

    def __str__(self) -> str:
        return self.message


class TieDetectedError(MVMError):
    """Multiple partitions scored equally; cannot auto-select."""

    def __init__(
        self,
        tied_partitions: list[str],
        message: str = "Tie detected between partitions",
        partitions: list[dict[str, object]] | None = None,
    ) -> None:
        self.tied_partitions = tied_partitions
        self.message = message
        self.partitions = partitions or []
        super().__init__(message)

    def __str__(self) -> str:
        return f"{self.message}: {', '.join(self.tied_partitions)}"


class DownloadError(MVMError):
    """Raised when a download operation fails."""

    pass


class HttpDownloadError(MVMError):
    """Raised when an HTTP download operation fails."""

    pass


def format_exception_debug(exc: Exception, debug: bool = False) -> str:
    """
    Format an exception for display, with optional debug details.

    Args:
        exc: The exception to format.
        debug: If True, include full traceback and exception class name.

    Returns:
        Formatted exception string suitable for user display.

    """
    if debug:
        import traceback

        return f"{exc.__class__.__name__}: {exc}\n{traceback.format_exc()}"
    return str(exc)
