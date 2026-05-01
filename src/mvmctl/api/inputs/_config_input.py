"""Config input resolution — Input → Request → ResolvedConfigInput."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

from mvmctl.core._shared import Database
from mvmctl.core.config._repository import SettingsRepository
from mvmctl.core.config._service import SettingsService
from mvmctl.exceptions import ConfigError


@dataclass
class ConfigInput:
    """Raw config parameters from CLI."""

    action: str  # 'get', 'set', 'list', 'reset'
    category: str | None = None  # e.g. 'defaults.vm'
    key: str | None = None  # e.g. 'vcpu_count'
    value: Any | None = None  # for 'set'
    all_overrides: bool = False  # for 'reset --all'


@dataclass(frozen=True)
class ResolvedConfigInput:
    """Fully resolved config operation parameters."""

    action: str
    category: str | None
    key: str | None
    value: Any | None
    all_overrides: bool
    service: SettingsService


class ConfigRequest:
    """Resolve ConfigInput against the database."""

    def __init__(self, inputs: ConfigInput, db: Database | None = None) -> None:
        self._inputs = inputs
        self._db = db if db is not None else Database()
        self._service = SettingsService(SettingsRepository(self._db))

    def resolve(self) -> ResolvedConfigInput:
        """Resolve and validate config input."""
        category = self._inputs.category
        key = self._inputs.key

        if self._inputs.action == "get":
            if not category:
                raise ConfigError("Category is required for get operation")
            # key is optional for category-level get

        elif self._inputs.action == "set":
            if not category or not key:
                raise ConfigError(
                    "Category and key are required for set operation"
                )
            if self._inputs.value is None:
                raise ConfigError("Value is required for set operation")

        elif self._inputs.action == "reset":
            if self._inputs.all_overrides:
                # category and key are both optional for --all
                pass
            elif not category:
                raise ConfigError(
                    "Category is required for reset operation (or use --all)"
                )
            # key is optional for category-level reset

        return ResolvedConfigInput(
            action=self._inputs.action,
            category=category,
            key=key,
            value=self._inputs.value,
            all_overrides=self._inputs.all_overrides,
            service=self._service,
        )
