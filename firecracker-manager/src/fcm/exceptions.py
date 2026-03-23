"""FCM exception hierarchy."""


class FCMError(Exception):
    """Base exception for all FCM errors."""


class VMNotFoundError(FCMError):
    """VM does not exist in state."""


class VMAlreadyExistsError(FCMError):
    """VM name already registered."""


class NetworkError(FCMError):
    """Network setup/teardown failure."""


class ImageError(FCMError):
    """Image download or conversion failure."""


class ChecksumMismatchError(ImageError):
    """Downloaded file checksum does not match expected."""


class KernelError(FCMError):
    """Kernel build or configuration failure."""


class FirecrackerError(FCMError):
    """Firecracker process or API failure."""


class ConfigError(FCMError):
    """Configuration loading or validation failure."""


class SocketNotFoundError(FirecrackerError):
    """Unix socket for VM API not found."""


class HostError(FCMError):
    """Host configuration or prerequisite failure."""


class ProcessError(FCMError):
    """Subprocess execution failure."""


class AssetNotFoundError(FCMError):
    """Requested asset (binary, kernel, image) not found locally or remotely."""


class BinaryError(FCMError):
    """Firecracker/jailer binary management failure."""


class FCMKeyError(FCMError):
    """SSH key management failure."""
