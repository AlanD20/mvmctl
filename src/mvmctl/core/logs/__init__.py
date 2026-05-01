"""Log retrieval domain — VM boot and OS log viewing."""

from __future__ import annotations

from mvmctl.core.logs._controller import LogController
from mvmctl.core.logs._service import LogService

__all__ = [
    "LogController",
    "LogService",
]
