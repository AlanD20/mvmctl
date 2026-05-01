"""Settings service — validation and type coercion for user settings."""

from __future__ import annotations

from typing import Any

from mvmctl.constants import OVERRIDABLE_DEFAULTS
from mvmctl.core._shared import Database
from mvmctl.core.config._repository import SettingsRepository
from mvmctl.exceptions import ConfigError
from mvmctl.utils.common import CommonUtils

# Registry of overridable settings with their types
OVERRIDABLE_SETTINGS: dict[str, dict[str, type]] = {
    category: {key: type(value) for key, value in keys.items()}
    for category, keys in OVERRIDABLE_DEFAULTS.items()
}


class SettingsService:
    """Validation and type coercion for user settings."""

    def __init__(self, repo: SettingsRepository) -> None:
        self._repo = repo

    def get(self, category: str, key: str) -> Any | None:
        """Get a validated setting value."""
        value = self._repo.get(category, key)
        if value is None:
            return None
        expected_type = self._get_expected_type(category, key)
        if expected_type is not None:
            return CommonUtils.coerce(value, expected_type)
        return value

    def set(self, category: str, key: str, value: Any) -> None:
        """
        Set a validated setting value.

        Raises:
            ConfigError: If the key is not overridable or value has wrong type.

        """
        expected_type = self._get_expected_type(category, key)
        if expected_type is None:
            raise ConfigError(
                f"'{category}.{key}' is not an overridable setting. "
                f"Use 'mvm config list' to see valid keys."
            )
        coerced = CommonUtils.coerce(value, expected_type)
        self._repo.set(category, key, coerced)

    def delete(self, category: str, key: str) -> bool:
        """Delete a setting after validating the key exists."""
        if self._get_expected_type(category, key) is None:
            raise ConfigError(f"'{category}.{key}' is not a valid setting key")
        return self._repo.delete(category, key)

    @classmethod
    def resolve(cls, db: Database, category: str, key: str) -> Any:
        """
        Resolve a setting: check user_settings override, else fall back to hardcoded default.

        Args:
            db: Database instance for querying overrides.
            category: Setting category (e.g., 'defaults.vm').
            key: Setting key (e.g., 'vcpu_count').

        Returns:
            The overridden value or the hardcoded default.

        """
        from mvmctl.constants import get_default

        repo = SettingsRepository(db)
        override = repo.get(category, key)
        if override is not None:
            expected_type = cls._get_expected_type(category, key)
            if expected_type is not None:
                return CommonUtils.coerce(override, expected_type)
            return override
        return get_default(category, key)

    def list_by_category(self, category: str) -> dict[str, dict[str, Any]]:
        """List all keys in a category with type, default, and override info."""
        if category not in OVERRIDABLE_SETTINGS:
            raise ConfigError(
                f"'{category}' is not a valid setting category. "
                f"Use 'mvm config list' to see valid categories."
            )
        overrides = self._repo.list_by_category(category)
        result: dict[str, dict[str, Any]] = {}
        for key, expected_type in OVERRIDABLE_SETTINGS[category].items():
            override = overrides.get(category, {}).get(key)
            default = OVERRIDABLE_DEFAULTS[category][key]
            result[key] = {
                "type": expected_type.__name__,
                "override": override,
                "default": default,
            }
        return result

    def delete_by_category(self, category: str) -> int:
        """Delete all overrides in a category after validating it exists."""
        if category not in OVERRIDABLE_SETTINGS:
            raise ConfigError(
                f"'{category}' is not a valid setting category. "
                f"Use 'mvm config list' to see valid categories."
            )
        return self._repo.delete_by_category(category)

    def delete_all(self) -> int:
        """Delete ALL user overrides."""
        return self._repo.delete_all()

    def list_all(self) -> dict[str, dict[str, Any]]:
        """List all overridable settings with their current overrides."""
        overrides = self._repo.list_by_category()
        result: dict[str, dict[str, Any]] = {}
        for category, keys in OVERRIDABLE_SETTINGS.items():
            result[category] = {}
            for key in keys:
                result[category][key] = {
                    "type": keys[key].__name__,
                    "override": overrides.get(category, {}).get(key),
                }
        return result

    @classmethod
    def _get_expected_type(cls, category: str, key: str) -> type | None:
        """Get the expected type for a setting key, or None if not overridable."""
        cat_settings = OVERRIDABLE_SETTINGS.get(category)
        if cat_settings is None:
            return None
        return cat_settings.get(key)
