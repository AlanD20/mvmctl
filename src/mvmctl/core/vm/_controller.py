"""
VM lifecycle operations.

This module contains the VMController class for managing VM lifecycle operations
like start, stop, pause, resume, ssh, logs, etc.
"""

from __future__ import annotations

import logging
from pathlib import Path

from mvmctl.core.vm._firecracker import FirecrackerClient
from mvmctl.core.vm._repository import VMRepository
from mvmctl.exceptions import MVMError
from mvmctl.models import VMInstanceItem, VMStatus
from mvmctl.utils._system import ProcessSignalHandler

logger = logging.getLogger(__name__)


class VMController:
    """
    Stateful VM lifecycle manager.

    Resolves VM entity in __init__ and operates on cached VM instance.
    """

    def __init__(
        self,
        entity: str | VMInstanceItem,
        repo: VMRepository,
    ) -> None:
        from mvmctl.core.vm._resolver import VMResolver

        self._repo = repo

        if isinstance(entity, VMInstanceItem):
            self._vm = entity
        else:
            self._resolver = VMResolver(self._repo)
            self._vm = self._resolver.resolve(entity)

    def stop(self, force: bool = False) -> None:
        """
        Stop the VM (idempotent — never raises).

        If the VM is already stopped or the underlying process is gone,
        returns immediately.  If the process exists but cannot be stopped,
        the status is set to ERROR and the method returns cleanly so
        that removal cleanup can still proceed.
        """

        # Idempotent — nothing to do if the VM isn't running.
        if self._vm.status not in (
            VMStatus.RUNNING.value,
            VMStatus.STARTING.value,
        ):
            return

        handler = ProcessSignalHandler(
            self._vm.pid,
            expected_start_time=self._vm.process_start_time,
        )

        if not self._vm.pid or not handler.is_alive():
            self._repo.update_status(self._vm.id, VMStatus.STOPPED.value)
            return

        self._repo.update_status(self._vm.id, VMStatus.STOPPING.value)
        try:
            if not force and self._vm.api_socket_path:
                # Try graceful shutdown via Firecracker API first
                try:
                    client = FirecrackerClient(
                        self._vm.vm_dir / self._vm.api_socket_path
                    )
                    client.send_ctrl_alt_del()
                    client.close()
                    # Wait for guest OS shutdown via pre_signal_hook
                    exit_code = handler.graceful_shutdown(
                        pre_signal_hook=lambda: False,
                    )
                except Exception:
                    # Fall through to signal-based shutdown
                    exit_code = None

                if exit_code is None:
                    exit_code = handler.graceful_shutdown()
            else:
                if force:
                    handler.kill()
                exit_code = handler.graceful_shutdown()

            # Capture exit code if not already captured
            if exit_code is None:
                exit_code = handler.wait_and_capture_exit()

            # Persist exit code to database
            if exit_code is not None:
                self._repo.update_exit_code(self._vm.id, exit_code)

            self._repo.update_status(self._vm.id, VMStatus.STOPPED.value)
        except Exception as exc:
            self._repo.update_status(self._vm.id, VMStatus.ERROR.value)
            logger.warning("Failed to stop VM '%s': %s", self._vm.name, exc)

    def pause(self) -> None:
        """
        Pause the VM (idempotent — no-op if already paused).

        Raises:
            MVMError: If VM cannot be paused from its current state

        """
        name = self._vm.name

        # No-op — already paused
        if self._vm.status == VMStatus.PAUSED.value:
            return

        # Cannot pause from these states
        if self._vm.status == VMStatus.STARTING.value:
            raise MVMError(
                f"VM '{name}' is still starting — cannot pause (current state: {self._vm.status})"
            )
        if self._vm.status == VMStatus.STOPPED.value:
            raise MVMError(
                f"VM '{name}' is stopped — cannot pause (current state: {self._vm.status})"
            )
        if self._vm.status == VMStatus.STOPPING.value:
            raise MVMError(
                f"VM '{name}' is shutting down — cannot pause (current state: {self._vm.status})"
            )
        if self._vm.status in (VMStatus.ERROR.value, VMStatus.CRASHED.value):
            raise MVMError(
                f"VM '{name}' is in {self._vm.status} state — cannot pause (current state: {self._vm.status})"
            )

        # Valid transition — must be RUNNING
        if not self._vm.api_socket_path:
            raise MVMError(f"VM '{name}' has no API socket enabled")
        client = FirecrackerClient(self._vm.vm_dir / self._vm.api_socket_path)
        try:
            client.pause_vm()
            self._repo.update_status(self._vm.id, VMStatus.PAUSED.value)
        finally:
            client.close()

    def resume(self) -> None:
        """
        Resume the VM (idempotent — no-op if already running/starting).

        Raises:
            MVMError: If VM cannot be resumed from its current state

        """
        name = self._vm.name

        # No-op — already in or moving toward target state (RUNNING)
        if self._vm.status in (VMStatus.RUNNING.value, VMStatus.STARTING.value):
            return

        # Error/crashed state
        if self._vm.status in (VMStatus.ERROR.value, VMStatus.CRASHED.value):
            raise MVMError(
                f"VM '{name}' is in {self._vm.status} state — remove and recreate (current state: {self._vm.status})"
            )

        # Wrong direction — stopped
        if self._vm.status == VMStatus.STOPPED.value:
            raise MVMError(
                f"VM '{name}' is stopped — use start() instead (current state: {self._vm.status})"
            )

        # Wrong direction — shutting down
        if self._vm.status == VMStatus.STOPPING.value:
            raise MVMError(
                f"VM '{name}' is shutting down — use start() after it stops (current state: {self._vm.status})"
            )

        # Valid transition — must be PAUSED
        if not self._vm.api_socket_path:
            raise MVMError(f"VM '{name}' has no API socket enabled")
        client = FirecrackerClient(self._vm.vm_dir / self._vm.api_socket_path)
        try:
            client.resume_vm()
            self._repo.update_status(self._vm.id, VMStatus.RUNNING.value)
        finally:
            client.close()

    def start(self) -> None:
        """
        Start an already configured VM via Firecracker API (idempotent — no-op if
        already running, starting, or stopping).

        Raises:
            MVMError: If VM cannot be started from its current state

        """
        name = self._vm.name

        # No-op — already in or moving toward target state (RUNNING),
        # or will be stopped soon (retry start after)
        if self._vm.status in (
            VMStatus.RUNNING.value,
            VMStatus.STARTING.value,
            VMStatus.STOPPING.value,
        ):
            return

        # Error/crashed state
        if self._vm.status in (VMStatus.ERROR.value, VMStatus.CRASHED.value):
            raise MVMError(
                f"VM '{name}' is in {self._vm.status} state — remove and recreate (current state: {self._vm.status})"
            )

        # Wrong direction — paused
        if self._vm.status == VMStatus.PAUSED.value:
            raise MVMError(
                f"VM '{name}' is paused — use resume() instead (current state: {self._vm.status})"
            )

        # Valid transition — must be STOPPED
        if not self._vm.api_socket_path:
            raise MVMError(f"VM '{name}' has no API socket enabled")

        client = FirecrackerClient(self._vm.vm_dir / self._vm.api_socket_path)
        try:
            client.start_instance()
            self._repo.update_status(self._vm.id, VMStatus.RUNNING.value)
        finally:
            client.close()

    def reboot(self, force: bool = False) -> None:
        """
        Reboot the VM (stop then boot).

        Args:
            force: If True, force immediate shutdown

        Raises:
            MVMError: If reboot fails

        """
        self.stop(force=force)
        self.start()

    def snapshot(self, mem_out: Path, state_out: Path) -> None:
        """
        Snapshot VM memory and disk state.

        Args:
            mem_out: Memory snapshot output path
            state_out: VM state output path

        Raises:
            MVMError: If socket not found or snapshot fails

        """
        if not self._vm.api_socket_path:
            raise MVMError(
                f"Socket not found for VM '{self._vm.name}'. Must be running with --enable-api-socket"
            )
        client = FirecrackerClient(self._vm.vm_dir / self._vm.api_socket_path)
        try:
            client.create_snapshot(mem_out, state_out)
        finally:
            client.close()

    def load_snapshot(
        self, mem_in: Path, state_in: Path, resume_after: bool = False
    ) -> None:
        """
        Load VM from snapshot.

        Args:
            mem_in: Memory snapshot input path
            state_in: VM state input path
            resume_after: Whether to resume VM after loading

        Raises:
            MVMError: If socket not found or load fails

        """
        if not self._vm.api_socket_path:
            raise MVMError(
                f"Socket not found for VM '{self._vm.name}'. Must be running with --enable-api-socket"
            )
        client = FirecrackerClient(self._vm.vm_dir / self._vm.api_socket_path)
        try:
            client.load_snapshot(mem_in, state_in, resume_after)
        finally:
            client.close()


__all__ = ["VMController"]
