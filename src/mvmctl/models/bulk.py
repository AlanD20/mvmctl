"""Bulk operation result tracking."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Generic, TypeVar

T = TypeVar("T")


@dataclass
class BulkResultItem(Generic[T]):
    """Result of a single item in a bulk operation."""

    item: T
    error: Exception | None = None

    @property
    def success(self) -> bool:
        return self.error is None


@dataclass
class BulkResult(Generic[T]):
    """Aggregated results of a bulk operation."""

    items: list[BulkResultItem[T]]

    @property
    def successes(self) -> list[T]:
        return [i.item for i in self.items if i.success]

    @property
    def failures(self) -> list[tuple[T, Exception]]:
        return [(i.item, i.error) for i in self.items if i.error is not None]

    @property
    def has_errors(self) -> bool:
        return any(i.error is not None for i in self.items)

    @property
    def success_count(self) -> int:
        return sum(1 for i in self.items if i.success)

    @property
    def failure_count(self) -> int:
        return sum(1 for i in self.items if i.error is not None)

    @property
    def total(self) -> int:
        return len(self.items)


__all__ = ["BulkResult", "BulkResultItem"]
