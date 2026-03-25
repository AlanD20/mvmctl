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
