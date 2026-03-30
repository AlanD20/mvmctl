"""MVM exception hierarchy."""


class MVMError(Exception):
    """Base exception for all MVM errors."""


class VMNotFoundError(MVMError):
    """VM does not exist in state."""


class VMAlreadyExistsError(MVMError):
    """VM name already registered."""


class NetworkError(MVMError):
    """Network setup/teardown failure."""


class ImageError(MVMError):
    """Image download or conversion failure."""


class ChecksumMismatchError(ImageError):
    """Downloaded file checksum does not match expected."""


class KernelError(MVMError):
    """Kernel build or configuration failure."""


class FirecrackerError(MVMError):
    """Firecracker process or API failure."""


class ConfigError(MVMError):
    """Configuration loading or validation failure."""


class SocketNotFoundError(FirecrackerError):
    """Unix socket for VM API not found."""


class HostError(MVMError):
    """Host configuration or prerequisite failure."""


class PrivilegeError(HostError):
    """Insufficient privileges for an operation."""


class ProcessError(MVMError):
    """Subprocess execution failure."""


class AssetNotFoundError(MVMError):
    """Requested asset (binary, kernel, image) not found locally or remotely."""


class BinaryError(MVMError):
    """Firecracker/jailer binary management failure."""


class MVMKeyError(MVMError):
    """SSH key management failure."""


class CloudInitError(MVMError):
    """Cloud-init ISO creation failure.

    Common messages:
    - cloud-localds not found. Install cloud-image-utils or cloud-utils package
    - Failed to create cloud-init ISO: {details}
    """


class VMCreateError(MVMError):
    """VM creation failed - resources may have been partially created.

    This error is raised when VM creation fails mid-way. The exception
    handler performs best-effort cleanup of any resources that were
    created before the failure (VM directory, TAP device, network IP,
    firewall rules, nocloud server, console relay).
    """


class RootfsInjectionError(MVMError):
    """Raised when cloud-init injection into rootfs fails.

    This error indicates a failure during direct filesystem injection
    using libguestfs. Common causes include missing libguestfs Python
    bindings, inability to mount the rootfs, or filesystem corruption.
    """


class GuestfsNotAvailableError(RootfsInjectionError):
    """Raised when libguestfs Python bindings are not available."""

    pass


class GuestfsLaunchError(RootfsInjectionError):
    """Raised when guestfs appliance fails to launch."""

    pass


class GuestfsMountError(RootfsInjectionError):
    """Raised when unable to mount rootfs in guestfs."""

    pass


class GuestfsWriteError(RootfsInjectionError):
    """Raised when writing files to guestfs fails."""

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


def format_exception_debug(exc: Exception, debug: bool = False) -> str:
    """Format an exception for display, with optional debug details.

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
