"""Tests for bulk models — BulkResultItem, BulkResult.

Verifies:
- BulkResultItem.success property with and without error
- BulkResult aggregation: successes, failures, has_errors, success_count, failure_count, total
- Empty items edge case
"""

from __future__ import annotations

from mvmctl.models.bulk import BulkResult, BulkResultItem


class TestBulkResultItem:
    """Tests for BulkResultItem[T] dataclass."""

    def test_success_true_when_no_error(self) -> None:
        item = BulkResultItem(item="vm-1")
        assert item.success is True

    def test_success_false_when_error_set(self) -> None:
        item = BulkResultItem(item="vm-1", error=RuntimeError("failed"))
        assert item.success is False

    def test_item_stored_correctly(self) -> None:
        item = BulkResultItem(item="vm-1")
        assert item.item == "vm-1"

    def test_error_stored_correctly(self) -> None:
        exc = ValueError("bad")
        item = BulkResultItem(item="vm-1", error=exc)
        assert item.error is exc

    def test_error_defaults_to_none(self) -> None:
        item = BulkResultItem(item="vm-1")
        assert item.error is None


class TestBulkResult:
    """Tests for BulkResult[T] aggregation properties."""

    def test_successes_returns_successful_items(self) -> None:
        result = BulkResult(items=[
            BulkResultItem(item="vm-1"),
            BulkResultItem(item="vm-2", error=RuntimeError("boom")),
            BulkResultItem(item="vm-3"),
        ])
        assert result.successes == ["vm-1", "vm-3"]

    def test_failures_returns_item_error_tuples(self) -> None:
        exc = RuntimeError("boom")
        result = BulkResult(items=[
            BulkResultItem(item="vm-1"),
            BulkResultItem(item="vm-2", error=exc),
            BulkResultItem(item="vm-3", error=ValueError("bad")),
        ])
        failures = result.failures
        assert len(failures) == 2
        assert failures[0] == ("vm-2", exc)
        assert isinstance(failures[1][1], ValueError)

    def test_has_errors_true_when_any_item_has_error(self) -> None:
        result = BulkResult(items=[
            BulkResultItem(item="vm-1"),
            BulkResultItem(item="vm-2", error=RuntimeError("boom")),
        ])
        assert result.has_errors is True

    def test_has_errors_false_when_all_succeed(self) -> None:
        result = BulkResult(items=[
            BulkResultItem(item="vm-1"),
            BulkResultItem(item="vm-2"),
        ])
        assert result.has_errors is False

    def test_success_count_counts_correctly(self) -> None:
        result = BulkResult(items=[
            BulkResultItem(item="vm-1"),
            BulkResultItem(item="vm-2", error=RuntimeError("boom")),
            BulkResultItem(item="vm-3"),
            BulkResultItem(item="vm-4", error=ValueError("bad")),
        ])
        assert result.success_count == 2

    def test_failure_count_counts_correctly(self) -> None:
        result = BulkResult(items=[
            BulkResultItem(item="vm-1"),
            BulkResultItem(item="vm-2", error=RuntimeError("boom")),
            BulkResultItem(item="vm-3"),
            BulkResultItem(item="vm-4", error=ValueError("bad")),
        ])
        assert result.failure_count == 2

    def test_total_returns_item_count(self) -> None:
        result = BulkResult(items=[
            BulkResultItem(item="vm-1"),
            BulkResultItem(item="vm-2"),
            BulkResultItem(item="vm-3"),
        ])
        assert result.total == 3

    def test_empty_items_successes(self) -> None:
        result = BulkResult(items=[])
        assert result.successes == []

    def test_empty_items_failures(self) -> None:
        result = BulkResult(items=[])
        assert result.failures == []

    def test_empty_items_has_errors_false(self) -> None:
        result = BulkResult(items=[])
        assert result.has_errors is False

    def test_empty_items_success_count_zero(self) -> None:
        result = BulkResult(items=[])
        assert result.success_count == 0

    def test_empty_items_failure_count_zero(self) -> None:
        result = BulkResult(items=[])
        assert result.failure_count == 0

    def test_empty_items_total_zero(self) -> None:
        result = BulkResult(items=[])
        assert result.total == 0

    def test_all_failures_no_successes(self) -> None:
        result = BulkResult(items=[
            BulkResultItem(item="vm-1", error=RuntimeError("a")),
            BulkResultItem(item="vm-2", error=RuntimeError("b")),
        ])
        assert result.successes == []
        assert len(result.failures) == 2
        assert result.success_count == 0
        assert result.failure_count == 2
        assert result.total == 2

    def test_all_successes_no_failures(self) -> None:
        result = BulkResult(items=[
            BulkResultItem(item="vm-1"),
            BulkResultItem(item="vm-2"),
        ])
        assert len(result.successes) == 2
        assert result.failures == []
        assert result.success_count == 2
        assert result.failure_count == 0
        assert result.total == 2
