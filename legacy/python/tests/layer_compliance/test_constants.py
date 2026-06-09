"""Verify constants.py is the single source of truth and follows conventions.

Architecture Rule: All defaults must be in constants.py.
Configuration priority (lowest -> highest):
1. constants.py DEFAULT_* / CONST_* values
2. ~/.config/mvmctl/config.json
3. MVM_* environment variables
4. CLI flags
"""

from __future__ import annotations

import re
from pathlib import Path

import pytest

PROJECT_ROOT = Path(__file__).parent.parent.parent / "src" / "mvmctl"
CONSTANTS_FILE = PROJECT_ROOT / "constants.py"


class TestConstantsFile:
    """Verify constants.py follows conventions."""

    def test_constants_has_const_values(self) -> None:
        """Verify constants.py defines CONST_* values."""
        if not CONSTANTS_FILE.exists():
            pytest.skip("constants.py not found")
        content = CONSTANTS_FILE.read_text()
        const_pattern = re.compile(r"CONST_\w+(?::\s*\w+(?:\[\w+\])?)?\s*=")
        matches = const_pattern.findall(content)
        assert len(matches) >= 5, (
            f"Expected at least 5 CONST_* values in constants.py, found {len(matches)}"
        )

    def test_constants_has_default_values(self) -> None:
        """Verify constants.py defines DEFAULT_* values."""
        if not CONSTANTS_FILE.exists():
            pytest.skip("constants.py not found")
        content = CONSTANTS_FILE.read_text()
        default_pattern = re.compile(r"DEFAULT_\w+(?::\s*\w+(?:\[\w+\])?)?\s*=")
        matches = default_pattern.findall(content)
        assert len(matches) >= 5, (
            f"Expected at least 5 DEFAULT_* values in constants.py, found {len(matches)}"
        )
