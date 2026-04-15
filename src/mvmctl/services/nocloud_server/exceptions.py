"""NoCloud server exceptions."""

from mvmctl.exceptions import MVMError


class NoCloudServerError(MVMError):
    """Base exception for NoCloud server errors."""


class NoCloudServerAlreadyRunningError(NoCloudServerError):
    """Raised when attempting to start a server that is already running."""
