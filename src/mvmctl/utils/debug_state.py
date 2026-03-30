"""Module-level debug state for mvmctl.

This module provides a simple global state for debug mode that avoids
passing Click context objects throughout the codebase.
"""

from mvmctl.constants import DEBUG_MODE

_debug_mode: bool = DEBUG_MODE


def set_debug_mode(value: bool) -> None:
    """Set the debug mode state. Called once from main.py CLI callback."""
    global _debug_mode
    _debug_mode = value


def is_debug_mode() -> bool:
    return _debug_mode
