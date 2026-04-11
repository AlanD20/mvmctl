"""Exception handling helpers for VM API modules."""

from __future__ import annotations

from typing import TYPE_CHECKING, Callable

if TYPE_CHECKING:
    from mvmctl.models.vm import VMInstance

__all__ = [
    "handle_creation_error",
]


def handle_creation_error(
    exc: Exception,
    vm_instance: VMInstance | None,
    skip_cleanup: bool,
    cleanup_fn: Callable[[], None],
    persist_fn: Callable[[VMInstance, object | None], None] | None = None,
    manager: object | None = None,
) -> None:
    """Unified exception handler for VM creation.

    Handles the common pattern across all exception types in create_vm:
    - If skip_cleanup is True and vm_instance exists, persist the failed VM
    - Otherwise, call cleanup_fn to release all resources
    - Always re-raise the original exception (caller must use 'raise' after this)

    Args:
        exc: The exception that occurred
        vm_instance: The VM instance (may be None if error occurred before creation)
        skip_cleanup: If True and vm_instance exists, persist the failed VM instead of cleaning up
        cleanup_fn: Cleanup function to call (releases network, files, etc.)
        persist_fn: Optional function to persist a failed VM to DB
        manager: Optional VM manager for persist_fn
    """
    if skip_cleanup and vm_instance is not None:
        if persist_fn is not None:
            persist_fn(vm_instance, manager)
    elif not skip_cleanup:
        cleanup_fn()
    # Caller is responsible for re-raising the exception
