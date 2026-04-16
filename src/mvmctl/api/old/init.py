"""Configuration wizard API --- database initialisation."""

from __future__ import annotations

__all__ = [
    "init_database",
]


def init_database() -> None:
    """Initialize the local state database.

    Creates the MVMDatabase instance and runs migrations.

    Raises:
        Exception: Any error from the database migration.
    """
    from mvmctl.core.mvm_db import MVMDatabase

    db = MVMDatabase()
    db.migrate()
