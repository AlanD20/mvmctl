"""SSH operation orchestration."""

from __future__ import annotations

import logging

from mvmctl.api.inputs._ssh_input import SSHInput, SSHRequest
from mvmctl.core._shared import Database
from mvmctl.core.ssh._service import SSHService
from mvmctl.utils.auditlog import AuditLog

logger = logging.getLogger(__name__)


class SSHOperation:
    """SSH orchestration operations.

    All methods are static and take Input classes as arguments.
    """

    @staticmethod
    def connect(inputs: SSHInput) -> int:
        """Open SSH session or execute command on a VM.

        Args:
            inputs: Raw SSH input from CLI

        Returns:
            Exit code from SSH session
        """
        db = Database()
        request = SSHRequest(inputs, db)
        resolved = request.resolve()

        AuditLog.log(
            "vm.ssh",
            changes={"ip": resolved.target_ip, "user": resolved.user},
        )

        return SSHService.connect(
            ip=resolved.target_ip,
            user=resolved.user,
            key_path=resolved.key,
            command=resolved.cmd,
            exec_mode=resolved.cmd is None,
        )
