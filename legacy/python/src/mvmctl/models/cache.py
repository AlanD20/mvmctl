from __future__ import annotations

from dataclasses import dataclass


@dataclass
class PruneAllResult:
    """Result of a full cache prune operation across all resource types."""

    pruned_ids: list[str]
    """All identifiers that were successfully pruned."""

    failed_ids: list[str]
    """All identifiers that failed to prune."""

    had_running_vms: bool
    """Whether any running or starting VMs were present during pruning."""


@dataclass
class CleanResult:
    """Result of a complete cache clean operation."""

    prune_result: PruneAllResult
    """Result of the prune_all step."""

    cache_dir_removed: bool
    """Whether the cache directory itself was removed."""

    cache_dir: str
    """Path to the cache directory that was (or would be) removed."""


__all__ = ["PruneAllResult", "CleanResult"]
