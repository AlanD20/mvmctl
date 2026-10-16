"""Strict pytest outcome evidence for the system-test release runner."""

from __future__ import annotations

import json
import os
import stat
from dataclasses import dataclass
from pathlib import Path


PYTEST_OUTCOME_REPORT_ENV = "MVM_PYTEST_OUTCOME_REPORT"
PYTEST_OUTCOME_SCHEMA_VERSION = 1
MAX_PYTEST_OUTCOME_REPORT_BYTES = 4096
MAX_PYTEST_OUTCOME_COUNT = 100_000
_COUNT_FIELDS = (
    "collected",
    "passed",
    "failed",
    "errors",
    "collection_errors",
    "deselected",
    "skipped",
    "xfailed",
    "xpassed",
)
_REPORT_FIELDS = {
    "schema_version",
    *_COUNT_FIELDS,
    "exit_status",
}
_PYTEST_EXIT_STATUSES = {0, 1, 2, 3, 4, 5}


@dataclass(frozen=True)
class PytestOutcomeReport:
    schema_version: int
    collected: int
    passed: int
    failed: int
    errors: int
    collection_errors: int
    deselected: int
    skipped: int
    xfailed: int
    xpassed: int
    exit_status: int


def read_pytest_outcome_report(report_path: Path) -> bytes:
    """Read one local report through a pinned, bounded, non-symlink file."""
    open_flags = os.O_RDONLY | os.O_CLOEXEC
    if hasattr(os, "O_NOFOLLOW"):
        open_flags |= os.O_NOFOLLOW
    try:
        descriptor = os.open(report_path, open_flags)
    except FileNotFoundError as exc:
        raise RuntimeError(
            f"pytest outcome report is missing: {report_path}"
        ) from exc
    except OSError as exc:
        raise RuntimeError(
            f"cannot open pytest outcome report {report_path}: {exc}"
        ) from exc
    try:
        metadata = os.fstat(descriptor)
        if not stat.S_ISREG(metadata.st_mode):
            raise RuntimeError(
                f"pytest outcome report is not a regular file: {report_path}"
            )
        if metadata.st_size > MAX_PYTEST_OUTCOME_REPORT_BYTES:
            raise RuntimeError(
                "pytest outcome report exceeds "
                f"{MAX_PYTEST_OUTCOME_REPORT_BYTES} bytes"
            )
        chunks: list[bytes] = []
        remaining = MAX_PYTEST_OUTCOME_REPORT_BYTES + 1
        while remaining > 0:
            chunk = os.read(descriptor, remaining)
            if not chunk:
                break
            chunks.append(chunk)
            remaining -= len(chunk)
        payload = b"".join(chunks)
        if len(payload) > MAX_PYTEST_OUTCOME_REPORT_BYTES:
            raise RuntimeError(
                "pytest outcome report exceeds "
                f"{MAX_PYTEST_OUTCOME_REPORT_BYTES} bytes"
            )
        return payload
    finally:
        os.close(descriptor)


def require_complete_pytest_outcomes(
    payload: bytes,
    *,
    process_returncode: int,
) -> PytestOutcomeReport:
    """Return exact all-pass evidence or reject the qualification result."""
    if len(payload) > MAX_PYTEST_OUTCOME_REPORT_BYTES:
        raise RuntimeError(
            "pytest outcome report exceeds "
            f"{MAX_PYTEST_OUTCOME_REPORT_BYTES} bytes"
        )
    def reject_duplicate_fields(
        pairs: list[tuple[str, object]],
    ) -> dict[str, object]:
        values_by_name: dict[str, object] = {}
        names_by_casefold: dict[str, str] = {}
        for field, value in pairs:
            if field in values_by_name:
                raise RuntimeError(
                    f"pytest outcome report has duplicate field: {field}"
                )
            folded = field.casefold()
            if folded in names_by_casefold:
                colliding = sorted((names_by_casefold[folded], field))
                raise RuntimeError(
                    "pytest outcome report has case-fold duplicate fields: "
                    f"{', '.join(colliding)}"
                )
            values_by_name[field] = value
            names_by_casefold[folded] = field
        return values_by_name

    try:
        values = json.loads(payload, object_pairs_hook=reject_duplicate_fields)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise RuntimeError(f"pytest outcome report contains malformed JSON: {exc}") from exc
    if not isinstance(values, dict):
        raise RuntimeError("pytest outcome report must be a top-level JSON object")

    provided_fields = set(values)
    missing_fields = sorted(_REPORT_FIELDS - provided_fields)
    if missing_fields:
        raise RuntimeError(
            "pytest outcome report missing fields: "
            f"{', '.join(missing_fields)}"
        )
    unknown_fields = sorted(provided_fields - _REPORT_FIELDS)
    if unknown_fields:
        raise RuntimeError(
            "pytest outcome report has unknown fields: "
            f"{', '.join(unknown_fields)}"
        )

    for field in ("schema_version", *_COUNT_FIELDS, "exit_status"):
        if type(values[field]) is not int:
            raise RuntimeError(
                f"pytest outcome report field {field} must be an integer"
            )
    if values["schema_version"] != PYTEST_OUTCOME_SCHEMA_VERSION:
        raise RuntimeError(
            "pytest outcome report schema version must be "
            f"{PYTEST_OUTCOME_SCHEMA_VERSION}"
        )
    for field in _COUNT_FIELDS:
        value = values[field]
        if value < 0:
            raise RuntimeError(
                f"pytest outcome report field {field} must not be negative"
            )
        if value > MAX_PYTEST_OUTCOME_COUNT:
            raise RuntimeError(
                f"pytest outcome report field {field} exceeds "
                f"{MAX_PYTEST_OUTCOME_COUNT}"
            )
    if values["exit_status"] not in _PYTEST_EXIT_STATUSES:
        raise RuntimeError(
            "pytest outcome report has unknown pytest exit status: "
            f"{values['exit_status']}"
        )
    if type(process_returncode) is not int:
        raise RuntimeError("pytest process return code must be an integer")
    if process_returncode < 0:
        raise RuntimeError("pytest process return code must not be negative")

    report = PytestOutcomeReport(**values)
    classified = (
        report.passed
        + report.failed
        + report.errors
        + report.skipped
        + report.xfailed
        + report.xpassed
    )
    if classified != report.collected:
        raise RuntimeError(
            "pytest outcome counts do not equal collected: "
            f"collected={report.collected}, outcomes={classified}"
        )
    if report.exit_status != process_returncode:
        raise RuntimeError(
            "pytest outcome exit status does not match process return code: "
            f"report={report.exit_status}, process={process_returncode}"
        )

    incomplete = [
        f"{field}={getattr(report, field)}"
        for field in (
            "failed",
            "errors",
            "collection_errors",
            "deselected",
            "skipped",
            "xfailed",
            "xpassed",
        )
        if getattr(report, field) != 0
    ]
    if incomplete:
        raise RuntimeError(
            "pytest outcomes are incomplete: " + ", ".join(incomplete)
        )
    if report.collected == 0:
        raise RuntimeError("pytest collection is empty")
    if report.exit_status != 0:
        raise RuntimeError(
            f"pytest exit status is not successful: {report.exit_status}"
        )
    if report.passed != report.collected:
        raise RuntimeError("pytest did not pass every collected test")
    return report
