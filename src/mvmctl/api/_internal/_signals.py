"""Signal handling helpers for long-running operations."""

from __future__ import annotations

import signal
from contextlib import contextmanager
from typing import Any, Callable

__all__ = [
    "SigtermContext",
]


class SigtermContext:
    """Context manager for SIGTERM signal handling.

    Sets up a signal handler on entry, restores original handler on exit.
    The signal handler calls the provided cleanup function.
    """

    def __init__(self, cleanup_fn: Callable[[], None]) -> None:
        self._cleanup_fn = cleanup_fn
        self._old_handler: Any = None

    def _handle_signal(self, signum: int, frame: Any) -> None:
        self._cleanup_fn()

    def __enter__(self) -> "SigtermContext":
        self._old_handler = signal.signal(signal.SIGTERM, self._handle_signal)
        return self

    def __exit__(self, exc_type: Any, exc_val: Any, exc_tb: Any) -> None:
        if self._old_handler is not None:
            signal.signal(signal.SIGTERM, self._old_handler)
        return None


@contextmanager
def sigterm_context(cleanup_fn: Callable[[], None]) -> Any:
    """Create a SigtermContext as a context manager.

    Usage:
        with sigterm_context(my_cleanup):
            # do work
    """
    ctx = SigtermContext(cleanup_fn)
    ctx.__enter__()
    try:
        yield ctx
    finally:
        ctx.__exit__(None, None, None)
