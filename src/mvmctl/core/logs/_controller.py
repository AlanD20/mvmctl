"""Stateful log controller — bound to a VM entity."""

from __future__ import annotations

from collections.abc import Generator

from mvmctl.core.logs._service import LogService
from mvmctl.core.vm._repository import VMRepository
from mvmctl.core.vm._resolver import VMResolver
from mvmctl.models.vm import VMInstanceItem


class LogController:
    """Stateful log controller bound to a single VM.

    Args:
        entity: VM identifier (name, ID, IP, MAC) or VMInstanceItem
        repo: VMRepository instance
    """

    def __init__(
        self, entity: str | VMInstanceItem, repo: VMRepository
    ) -> None:
        if isinstance(entity, VMInstanceItem):
            self._vm = entity
        else:
            resolver = VMResolver(repo)
            self._vm = resolver.resolve(entity)
        self._hash: str = self._vm.id if self._vm.id else self._vm.name

    @property
    def vm(self) -> VMInstanceItem:
        """The resolved VM instance."""
        return self._vm

    def show(self, log_type: str, lines: int) -> list[str]:
        """Read the last N lines from the VM's log file.

        Args:
            log_type: 'boot' or 'os'
            lines: Number of trailing lines to return

        Returns:
            List of log line strings.
        """
        log_file = LogService.get_log_path(self._hash, log_type)
        return LogService.read_log_lines(log_file, lines)

    def follow(self, log_type: str) -> Generator[str, None, None]:
        """Stream log file lines in real-time.

        Args:
            log_type: 'boot' or 'os'

        Yields:
            New log lines as they are written.
        """
        log_file = LogService.get_log_path(self._hash, log_type)
        return LogService.follow_log(log_file)
