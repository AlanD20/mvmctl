"""Generic resource context with cleanup tracking.

This module provides base context classes for resource operations across all API modules.
Used by: VM creation, network creation, image import, kernel fetch, etc.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any


@dataclass
class ResourceContext:
    """Generic context for resource operations with cleanup tracking.

        Tracks what resources have been created during an operation so they can be
    cleaned up if the operation fails.

        Usage:
            ctx = ResourceContext()
            try:
                create_resource_a()
                ctx.mark_created("resource_a")
                create_resource_b()
                ctx.mark_created("resource_b")
            except Exception:
                ctx.cleanup()  # Cleans up a and b
    """

    resources_created: dict[str, bool] = field(default_factory=dict)
    errors: list[Exception] = field(default_factory=list)

    def mark_created(self, resource: str) -> None:
        """Mark a resource as created for cleanup tracking."""
        self.resources_created[resource] = True

    def was_created(self, resource: str) -> bool:
        """Check if a resource was created."""
        return self.resources_created.get(resource, False)

    def add_error(self, error: Exception) -> None:
        """Add an error to the context."""
        self.errors.append(error)

    def cleanup(self) -> None:
        """Override in subclasses for resource-specific cleanup.

        Base implementation just clears tracking. Subclasses should override
        to perform actual resource cleanup.
        """
        self.resources_created.clear()


@dataclass
class BulkContext:
    """Generic context for bulk operations.

    Tracks multiple targets and any errors that occur during bulk processing.

    Usage:
        ctx = BulkContext()
        ctx.set_targets([vm1, vm2, vm3])
        for target in ctx.targets:
            try:
                process(target)
            except Exception as e:
                ctx.add_error(str(e))
    """

    targets: list[Any] = field(default_factory=list)
    errors: list[str] = field(default_factory=list)

    def set_targets(self, targets: list[Any]) -> None:
        """Set the list of targets for bulk processing."""
        self.targets = targets

    def add_error(self, error: str) -> None:
        """Add an error message to the context."""
        self.errors.append(error)


__all__ = [
    "ResourceContext",
    "BulkContext",
]
