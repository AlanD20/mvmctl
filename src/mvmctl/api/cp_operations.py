"""CP operation orchestration — tar-over-SSH file copy."""

from __future__ import annotations

import logging
from collections.abc import Callable
from typing import Any

from mvmctl.api.inputs._cp_input import CPInput, CPRequest
from mvmctl.core._shared import Database
from mvmctl.core.ssh._cp import CPService
from mvmctl.exceptions import CPError
from mvmctl.models.result import OperationResult
from mvmctl.utils.auditlog import AuditLog

logger = logging.getLogger(__name__)


class CPOperation:
    """
    File copy orchestration operations (host ↔ VM, VM ↔ VM).

    All methods are static and take Input classes as arguments.
    """

    @staticmethod
    def copy(
        inputs: CPInput,
        on_progress: Callable[[int], None] | None = None,
    ) -> OperationResult[dict[str, Any]]:
        """
        Copy files between host and microVMs using tar-over-SSH.

        Args:
            inputs: Raw copy input from CLI.
            on_progress: Callback receiving bytes read per chunk (for CLI progress display).

        Returns:
            OperationResult with ``item`` containing ``bytes`` and ``message``.

        """
        try:
            db = Database()
            request = CPRequest(inputs, db)
            resolved = request.resolve()

            AuditLog.log(
                "cp.copy",
                changes={
                    "direction": resolved.direction,
                    "src": inputs.src,
                    "dst": inputs.dst,
                    "force": inputs.force,
                },
            )

            total_bytes: int = 0
            result_message: str = ""

            if resolved.direction == "host_to_vm":
                if resolved.dst_info is None or resolved.local_path is None:
                    raise CPError(
                        "Internal error: destination VM info not available",
                        code="cp.resolve_failed",
                    )

                total_bytes, result_message = CPService.copy_host_to_vm(
                    local_path=resolved.local_path,
                    vm_ip=resolved.dst_info.ip,
                    vm_user=resolved.dst_info.user,
                    vm_key_path=resolved.dst_info.key_path,
                    remote_dst=resolved.dst_info.remote_path,
                    force=resolved.force,
                    on_progress=on_progress,
                )

            elif resolved.direction == "vm_to_host":
                if resolved.src_info is None or resolved.local_path is None:
                    raise CPError(
                        "Internal error: source VM info not available",
                        code="cp.resolve_failed",
                    )

                total_bytes, result_message = CPService.copy_vm_to_host(
                    vm_ip=resolved.src_info.ip,
                    vm_user=resolved.src_info.user,
                    vm_key_path=resolved.src_info.key_path,
                    remote_path=resolved.src_info.remote_path,
                    local_dst=resolved.local_path,
                    force=resolved.force,
                    on_progress=on_progress,
                )

            elif resolved.direction == "vm_to_vm":
                if resolved.src_info is None or resolved.dst_info is None:
                    raise CPError(
                        "Internal error: source or destination VM info not available",
                        code="cp.resolve_failed",
                    )

                total_bytes, result_message = CPService.copy_vm_to_vm(
                    src_ip=resolved.src_info.ip,
                    src_user=resolved.src_info.user,
                    src_key_path=resolved.src_info.key_path,
                    src_path=resolved.src_info.remote_path,
                    dst_ip=resolved.dst_info.ip,
                    dst_user=resolved.dst_info.user,
                    dst_key_path=resolved.dst_info.key_path,
                    dst_path=resolved.dst_info.remote_path,
                    force=resolved.force,
                    on_progress=on_progress,
                )

            return OperationResult[dict[str, Any]](
                status="success",
                code="cp.success",
                message=result_message,
                item={"bytes": total_bytes, "message": result_message},
            )

        except CPError as e:
            logger.debug("CP error: %s", e, exc_info=True)
            return OperationResult[dict[str, Any]](
                status="error",
                code=e.code or "cp.failed",
                message=str(e),
                exception=e,
            )
