"""Log viewing operations — orchestrates log retrieval."""

from __future__ import annotations

from collections.abc import Generator

from mvmctl.api.inputs._logs_input import LogInput, LogRequest
from mvmctl.core._shared import Database
from mvmctl.core.logs._controller import LogController
from mvmctl.core.vm._repository import VMRepository


class LogOperation:
    """Log viewing orchestration."""

    @staticmethod
    def stream(inputs: LogInput) -> Generator[str, None, None]:
        """Stream log lines for a VM.

        If follow is True, yields lines indefinitely (use Ctrl+C to stop).
        If follow is False, yields the last N lines then stops.

        Args:
            inputs: LogInput with VM identifier and options

        Yields:
            Log line strings
        """
        resolved = LogRequest(inputs=inputs, db=Database()).resolve()
        controller = LogController(resolved.vm, VMRepository(Database()))
        if resolved.follow:
            yield from controller.follow(resolved.log_type)
        else:
            yield from controller.show(resolved.log_type, resolved.lines)
