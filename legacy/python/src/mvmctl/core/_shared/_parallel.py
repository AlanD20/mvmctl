"""Parallel execution with batching for resource control."""

from __future__ import annotations

from collections.abc import Callable
from concurrent.futures import ThreadPoolExecutor, as_completed
from typing import TypeVar

T = TypeVar("T")
R = TypeVar("R")


class ParallelExecutor:
    """
    Reusable parallel execution with batching.

    Sequential mode: fail-fast on first error.
    Parallel mode: process all items, collect errors, continue on failure.
    """

    def execute(
        self,
        items: list[T],
        func: Callable[[T], R],
        parallel: bool = False,
        max_workers: int | None = None,
        batch_size: int | None = None,
    ) -> list[tuple[T, R | Exception]]:
        """
        Execute func on each item.

        Args:
            items: Items to process.
            func: Function to apply to each item. Must accept one argument.
            parallel: If True, use ThreadPoolExecutor with batching.
            max_workers: Max concurrent threads. None = auto-calculate based on
                CPU count and item count.
            batch_size: Items per batch. None = auto-calculate (single batch).

        Returns:
            List of (item, result_or_error) tuples.
            - Sequential: stops on first error (fail-fast).
            - Parallel: processes all items, collects all errors.

        """
        if parallel:
            import os

            n = len(items)
            if max_workers is None:
                max_workers = min(n, (os.cpu_count() or 4) * 2)
            if batch_size is None:
                batch_size = n  # single batch — no sequential staging
            if max_workers < 1:
                max_workers = 1
            if batch_size < 1:
                batch_size = 1
            return self._parallel(items, func, max_workers, batch_size)
        return self._sequential(items, func)

    def _sequential(
        self, items: list[T], func: Callable[[T], R]
    ) -> list[tuple[T, R | Exception]]:
        results: list[tuple[T, R | Exception]] = []
        for item in items:
            try:
                result = func(item)
                results.append((item, result))
            except Exception as exc:
                results.append((item, exc))
                break  # Fail-fast
        return results

    def _parallel(
        self,
        items: list[T],
        func: Callable[[T], R],
        max_workers: int,
        batch_size: int,
    ) -> list[tuple[T, R | Exception]]:
        all_results: list[tuple[T, R | Exception]] = []
        for i in range(0, len(items), batch_size):
            batch = items[i : i + batch_size]
            batch_results = self._execute_batch(batch, func, max_workers)
            all_results.extend(batch_results)
        return all_results

    def _execute_batch(
        self,
        batch: list[T],
        func: Callable[[T], R],
        max_workers: int,
    ) -> list[tuple[T, R | Exception]]:
        results: list[tuple[T, R | Exception]] = []
        with ThreadPoolExecutor(max_workers=max_workers) as executor:
            futures = {executor.submit(func, item): item for item in batch}
            for future in as_completed(futures):
                item = futures[future]
                try:
                    results.append((item, future.result()))
                except Exception as exc:
                    results.append((item, exc))
        return results


__all__ = ["ParallelExecutor"]
