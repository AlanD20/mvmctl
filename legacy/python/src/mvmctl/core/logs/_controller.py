"""Stateful log controller — bound to a VM entity."""

from __future__ import annotations

from collections.abc import Generator

from mvmctl.core.logs._service import LogService
from mvmctl.models import VMInstanceItem


class LogController:
    """
    Stateful log controller bound to a single VM.

    Args:
        vm: Resolved VMInstanceItem whose logs to view.

    """

    def __init__(self, vm: VMInstanceItem) -> None:
        self._vm = vm
        self._hash: str = self._vm.id if self._vm.id else self._vm.name

    @property
    def vm(self) -> VMInstanceItem:
        """The resolved VM instance."""
        return self._vm

    def show(
        self,
        log_type: str,
        lines: int,
        *,
        log_filename: str,
        serial_output_filename: str,
    ) -> list[str]:
        """
        Read the last N lines from the VM's log file.

        Args:
            log_type: 'boot' or 'os'
            lines: Number of trailing lines to return
            log_filename: Firecracker log filename override
            serial_output_filename: Serial output filename override

        Returns:
            List of log line strings.

        """
        log_file = LogService.get_log_path(
            self._hash,
            log_type,
            log_filename=log_filename,
            serial_output_filename=serial_output_filename,
        )
        return LogService.read_log_lines(log_file, lines)

    def follow(
        self,
        log_type: str,
        *,
        log_filename: str,
        serial_output_filename: str,
    ) -> Generator[str]:
        """
        Stream log file lines in real-time.

        Args:
            log_type: 'boot' or 'os'
            log_filename: Firecracker log filename override
            serial_output_filename: Serial output filename override

        Yields:
            New log lines as they are written.

        """
        log_file = LogService.get_log_path(
            self._hash,
            log_type,
            log_filename=log_filename,
            serial_output_filename=serial_output_filename,
        )
        return LogService.follow_log(log_file)
