"""SSH operation orchestration."""

from __future__ import annotations

import logging

from mvmctl.api.inputs._ssh_input import SSHInput, SSHRequest
from mvmctl.core._shared import Database
from mvmctl.core.ssh._service import SSHService
from mvmctl.exceptions import MVMError
from mvmctl.models.result import OperationResult
from mvmctl.utils.auditlog import AuditLog

logger = logging.getLogger(__name__)


class SSHOperation:
    """
    SSH orchestration operations.

    All methods are static and take Input classes as arguments.
    """

    @staticmethod
    def connect(inputs: SSHInput) -> OperationResult[int]:
        """
        Open SSH session or execute command on a VM.

        Args:
            inputs: Raw SSH input from CLI

        Returns:
            OperationResult with item int = exit code from SSH session.

        """
        try:
            db = Database()
            request = SSHRequest(inputs, db)
            resolved = request.resolve()

            AuditLog.log(
                "vm.ssh",
                changes={"ip": resolved.target_ip, "user": resolved.user},
            )

            exit_code = SSHService.connect(
                ip=resolved.target_ip,
                user=resolved.user,
                key_path=resolved.key,
                command=resolved.cmd,
                exec_mode=resolved.cmd is None,
            )

            if exit_code == 0:
                return OperationResult(
                    status="success",
                    code="ssh.connected",
                    message="SSH connection successful",
                    item=exit_code,
                )
            return OperationResult(
                status="error",
                code="ssh.failed",
                message=f"SSH command failed with exit code {exit_code}",
                item=exit_code,
            )
        except MVMError as e:
            return OperationResult(
                status="error",
                code="ssh.failed",
                message=str(e),
                exception=e,
            )
