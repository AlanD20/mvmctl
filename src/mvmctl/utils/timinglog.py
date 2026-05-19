"""Centralized timing logging for VM creation phases."""

from __future__ import annotations

import logging
import time
from collections.abc import Generator
from contextlib import contextmanager

from mvmctl.utils.common import CacheUtils, env

_TIMING_ENABLED: bool = env.get("TIMING_ENABLED") is not None


class TimingLog:
    """
    Centralized timing logger for VM creation phases.

    Provides a singleton logger that writes machine-parseable timing entries
    to the timing log file. Gated by MVM_TIMING_ENABLED environment variable.
    """

    _logger: logging.Logger | None = None

    @classmethod
    def _get_logger(cls) -> logging.Logger:
        """Return the singleton timing logger, configuring handler on first call."""
        if cls._logger is not None:
            return cls._logger

        timing = logging.getLogger("mvmctl.timing")
        timing.setLevel(logging.INFO)
        timing.propagate = False

        if _TIMING_ENABLED:
            try:
                log_path = CacheUtils.get_timing_log_path()
                log_path.parent.mkdir(parents=True, exist_ok=True)
                handler = logging.FileHandler(
                    log_path, mode="a", encoding="utf-8"
                )
                handler.setFormatter(
                    logging.Formatter(
                        "%(asctime)s %(message)s",
                        datefmt="%Y-%m-%dT%H:%M:%S",
                    )
                )
                timing.addHandler(handler)
            except OSError:
                timing.addHandler(logging.NullHandler())
        else:
            timing.addHandler(logging.NullHandler())

        cls._logger = timing
        return cls._logger


@contextmanager
def timed(phase: str, vm_name: str, vm_id: str) -> Generator[None, None, None]:
    """
    Context manager that measures and logs elapsed time for a phase.

    Yields immediately with zero overhead when MVM_TIMING_ENABLED is unset.
    Each log line is machine-parseable: phase name, elapsed_ms, vm_name, vm_id.
    """
    if not _TIMING_ENABLED:
        yield
        return
    start = time.perf_counter()
    yield
    elapsed_ms = (time.perf_counter() - start) * 1000
    TimingLog._get_logger().info(
        "phase=%s elapsed_ms=%.3f vm_name=%s vm_id=%s",
        phase,
        elapsed_ms,
        vm_name,
        vm_id,
    )
