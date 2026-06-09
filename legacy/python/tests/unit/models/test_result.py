"""Tests for result models — OperationResult, BatchResult, ProgressEvent, NeedsInteraction.

Verifies:
- OperationResult.is_ok / is_error property contracts
- Default values for optional fields
- BatchResult aggregation properties (status_summary, successes, skipped, errors, has_any_error, all_ok)
- Empty items edge case
- ProgressEvent field storage and defaults
- NeedsInteraction field storage and defaults
"""

from __future__ import annotations

from mvmctl.models.result import (
    BatchResult,
    NeedsInteraction,
    OperationResult,
    ProgressEvent,
)


class TestOperationResult:
    """Tests for OperationResult[T] dataclass and properties."""

    def test_is_ok_success(self) -> None:
        r = OperationResult(status="success", code="vm.create")
        assert r.is_ok is True

    def test_is_ok_skipped(self) -> None:
        r = OperationResult(status="skipped", code="vm.create")
        assert r.is_ok is True

    def test_is_ok_warning(self) -> None:
        r = OperationResult(status="warning", code="vm.create")
        assert r.is_ok is True

    def test_is_error_error(self) -> None:
        r = OperationResult(status="error", code="vm.create")
        assert r.is_error is True

    def test_is_error_failure(self) -> None:
        r = OperationResult(status="failure", code="vm.create")
        assert r.is_error is True

    def test_is_ok_and_is_error_are_opposites_success(self) -> None:
        r = OperationResult(status="success", code="vm.create")
        assert r.is_ok is True
        assert r.is_error is False

    def test_is_ok_and_is_error_are_opposites_error(self) -> None:
        r = OperationResult(status="error", code="vm.create")
        assert r.is_ok is False
        assert r.is_error is True

    def test_default_message_is_empty(self) -> None:
        r = OperationResult(status="success", code="vm.create")
        assert r.message == ""

    def test_default_item_is_none(self) -> None:
        r = OperationResult(status="success", code="vm.create")
        assert r.item is None

    def test_default_metadata_is_empty_dict(self) -> None:
        r = OperationResult(status="success", code="vm.create")
        assert r.metadata == {}

    def test_default_exception_is_none(self) -> None:
        r = OperationResult(status="success", code="vm.create")
        assert r.exception is None

    def test_item_stored(self) -> None:
        r = OperationResult(status="success", code="vm.create", item="myvm")
        assert r.item == "myvm"

    def test_exception_stored(self) -> None:
        exc = RuntimeError("boom")
        r = OperationResult(status="failure", code="vm.create", exception=exc)
        assert r.exception is exc


class TestBatchResult:
    """Tests for BatchResult[T] aggregation properties."""

    def test_status_summary_counts_correctly(self) -> None:
        items = [
            OperationResult(status="success", code="a"),
            OperationResult(status="success", code="b"),
            OperationResult(status="skipped", code="c"),
            OperationResult(status="error", code="d"),
            OperationResult(status="failure", code="e"),
        ]
        batch = BatchResult(items=items)
        assert batch.status_summary == {
            "success": 2,
            "skipped": 1,
            "error": 1,
            "failure": 1,
        }

    def test_successes_returns_only_success_items(self) -> None:
        items = [
            OperationResult(status="success", code="a"),
            OperationResult(status="skipped", code="b"),
            OperationResult(status="error", code="c"),
        ]
        batch = BatchResult(items=items)
        assert len(batch.successes) == 1
        assert batch.successes[0].code == "a"

    def test_skipped_returns_only_skipped_items(self) -> None:
        items = [
            OperationResult(status="success", code="a"),
            OperationResult(status="skipped", code="b"),
            OperationResult(status="skipped", code="c"),
        ]
        batch = BatchResult(items=items)
        assert len(batch.skipped) == 2
        assert all(r.status == "skipped" for r in batch.skipped)

    def test_errors_returns_error_and_failure_items(self) -> None:
        items = [
            OperationResult(status="success", code="a"),
            OperationResult(status="error", code="b"),
            OperationResult(status="failure", code="c"),
            OperationResult(status="warning", code="d"),
        ]
        batch = BatchResult(items=items)
        assert len(batch.errors) == 2
        assert batch.errors[0].code == "b"
        assert batch.errors[1].code == "c"

    def test_has_any_error_false_when_all_ok(self) -> None:
        items = [
            OperationResult(status="success", code="a"),
            OperationResult(status="skipped", code="b"),
        ]
        batch = BatchResult(items=items)
        assert batch.has_any_error is False

    def test_has_any_error_true_when_any_error(self) -> None:
        items = [
            OperationResult(status="success", code="a"),
            OperationResult(status="error", code="b"),
        ]
        batch = BatchResult(items=items)
        assert batch.has_any_error is True

    def test_has_any_error_true_when_any_failure(self) -> None:
        items = [
            OperationResult(status="success", code="a"),
            OperationResult(status="failure", code="b"),
        ]
        batch = BatchResult(items=items)
        assert batch.has_any_error is True

    def test_all_ok_true_when_all_is_ok(self) -> None:
        items = [
            OperationResult(status="success", code="a"),
            OperationResult(status="warning", code="b"),
        ]
        batch = BatchResult(items=items)
        assert batch.all_ok is True

    def test_all_ok_false_when_any_error(self) -> None:
        items = [
            OperationResult(status="success", code="a"),
            OperationResult(status="error", code="b"),
        ]
        batch = BatchResult(items=items)
        assert batch.all_ok is False

    def test_all_ok_false_when_any_failure(self) -> None:
        items = [
            OperationResult(status="success", code="a"),
            OperationResult(status="failure", code="b"),
        ]
        batch = BatchResult(items=items)
        assert batch.all_ok is False

    def test_empty_items_status_summary(self) -> None:
        batch = BatchResult(items=[])
        assert batch.status_summary == {}

    def test_empty_items_successes(self) -> None:
        batch = BatchResult(items=[])
        assert batch.successes == []

    def test_empty_items_skipped(self) -> None:
        batch = BatchResult(items=[])
        assert batch.skipped == []

    def test_empty_items_errors(self) -> None:
        batch = BatchResult(items=[])
        assert batch.errors == []

    def test_empty_items_has_any_error_false(self) -> None:
        batch = BatchResult(items=[])
        assert batch.has_any_error is False

    def test_empty_items_all_ok_true(self) -> None:
        batch = BatchResult(items=[])
        assert batch.all_ok is True

    def test_warnings_and_metadata_defaults(self) -> None:
        batch = BatchResult(items=[])
        assert batch.warnings == []
        assert batch.metadata == {}


class TestProgressEvent:
    """Tests for ProgressEvent dataclass."""

    def test_fields_stored_correctly(self) -> None:
        event = ProgressEvent(
            phase="network",
            status="running",
            percent=50.0,
            message="Configuring bridge",
        )
        assert event.phase == "network"
        assert event.status == "running"
        assert event.percent == 50.0
        assert event.message == "Configuring bridge"

    def test_percent_defaults_to_none(self) -> None:
        event = ProgressEvent(phase="network", status="running")
        assert event.percent is None

    def test_message_defaults_to_empty(self) -> None:
        event = ProgressEvent(phase="network", status="complete")
        assert event.message == ""

    def test_complete_status(self) -> None:
        event = ProgressEvent(phase="spawn", status="complete")
        assert event.status == "complete"

    def test_failed_status(self) -> None:
        event = ProgressEvent(phase="image", status="failed")
        assert event.status == "failed"


class TestNeedsInteraction:
    """Tests for NeedsInteraction dataclass."""

    def test_fields_stored_correctly(self) -> None:
        ni = NeedsInteraction(
            code="host.sudo",
            message="Sudo required",
            input_type="sudo",
            context={"command": "sudo mvm host init"},
        )
        assert ni.code == "host.sudo"
        assert ni.message == "Sudo required"
        assert ni.input_type == "sudo"
        assert ni.context == {"command": "sudo mvm host init"}

    def test_context_defaults_to_empty_dict(self) -> None:
        ni = NeedsInteraction(
            code="host.sudo", message="Sudo required", input_type="sudo"
        )
        assert ni.context == {}

    def test_confirm_input_type(self) -> None:
        ni = NeedsInteraction(
            code="user.confirm", message="Proceed?", input_type="confirm"
        )
        assert ni.input_type == "confirm"

    def test_choice_input_type(self) -> None:
        ni = NeedsInteraction(
            code="user.choice", message="Pick one", input_type="choice"
        )
        assert ni.input_type == "choice"

    def test_input_input_type(self) -> None:
        ni = NeedsInteraction(
            code="user.input", message="Enter value", input_type="input"
        )
        assert ni.input_type == "input"
