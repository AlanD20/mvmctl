"""Atomic operations ensuring system and DB consistency.

This module provides patterns for atomic operations that require both system-level
changes and database persistence. If the database operation fails, the system change
is rolled back.

Used by: iptables rules, network creation, VM registration
"""

from __future__ import annotations

from contextlib import contextmanager
from typing import Callable, Generator, Generic, Literal, TypeVar

T = TypeVar("T")


class AtomicOperation(Generic[T]):
    """Ensures system operation + DB write are atomic.

    Pattern:
        1. Perform system operation
        2. Write to database
        3. On DB failure, rollback system operation

    Usage:
        with AtomicOperation(
            system_op=lambda: create_iptables_rule(rule),
            db_op=lambda result: db.record_rule(result),
            rollback_op=lambda result: tracker.remove_rule(result)
        ) as op:
            # Validate result if needed
            op.commit()  # This calls db_op

    If an exception occurs before commit(), rollback_op is called automatically.
    """

    def __init__(
        self,
        system_op: Callable[[], T],
        db_op: Callable[[T], None],
        rollback_op: Callable[[T], None],
    ):
        self.system_op = system_op
        self.db_op = db_op
        self.rollback_op = rollback_op
        self.result: T | None = None
        self.committed = False

    def __enter__(self) -> AtomicOperation[T]:
        """Execute system operation and return self for context manager."""
        self.result = self.system_op()
        return self

    def __exit__(
        self,
        exc_type: type[BaseException] | None,
        exc_val: BaseException | None,
        exc_tb: object | None,
    ) -> Literal[False]:
        """Handle cleanup if exception occurred before commit."""
        if exc_val is not None and self.result is not None and not self.committed:
            # Exception occurred before commit — rollback
            self.rollback_op(self.result)
        return False  # Don't suppress exception

    def commit(self) -> None:
        """Commit the operation by writing to database."""
        if self.result is None:
            raise RuntimeError("No result to commit - did system_op fail?")
        self.db_op(self.result)
        self.committed = True


@contextmanager
def atomic_operation(
    system_op: Callable[[], T],
    db_op: Callable[[T], None],
    rollback_op: Callable[[T], None],
) -> Generator[AtomicOperation[T], None, None]:
    """Context manager for atomic operations.

    Simpler syntax than using AtomicOperation class directly.

    Usage:
        with atomic_operation(
            lambda: create_iptables_rule(rule),
            lambda result: db.record_rule(result),
            lambda result: tracker.remove_rule(result)
        ) as op:
            # op.result contains the system operation result
            # Validate if needed
            op.commit()
    """
    op = AtomicOperation(system_op, db_op, rollback_op)
    try:
        yield op
    except Exception:
        # Rollback happens in __exit__ if not committed
        raise


__all__ = [
    "AtomicOperation",
    "atomic_operation",
]
