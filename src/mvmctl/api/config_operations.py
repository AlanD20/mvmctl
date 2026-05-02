"""Config operations — user settings management."""

from __future__ import annotations

from typing import Any

from mvmctl.api.inputs._config_input import ConfigInput, ConfigRequest
from mvmctl.core._shared import Database
from mvmctl.models.result import OperationResult
from mvmctl.utils.auditlog import AuditLog


class ConfigOperation:
    """User config settings orchestration."""

    @staticmethod
    def get(
        category: str, key: str | None = None
    ) -> Any | dict[str, Any] | None:
        """
        Get a config value.

        Args:
            category: Setting category (e.g. 'defaults.vm').
            key: Setting key (e.g. 'vcpu_count'). If None, returns all keys
                in the category.

        Returns:
            The current override value, a dict of category keys when key is
            None, or None if not set.

        """
        inputs = ConfigInput(action="get", category=category, key=key)
        resolved = ConfigRequest(inputs=inputs, db=Database()).resolve()
        assert resolved.category is not None
        if resolved.key is None:
            return resolved.service.list_by_category(resolved.category)
        value = resolved.service.get(resolved.category, resolved.key)
        return value

    @staticmethod
    def set(category: str, key: str, value: Any) -> OperationResult[None]:
        """Set a config value.

        Returns:
            OperationResult with code "config.set" on success.
        """
        inputs = ConfigInput(
            action="set", category=category, key=key, value=value
        )
        resolved = ConfigRequest(inputs=inputs, db=Database()).resolve()
        assert resolved.category is not None
        assert resolved.key is not None
        resolved.service.set(resolved.category, resolved.key, resolved.value)
        AuditLog.log(
            "config.set",
            context=f"{resolved.category}.{resolved.key}={resolved.value}",
        )
        return OperationResult(
            status="success",
            code="config.set",
            message=f"Set {resolved.category}.{resolved.key} = {resolved.value}",
        )

    @staticmethod
    def reset(
        category: str | None = None,
        key: str | None = None,
        all_overrides: bool = False,
    ) -> OperationResult[int]:
        """
        Reset a config value to its default (remove override).

        Args:
            category: Setting category. Optional when all_overrides is True.
            key: Setting key. Optional for category-level reset.
            all_overrides: If True, delete ALL overrides globally.

        Returns:
            OperationResult with item int = number of overrides removed.

        """
        inputs = ConfigInput(
            action="reset",
            category=category,
            key=key,
            all_overrides=all_overrides,
        )
        resolved = ConfigRequest(inputs=inputs, db=Database()).resolve()
        if resolved.all_overrides:
            deleted = resolved.service.delete_all()
            if deleted:
                AuditLog.log(
                    "config.reset",
                    context=f"all overrides ({deleted} removed)",
                )
            return OperationResult(
                status="success",
                code="config.reset",
                message=f"Reset {deleted} override(s) globally",
                item=deleted,
            )
        assert resolved.category is not None
        if resolved.key is None:
            deleted = resolved.service.delete_by_category(resolved.category)
            if deleted:
                AuditLog.log(
                    "config.reset",
                    context=f"{resolved.category}.* ({deleted} removed)",
                )
            return OperationResult(
                status="success",
                code="config.reset",
                message=f"Reset {deleted} override(s) in {resolved.category}",
                item=deleted,
            )
        deleted = resolved.service.delete(resolved.category, resolved.key)
        if deleted:
            AuditLog.log(
                "config.reset",
                context=f"{resolved.category}.{resolved.key}",
            )
        result_count = 1 if deleted else 0
        return OperationResult(
            status="success",
            code="config.reset",
            message=f"Reset {resolved.category}.{resolved.key} ({result_count} override(s))",
            item=result_count,
        )

    @staticmethod
    def list_all() -> dict[str, dict[str, Any]]:
        """List all overridable settings with their current overrides."""
        inputs = ConfigInput(action="list")
        resolved = ConfigRequest(inputs=inputs, db=Database()).resolve()
        return resolved.service.list_all()
