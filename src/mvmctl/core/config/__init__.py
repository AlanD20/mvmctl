"""Config domain — user settings persistence."""

from __future__ import annotations

from typing import TYPE_CHECKING

from mvmctl.utils._lazy_import import resolve_lazy

if TYPE_CHECKING:
    from mvmctl.core.config._repository import SettingsRepository
    from mvmctl.core.config._service import SettingsService

__all__ = ["SettingsRepository", "SettingsService"]

_LAZY_MAP = {
    "SettingsRepository": (
        "mvmctl.core.config._repository",
        "SettingsRepository",
    ),
    "SettingsService": ("mvmctl.core.config._service", "SettingsService"),
}


def __getattr__(name: str) -> object:
    return resolve_lazy(name, _LAZY_MAP, __name__)


def __dir__() -> list[str]:
    return __all__
