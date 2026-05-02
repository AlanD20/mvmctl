"""
Operation result types for API-to-consumer communication.

Every public API method returns :class:`OperationResult` or
:class:`BatchResult` to communicate what happened, what didn't,
and why — without performing any I/O itself.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Generic, Literal, TypeVar

T = TypeVar("T")

OperationStatus = Literal[
    "success",
    "skipped",
    "warning",
    "error",
    "failure",
]


@dataclass
class OperationResult(Generic[T]):
    """
    Result of a single operation on a single item.

    Generic over T, the domain model type (VMInstanceItem, NetworkItem, …).
    """

    #: Machine-readable status. One of the five Literal values above.
    status: OperationStatus

    #: Machine-readable reason code. Convention: ``<domain>.<verb>[.<reason>]``.
    #: Also used as the audit log event name.
    code: str

    #: Human-readable message. CLI uses directly; TUI/GUI may format
    #: based on ``status`` + ``code`` + ``item`` instead.
    message: str = ""

    #: The domain object that was operated on (if applicable).
    #: ``None`` for delete operations, or when the item was not found.
    item: T | None = None

    #: Structured extra data for rich consumer output.
    metadata: dict[str, Any] = field(default_factory=dict)

    #: Underlying exception, if any (only populated for "failure" status).
    exception: BaseException | None = None

    @property
    def is_ok(self) -> bool:
        """True if the operation completed without error."""
        return self.status in ("success", "skipped", "warning")

    @property
    def is_error(self) -> bool:
        """True if the operation failed."""
        return self.status in ("error", "failure")


@dataclass
class BatchResult(Generic[T]):
    """
    Result of a batch operation on multiple items.

    Collects per-item :class:`OperationResult` values into a single
    response with aggregated summaries.
    """

    items: list[OperationResult[T]]

    #: Batch-level warnings (distinct from per-item warnings).
    warnings: list[str] = field(default_factory=list)

    #: Batch-level metadata (e.g. duration, parallel vs sequential).
    metadata: dict[str, Any] = field(default_factory=dict)

    @property
    def status_summary(self) -> dict[str, int]:
        """Count of each status across all items."""
        counts: dict[str, int] = {}
        for r in self.items:
            counts[r.status] = counts.get(r.status, 0) + 1
        return counts

    @property
    def successes(self) -> list[OperationResult[T]]:
        return [r for r in self.items if r.status == "success"]

    @property
    def skipped(self) -> list[OperationResult[T]]:
        return [r for r in self.items if r.status == "skipped"]

    @property
    def errors(self) -> list[OperationResult[T]]:
        return [r for r in self.items if r.status in ("error", "failure")]

    @property
    def has_any_error(self) -> bool:
        return any(r.status in ("error", "failure") for r in self.items)

    @property
    def all_ok(self) -> bool:
        return all(r.is_ok for r in self.items)


@dataclass
class ProgressEvent:
    """
    Emitted during long-running operations to inform the consumer
    of progress, phase transitions, or failures.

    The API method accepts an optional ``on_progress`` callback:

    .. code:: python

        def on_progress(event: ProgressEvent) -> None:
            console.log(f"[dim]{event.message}[/dim]")

        result = VMOperation.create(inputs, on_progress=on_progress)
    """

    #: Logical phase name (e.g. "network", "image", "cloud_init", "spawn").
    phase: str

    #: "running" — phase is in progress; "complete" — finished; "failed" — aborted.
    status: Literal["running", "complete", "failed"]

    #: Overall progress percentage (0.0–100.0) if known, else ``None``.
    percent: float | None = None

    #: Human-readable status message for this moment.
    message: str = ""


@dataclass
class NeedsInteraction:
    """
    Returned **instead of** :class:`OperationResult` when the API
    cannot proceed without user input (e.g. sudo escalation).

    The consumer should handle the interaction and call the same
    API method again (retry pattern).

    This is **not** an exception — it is normal control flow.
    """

    #: Machine-readable reason code. Convention: ``<domain>.<interaction_type>``.
    code: str

    #: Human-readable prompt for the consumer to display.
    message: str

    #: Type of interaction:
    #:   "sudo"    — Consumer must spawn a sudo subprocess
    #:   "confirm" — Consumer must ask Yes/No
    #:   "choice"  — Consumer must present multiple options
    #:   "input"   — Consumer must collect free-form text
    input_type: Literal["sudo", "confirm", "choice", "input"]

    #: Structured context for the consumer to act on.
    #: For ``sudo``: ``{"command": "sudo mvm host init"}``
    context: dict[str, Any] = field(default_factory=dict)


__all__ = [
    "BatchResult",
    "NeedsInteraction",
    "OperationResult",
    "OperationStatus",
    "ProgressEvent",
]
