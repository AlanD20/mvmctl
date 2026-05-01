"""Centralized audit logging."""

from __future__ import annotations

import getpass
import logging
import os
from datetime import UTC, datetime

from mvmctl.utils.common import CacheUtils


class AuditLog:
    """
    Centralized audit logger.

    Provides a single structured log method with operation, changes dict,
    and context string. All audit entries append to the audit log file.
    """

    _logger: logging.Logger | None = None

    @classmethod
    def _get_logger(cls) -> logging.Logger:
        """Return the singleton audit logger, configuring handler on first call."""
        if cls._logger is not None:
            return cls._logger

        audit = logging.getLogger("mvmctl.audit")
        audit.setLevel(logging.INFO)
        audit.propagate = False

        try:
            log_path = CacheUtils.get_audit_log_path()
            log_path.parent.mkdir(parents=True, exist_ok=True)
            handler = logging.FileHandler(log_path, mode="a", encoding="utf-8")
            handler.setFormatter(
                logging.Formatter(
                    "%(asctime)s UTC %(message)s", datefmt="%Y-%m-%dT%H:%M:%S"
                )
            )
            audit.addHandler(handler)
        except OSError:
            audit.addHandler(logging.NullHandler())

        cls._logger = audit
        return cls._logger

    @classmethod
    def _user(cls) -> str:
        try:
            return getpass.getuser()
        except Exception:
            return str(os.getuid())

    @classmethod
    def log(
        cls,
        operation: str,
        changes: dict[str, str | int | bool] | None = None,
        context: str = "",
    ) -> None:
        """
        Write a structured audit log entry.

        Args:
            operation: Short identifier for the operation (e.g. ``binary.fetch``).
            changes: Key-value pairs describing what changed.
            context: Additional free-form context string.

        """
        ts = datetime.now(UTC).strftime("%Y-%m-%dT%H:%M:%SZ")
        msg = f"[{ts}] user={cls._user()} op={operation}"
        if changes:
            changes_str = ",".join(f"{k}={v}" for k, v in changes.items())
            msg += f" changes={changes_str}"
        if context:
            msg += f" context={context!r}"
        cls._get_logger().info(msg)
