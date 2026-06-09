"""Console operations - connection info, state, kill for VM console relays."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

from mvmctl.api.inputs import ConsoleInput, ConsoleRequest
from mvmctl.exceptions import MVMError
from mvmctl.models.result import OperationResult
from mvmctl.utils.auditlog import AuditLog


@dataclass(frozen=True)
class ConsoleConnectionInfo:
    """
    Console connection info for a running VM relay socket.

    Attributes:
        socket_path: Path to the console relay Unix domain socket.
        vm_name: Resolved VM name.
        vm_id: Resolved VM ID.

    """

    socket_path: str
    vm_name: str
    vm_id: str


class ConsoleOperation:
    """Console relay orchestration for VM console access."""

    @staticmethod
    def get_state(identifier: str) -> dict[str, Any]:
        """
        Get console relay state for a VM.

        Args:
            identifier: VM name, ID, MAC, or IP address.

        Returns:
            Dict with: running (bool), pid (int|None), socket_path (str).

        """
        resolved = ConsoleRequest(
            inputs=ConsoleInput(identifier=identifier)
        ).resolve()
        return {
            "running": resolved.relay.is_running(),
            "pid": resolved.relay.get_pid(),
            "socket_path": str(resolved.relay.socket_path),
        }

    @staticmethod
    def get_connection_info(identifier: str) -> ConsoleConnectionInfo:
        """
        Get connection info for a VM console relay.

        Args:
            identifier: VM name, ID, MAC, or IP address.

        Returns:
            ConsoleConnectionInfo with socket path, VM name, and VM ID.

        Raises:
            MVMError: If console relay is not running.

        """
        resolved = ConsoleRequest(
            inputs=ConsoleInput(identifier=identifier)
        ).resolve()
        if not resolved.relay.is_running():
            raise MVMError(f"No console relay running for VM '{identifier}'")

        return ConsoleConnectionInfo(
            socket_path=str(resolved.relay.socket_path),
            vm_name=resolved.vm.name,
            vm_id=resolved.vm.id,
        )

    @staticmethod
    def kill(identifier: str) -> OperationResult[bool]:
        """
        Kill the console relay for a VM.

        Args:
            identifier: VM name, ID, MAC, or IP address.

        Returns:
            OperationResult with item bool: True if relay was stopped,
            False if not running or failed.

        """
        resolved = ConsoleRequest(
            inputs=ConsoleInput(identifier=identifier)
        ).resolve()
        if not resolved.relay.is_running():
            return OperationResult(
                status="skipped",
                code="console.not_running",
                message=f"No console relay running for '{identifier}'",
                item=False,
            )

        killed = resolved.relay.stop(force=True)
        AuditLog.log("console.kill", changes={"name": identifier})

        if killed:
            return OperationResult(
                status="success",
                code="console.killed",
                message=f"Console relay stopped for '{identifier}'",
                item=True,
            )
        return OperationResult(
            status="error",
            code="console.kill_failed",
            message=f"Failed to stop console relay for '{identifier}'",
            item=False,
        )
