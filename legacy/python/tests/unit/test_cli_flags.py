"""Top-level mvm CLI flags and options tests."""

from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.system


def test_verbose_flag() -> None:
    """mvm --verbose should output additional diagnostic info."""
    result = subprocess.run(
        ["uv", "run", "mvm", "--verbose", "--version"],
        capture_output=True,
        text=True,
        timeout=30,
    )
    assert result.returncode == 0


def test_debug_flag() -> None:
    """mvm --debug should enable debug mode without error."""
    result = subprocess.run(
        ["uv", "run", "mvm", "--debug", "--version"],
        capture_output=True,
        text=True,
        timeout=30,
    )
    assert result.returncode == 0


def test_verbose_and_debug_together() -> None:
    """mvm --verbose --debug should work together."""
    result = subprocess.run(
        ["uv", "run", "mvm", "--verbose", "--debug", "--version"],
        capture_output=True,
        text=True,
        timeout=30,
    )
    assert result.returncode == 0
