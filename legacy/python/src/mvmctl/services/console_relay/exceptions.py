"""Console relay service exceptions."""

from __future__ import annotations

from mvmctl.exceptions import MVMError


class ConsoleRelayError(MVMError):
    """Base exception for console relay failures."""


class ConsoleRelayAlreadyRunningError(ConsoleRelayError):
    """Raised when attempting to start a relay that is already running."""


class ConsoleRelayProcessError(ConsoleRelayError):
    """Raised when the relay subprocess fails to start or terminates unexpectedly."""


class ConsoleRelayNotRunningError(ConsoleRelayError):
    """Raised when attempting to stop or interact with a relay that is not running."""


class ConsoleRelayPermissionError(ConsoleRelayError):
    """Raised when the relay lacks necessary permissions (e.g., for PTY or socket)."""


class ConsoleRelayConnectionError(ConsoleRelayError):
    """Raised when the client fails to connect to the relay socket."""


__all__ = [
    "ConsoleRelayError",
    "ConsoleRelayAlreadyRunningError",
    "ConsoleRelayProcessError",
    "ConsoleRelayNotRunningError",
    "ConsoleRelayPermissionError",
    "ConsoleRelayConnectionError",
]
