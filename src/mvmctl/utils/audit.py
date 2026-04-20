import getpass
import logging
import os
from datetime import datetime, timezone
from pathlib import Path

from mvmctl.utils.common import CacheUtils


def _get_audit_log_path() -> Path:
    return CacheUtils.get_cache_dir() / "audit.log"


def _audit_logger() -> logging.Logger:
    """Return the singleton audit logger, configuring a file handler on first call."""
    audit = logging.getLogger("mvmctl.audit")
    if audit.handlers:
        return audit
    audit.setLevel(logging.INFO)
    audit.propagate = False
    try:
        log_path = _get_audit_log_path()
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
    return audit


def log_audit(operation: str, detail: str = "") -> None:
    """Write a structured audit log entry for the given operation.

    Args:
        operation: Short identifier for the operation performed (e.g. ``host.init``).
        detail: Optional additional context appended to the log entry.
    """
    try:
        user = getpass.getuser()
    except Exception:
        user = str(os.getuid())
    ts = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    msg = f"[{ts}] user={user} op={operation}"
    if detail:
        msg += f" detail={detail!r}"
    _audit_logger().info(msg)
