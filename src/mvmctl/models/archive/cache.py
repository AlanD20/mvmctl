"""Cache data models."""

from __future__ import annotations

from dataclasses import dataclass


@dataclass
class PruneAllResult:
    """Result of a full cache prune operation."""

    pruned_vms: list[str]
    pruned_networks: list[str]
    pruned_images: list[str]
    pruned_kernels: list[str]
    had_running_vms: bool
