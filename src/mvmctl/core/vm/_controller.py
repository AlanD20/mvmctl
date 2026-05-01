"""
VM lifecycle operations.

This module contains the VMController class for managing VM lifecycle operations
like start, stop, pause, resume, ssh, logs, etc.
"""

from __future__ import annotations

import logging
from pathlib import Path

from mvmctl.constants import DEFAULT_SNAPSHOT_RESUME
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
        Stop the VM.

        Args:
            force: If True, force immediate shutdown without graceful shutdown

        Raises:
            MVMError: If VM is not running or stop fails

        """
        name = self._vm.name
        if self._vm.status not in (VMStatus.RUNNING.value,):
            raise MVMError(
                f"VM '{name}' is not running (current state: {self._vm.status})"
            )
        self._repo.update_status(self._vm.id, VMStatus.STOPPING.value)
        try:
            handler = ProcessSignalHandler(
                self._vm.pid,
                expected_start_time=self._vm.process_start_time,
            )

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
            raise MVMError(f"Failed to stop VM '{name}': {exc}") from exc

    def pause(self) -> None:
        """
        Pause the VM.

        Raises:
            MVMError: If VM is not running

        """
        name = self._vm.name
        if self._vm.status != VMStatus.RUNNING.value:
            raise MVMError(
                f"VM '{name}' is not running (current state: {self._vm.status})"
            )
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
        Resume the VM.

        Raises:
            MVMError: If VM is not paused

        """
        name = self._vm.name
        if self._vm.status != VMStatus.PAUSED.value:
            raise MVMError(
                f"VM '{name}' is not paused (current state: {self._vm.status})"
            )
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
        Start an already configured VM via Firecracker API.

        Raises:
            MVMError: If VM is not stopped or start fails

        """
        name = self._vm.name
        if self._vm.status != VMStatus.STOPPED.value:
            raise MVMError(
                f"VM '{name}' is not stopped (current state: {self._vm.status})"
            )
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
        self, mem_in: Path, state_in: Path, resume_after: bool | None = None
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
        effective_resume = (
            resume_after
            if resume_after is not None
            else DEFAULT_SNAPSHOT_RESUME
        )
        if not self._vm.api_socket_path:
            raise MVMError(
                f"Socket not found for VM '{self._vm.name}'. Must be running with --enable-api-socket"
            )
        client = FirecrackerClient(self._vm.vm_dir / self._vm.api_socket_path)
        try:
            client.load_snapshot(mem_in, state_in, effective_resume)
        finally:
            client.close()


__all__ = ["VMController"]
