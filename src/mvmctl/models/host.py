"""Host data models."""

from __future__ import annotations

from dataclasses import dataclass


@dataclass
class HostStateItem:
    """Host state record — maps to host_state table (singleton id=1)."""

    id: int
    initialized: bool
    mvm_group_created: bool
    sudoers_configured: bool
    default_network_created: bool
    initialized_at: str
    updated_at: str


@dataclass
class HostStateChangeItem:
    """Host state change record — maps to host_state_changes table."""

    session_id: str
    init_timestamp: str
    setting: str
    mechanism: str
    applied_value: str
    reverted: bool
    change_order: int
    created_at: str

    id: int | None = None
    original_value: str | None = None
    reverted_at: str | None = None
    revert_mechanism: str | None = None
