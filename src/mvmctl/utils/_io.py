"""Logging utilities."""

from __future__ import annotations

import logging
import os

# ==================== Logging utilities ====================


def setup_logging(*, verbose: bool = False, debug: bool = False) -> None:
    """
    Configure root logger with console and file handlers.

    Console handler respects the configured level (DEBUG/INFO/WARNING).
    File handler always logs at DEBUG level to {cache_dir}/mvmctl.log
    for persistent debugging without requiring --debug flags.

    Priority (highest first):
    1. ``debug=True``  → DEBUG
    2. ``verbose=True`` → INFO
    3. ``MVM_LOG_LEVEL`` env var → parsed level (default WARNING)

    Args:
        verbose: Force INFO level on console.
        debug: Force DEBUG level on console.

    """
    if debug:
        level = logging.DEBUG
    elif verbose:
        level = logging.INFO
    else:
        env_level = os.environ.get("MVM_LOG_LEVEL", "WARNING").upper()
        level = getattr(logging, env_level, logging.WARNING)

    root = logging.getLogger()

    # Prevent duplicate handler setup on repeated calls
    if root.handlers:
        return

    formatter = logging.Formatter("%(levelname)s: %(name)s: %(message)s")

    # Console handler at configured level
    console = logging.StreamHandler()
    console.setLevel(level)
    console.setFormatter(formatter)
    root.addHandler(console)

    # File handler always at DEBUG — captures everything without --debug flags
    from logging.handlers import RotatingFileHandler

    from mvmctl.utils.common import CacheUtils

    log_path = CacheUtils.get_log_path()
    try:
        file_handler = RotatingFileHandler(
            str(log_path), maxBytes=10_485_760, backupCount=3
        )
    except Exception:
        pass
    else:
        file_handler.setLevel(logging.DEBUG)
        file_handler.setFormatter(formatter)
        root.addHandler(file_handler)

    # Root must be at lowest level so individual handlers can filter up
    root.setLevel(logging.DEBUG)


def get_logger(name: str) -> logging.Logger:
    """
    Return a logger instance with the given name.

    Thin wrapper around :func:`logging.getLogger` for consistency.
    """
    return logging.getLogger(name)


def log_exception(logger: logging.Logger, msg: str, exc: Exception) -> None:
    """
    Log an exception respecting the configured log level.

    When DEBUG is enabled, logs the full traceback via
    :meth:`logging.Logger.exception`. Otherwise logs a single-line
    ERROR message without traceback.
    """
    if logger.isEnabledFor(logging.DEBUG):
        logger.exception("%s: %s", msg, exc)
    else:
        logger.error("%s: %s", msg, exc)
