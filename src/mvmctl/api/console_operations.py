"""Console operations - attach, state, kill for VM console relays."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Any

from mvmctl.api.inputs import ConsoleInput, ConsoleRequest
from mvmctl.exceptions import MVMError
from mvmctl.utils.auditlog import AuditLog


@dataclass(frozen=True)
class ConsoleAttachInfo:
    """
    Result of a console attach operation — connection info for the relay socket.

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
    def attach(identifier: str) -> ConsoleAttachInfo:
        """
        Attach to VM console.

        Args:
            identifier: VM name, ID, MAC, or IP address.

        Returns:
            ConsoleAttachInfo with socket path, VM name, and VM ID.

        Raises:
            MVMError: If console relay is not running.

        """
        resolved = ConsoleRequest(
            inputs=ConsoleInput(identifier=identifier)
        ).resolve()
        if not resolved.relay.is_running():
            raise MVMError(f"No console relay running for VM '{identifier}'")

        return ConsoleAttachInfo(
            socket_path=str(resolved.relay.socket_path),
            vm_name=resolved.vm.name,
            vm_id=resolved.vm.id,
        )

    @staticmethod
    def kill(identifier: str) -> bool:
        """
        Kill the console relay for a VM.

        Args:
            identifier: VM name, ID, MAC, or IP address.

        Returns:
            True if relay was stopped, False if not running.

        """
        resolved = ConsoleRequest(
            inputs=ConsoleInput(identifier=identifier)
        ).resolve()
        if not resolved.relay.is_running():
            return False

        result = resolved.relay.terminate()
        AuditLog.log("console.kill", changes={"name": identifier})

        return result
