from __future__ import annotations

from dataclasses import dataclass


@dataclass
class HostStateChange:
    """Represents a single host configuration change."""

    setting: str
    original_value: str | None
    applied_value: str
    mechanism: str


@dataclass
class HostState:
    """Represents the host state snapshot for backward compatibility."""

    init_timestamp: str
    changes: list[HostStateChange]
