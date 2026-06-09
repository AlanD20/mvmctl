"""Log viewing operations — orchestrates log retrieval."""

from __future__ import annotations

from collections.abc import Generator

from mvmctl.api.inputs._logs_input import LogInput, LogRequest
from mvmctl.core.logs._controller import LogController


class LogOperation:
    """Log viewing orchestration."""

    @staticmethod
    def stream(inputs: LogInput) -> Generator[str]:
        """
        Stream log lines for a VM.

        If follow is True, yields lines indefinitely (use Ctrl+C to stop).
        If follow is False, yields the last N lines then stops.

        Args:
            inputs: LogInput with VM identifier and options

        Yields:
            Log line strings

        """
        resolved = LogRequest(inputs=inputs).resolve()
        controller = LogController(resolved.vm)
        if resolved.follow:
            yield from controller.follow(
                resolved.log_type,
                log_filename=resolved.log_filename,
                serial_output_filename=resolved.serial_output_filename,
            )
        else:
            yield from controller.show(
                resolved.log_type,
                resolved.lines,
                log_filename=resolved.log_filename,
                serial_output_filename=resolved.serial_output_filename,
            )
