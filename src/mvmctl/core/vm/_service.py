"""VM service — stateless operations coordinator.

Handles both single-VM and bulk VM operations.
Bulk operations delegate to VMController per VM via ParallelExecutor.
"""

from __future__ import annotations

from mvmctl.core._shared import ParallelExecutor
from mvmctl.core.vm._controller import VMController
from mvmctl.core.vm._repository import VMRepository
from mvmctl.models.bulk import BulkResult, BulkResultItem
from mvmctl.models.vm import VMInstanceItem


class VMService:
    """Stateless VM operations coordinator.

    Handles bulk operations and delegates single-VM operations to Controller.
    """

    def __init__(self, repo: VMRepository) -> None:
        self._repo = repo
        self._executor = ParallelExecutor()

    def stop(self, vm: VMInstanceItem, force: bool = False) -> None:
        """Stop a single VM."""
        controller = VMController(entity=vm, repo=self._repo)
        controller.stop(force=force)

    def stop_many(
        self,
        vms: list[VMInstanceItem],
        force: bool = False,
        parallel: bool = False,
        max_workers: int | None = None,
        batch_size: int | None = None,
    ) -> BulkResult[VMInstanceItem]:
        """Stop multiple VMs."""
        raw = self._executor.execute(
            items=vms,
            func=lambda vm: self.stop(vm, force=force),
            parallel=parallel,
            max_workers=max_workers,
            batch_size=batch_size,
        )
        return BulkResult(
            items=[BulkResultItem(item=vm, error=exc) for vm, exc in raw]
        )

    def start(self, vm: VMInstanceItem) -> None:
        """Start a single VM."""
        controller = VMController(entity=vm, repo=self._repo)
        controller.start()

    def start_many(
        self,
        vms: list[VMInstanceItem],
        parallel: bool = False,
        max_workers: int | None = None,
        batch_size: int | None = None,
    ) -> BulkResult[VMInstanceItem]:
        """Start multiple VMs."""
        raw = self._executor.execute(
            items=vms,
            func=lambda vm: self.start(vm),
            parallel=parallel,
            max_workers=max_workers,
            batch_size=batch_size,
        )
        return BulkResult(
            items=[BulkResultItem(item=vm, error=exc) for vm, exc in raw]
        )

    def pause(self, vm: VMInstanceItem) -> None:
        """Pause a single VM."""
        controller = VMController(entity=vm, repo=self._repo)
        controller.pause()

    def pause_many(
        self,
        vms: list[VMInstanceItem],
        parallel: bool = False,
        max_workers: int | None = None,
        batch_size: int | None = None,
    ) -> BulkResult[VMInstanceItem]:
        """Pause multiple VMs."""
        raw = self._executor.execute(
            items=vms,
            func=lambda vm: self.pause(vm),
            parallel=parallel,
            max_workers=max_workers,
            batch_size=batch_size,
        )
        return BulkResult(
            items=[BulkResultItem(item=vm, error=exc) for vm, exc in raw]
        )

    def resume(self, vm: VMInstanceItem) -> None:
        """Resume a single VM."""
        controller = VMController(entity=vm, repo=self._repo)
        controller.resume()

    def resume_many(
        self,
        vms: list[VMInstanceItem],
        parallel: bool = False,
        max_workers: int | None = None,
        batch_size: int | None = None,
    ) -> BulkResult[VMInstanceItem]:
        """Resume multiple VMs."""
        raw = self._executor.execute(
            items=vms,
            func=lambda vm: self.resume(vm),
            parallel=parallel,
            max_workers=max_workers,
            batch_size=batch_size,
        )
        return BulkResult(
            items=[BulkResultItem(item=vm, error=exc) for vm, exc in raw]
        )

    def reboot(self, vm: VMInstanceItem, force: bool = False) -> None:
        """Reboot a single VM."""
        controller = VMController(entity=vm, repo=self._repo)
        controller.reboot(force=force)

    def reboot_many(
        self,
        vms: list[VMInstanceItem],
        force: bool = False,
        parallel: bool = False,
        max_workers: int | None = None,
        batch_size: int | None = None,
    ) -> BulkResult[VMInstanceItem]:
        """Reboot multiple VMs."""
        raw = self._executor.execute(
            items=vms,
            func=lambda vm: self.reboot(vm, force=force),
            parallel=parallel,
            max_workers=max_workers,
            batch_size=batch_size,
        )
        return BulkResult(
            items=[BulkResultItem(item=vm, error=exc) for vm, exc in raw]
        )


__all__ = ["VMService"]
