"""Internal shared utilities for API modules.

WARNING: This module is INTERNAL to the API layer. It is NOT public API.
- Other _internal/* modules can import from here
- api/* modules can import from here
- cli/* and core/* modules CANNOT import from here

See docs/plans/vms-api-refactoring.md for boundary rules.
"""

from __future__ import annotations

from mvmctl.api._internal._key_manager import KeyManager

__all__ = [
    "KeyManager",
]
