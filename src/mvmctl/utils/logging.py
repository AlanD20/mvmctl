"""Centralized logging configuration for mvmctl.

All logging setup goes through this module to ensure consistent formatting
and level configuration across the entire application.
"""

from __future__ import annotations

import logging
import os


def setup_logging(*, verbose: bool = False, debug: bool = False) -> None:
    """Configure root logger level and format.

    Priority (highest first):
    1. ``debug=True``  → DEBUG
    2. ``verbose=True`` → INFO
    3. ``MVM_LOG_LEVEL`` env var → parsed level (default WARNING)

    Args:
        verbose: Force INFO level.
        debug: Force DEBUG level.
    """
    if debug:
        level = logging.DEBUG
    elif verbose:
        level = logging.INFO
    else:
        env_level = os.environ.get("MVM_LOG_LEVEL", "WARNING").upper()
        level = getattr(logging, env_level, logging.WARNING)

    logging.basicConfig(
        level=level,
        format="%(levelname)s: %(name)s: %(message)s",
    )


def get_logger(name: str) -> logging.Logger:
    """Return a logger instance with the given name.

    Thin wrapper around :func:`logging.getLogger` for consistency.
    """
    return logging.getLogger(name)


def log_exception(logger: logging.Logger, msg: str, exc: Exception) -> None:
    """Log an exception respecting the configured log level.

    When DEBUG is enabled, logs the full traceback via
    :meth:`logging.Logger.exception`. Otherwise logs a single-line
    ERROR message without traceback.
    """
    if logger.isEnabledFor(logging.DEBUG):
        logger.exception("%s: %s", msg, exc)
    else:
        logger.error("%s: %s", msg, exc)
