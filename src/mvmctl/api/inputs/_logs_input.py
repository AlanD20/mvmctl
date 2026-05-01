"""Log input resolution — Input → Request → ResolvedLogInput."""

from __future__ import annotations

from dataclasses import dataclass

from mvmctl.api.inputs._vm_input import VMInput, VMRequest
from mvmctl.core._shared import Database
from mvmctl.core.config._service import SettingsService
from mvmctl.models.vm import VMInstanceItem


@dataclass
class LogInput:
    """Raw log viewing parameters from CLI."""

    identifier: str
    os_log: bool = False
    lines: int | None = None
    follow: bool | None = None


@dataclass(frozen=True)
class ResolvedLogInput:
    """Fully resolved log viewing parameters."""

    vm: VMInstanceItem
    log_type: str
    lines: int
    follow: bool
    log_filename: str
    serial_output_filename: str


class LogRequest:
    """Resolve LogInput against the database and constants."""

    def __init__(self, *, inputs: LogInput, db: Database | None = None) -> None:
        self._inputs = inputs
        self._db = db if db is not None else Database()
        self._result: ResolvedLogInput | None = None

    @property
    def result(self) -> ResolvedLogInput | None:
        return self._result

    def resolve(self) -> ResolvedLogInput:
        """Resolve all inputs to explicit values."""
        vm = self._resolve_vm()
        log_type = self._resolve_log_type()
        lines = self._resolve_lines()
        follow = self._resolve_follow()
        log_filename = str(
            SettingsService.resolve(
                self._db, "defaults.firecracker", "log_filename"
            )
        )
        serial_output_filename = str(
            SettingsService.resolve(
                self._db, "defaults.firecracker", "serial_output_filename"
            )
        )

        self._result = ResolvedLogInput(
            vm=vm,
            log_type=log_type,
            lines=lines,
            follow=follow,
            log_filename=log_filename,
            serial_output_filename=serial_output_filename,
        )
        return self._result

    def _resolve_vm(self) -> VMInstanceItem:
        resolved = VMRequest(
            inputs=VMInput(identifiers=[self._inputs.identifier]),
            db=self._db,
        ).resolve()
        return resolved.vms[0]

    def _resolve_log_type(self) -> str:
        if self._inputs.os_log:
            return "os"
        return "boot"

    def _resolve_lines(self) -> int:
        if self._inputs.lines is not None:
            return self._inputs.lines
        return int(
            SettingsService.resolve(self._db, "settings.vm", "log_lines")
        )

    def _resolve_follow(self) -> bool:
        if self._inputs.follow is not None:
            return self._inputs.follow
        return bool(
            SettingsService.resolve(self._db, "settings.vm", "log_follow")
        )
