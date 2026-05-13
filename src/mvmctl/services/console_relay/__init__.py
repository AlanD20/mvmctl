"""Console relay service for VM serial console access."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from mvmctl.services.console_relay.client import ConsoleRelayClient
    from mvmctl.services.console_relay.manager import ConsoleRelayManager

__all__ = ["ConsoleRelayClient", "ConsoleRelayManager"]

_LAZY_MAP = {
    "ConsoleRelayClient": (
        "mvmctl.services.console_relay.client",
        "ConsoleRelayClient",
    ),
    "ConsoleRelayManager": (
        "mvmctl.services.console_relay.manager",
        "ConsoleRelayManager",
    ),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
